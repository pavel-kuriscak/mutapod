// Package gcp implements the Provider interface for Google Cloud Platform.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
	"github.com/mutapod/mutapod/internal/sshrun"
)

var (
	trustHostFunc = func(client *sshrun.Client, knownHostsFile, hostKeyAlias string) error {
		return client.TrustHost(knownHostsFile, hostKeyAlias)
	}
	sshReadyTimeout     = 5 * time.Minute
	sshReadyRetryPeriod = 2 * time.Second
)

func init() {
	provider.Register("gcp", func(cfg *config.Config, cmd shell.Commander) (provider.Provider, error) {
		return New(cfg, cmd), nil
	})
}

// Provider implements provider.Provider for GCP Compute Engine VMs.
type Provider struct {
	cfg    *config.Config
	cmd    shell.Commander
	name   string              // instance name = mutapod-<workspace>
	sshCfg *provider.SSHConfig // populated after SSHConfig() is called
}

// New creates a new GCP Provider.
func New(cfg *config.Config, cmd shell.Commander) *Provider {
	return &Provider{
		cfg:  cfg,
		cmd:  cmd,
		name: cfg.InstanceName(),
	}
}

func (p *Provider) Name() string { return "gcp" }

func (p *Provider) PreferredSyncBackend() provider.SyncBackend {
	return provider.SyncMutagen
}

func (p *Provider) ForwardedWorkspacePath() string {
	return p.cfg.WorkspacePath()
}

// State returns the current instance state.
func (p *Provider) State(ctx context.Context) (provider.InstanceState, error) {
	out, err := p.cmd.Output(ctx, shell.RunOptions{}, "gcloud", "compute", "instances", "describe",
		p.name,
		"--project", p.cfg.Provider.GCP.Project,
		"--zone", p.cfg.Provider.GCP.Zone,
		"--format", "json(status)",
	)
	if err != nil {
		if isNotFound(err) {
			return provider.StateNotFound, nil
		}
		return provider.StateUnknown, fmt.Errorf("gcp: describe instance: %w", err)
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return provider.StateUnknown, fmt.Errorf("gcp: parse instance status: %w", err)
	}
	return gcpStatusToState(result.Status), nil
}

func gcpStatusToState(s string) provider.InstanceState {
	switch strings.ToUpper(s) {
	case "RUNNING":
		return provider.StateRunning
	case "TERMINATED", "STOPPED":
		return provider.StateStopped
	case "STOPPING":
		return provider.StateStopping
	case "STAGING", "PROVISIONING":
		return provider.StateStarting
	default:
		return provider.StateUnknown
	}
}

// EnsureInstance creates the instance if absent, or starts it if stopped.
func (p *Provider) EnsureInstance(ctx context.Context) (provider.InstanceState, error) {
	state, err := p.State(ctx)
	if err != nil {
		return provider.StateUnknown, err
	}

	switch state {
	case provider.StateRunning:
		shell.Debugf("gcp: instance %s is already running", p.name)
		return provider.StateRunning, nil

	case provider.StateNotFound:
		shell.Debugf("gcp: creating instance %s", p.name)
		if err := p.createInstance(ctx); err != nil {
			return provider.StateUnknown, err
		}

	case provider.StateStopped:
		shell.Debugf("gcp: starting stopped instance %s", p.name)
		if err := p.startInstance(ctx); err != nil {
			return provider.StateUnknown, err
		}

	case provider.StateStarting, provider.StateStopping:
		shell.Debugf("gcp: instance %s is transitioning (%s), waiting...", p.name, state)

	default:
		return provider.StateUnknown, fmt.Errorf("gcp: instance in unexpected state %q", state)
	}

	return p.waitForRunning(ctx)
}

func (p *Provider) createInstance(ctx context.Context) error {
	gcp := p.cfg.Provider.GCP
	args := []string{
		"compute", "instances", "create", p.name,
		"--project", gcp.Project,
		"--zone", gcp.Zone,
		"--machine-type", gcp.MachineType,
		"--boot-disk-size", fmt.Sprintf("%dGB", gcp.DiskSizeGB),
		"--boot-disk-type", gcp.DiskType,
		"--image-family", gcp.ImageFamily,
		"--image-project", gcp.ImageProject,
	}
	if gcp.Network != "" {
		args = append(args, "--network", gcp.Network)
	}
	if gcp.Subnet != "" {
		args = append(args, "--subnet", gcp.Subnet)
	}
	if gcp.ServiceAccount != "" {
		args = append(args, "--service-account", gcp.ServiceAccount)
	}
	for _, tag := range gcp.Tags {
		args = append(args, "--tags", tag)
	}
	if gcp.Spot {
		args = append(args, "--provisioning-model", "SPOT")
	} else if gcp.Preemptible {
		args = append(args, "--preemptible")
	}
	if len(gcp.Labels) > 0 {
		args = append(args, "--labels", formatLabels(gcp.Labels))
	}
	args = append(args, "--format", "json")

	return p.cmd.Run(ctx, shell.RunOptions{}, "gcloud", args...)
}

func (p *Provider) startInstance(ctx context.Context) error {
	return p.cmd.Run(ctx, shell.RunOptions{}, "gcloud", "compute", "instances", "start",
		p.name,
		"--project", p.cfg.Provider.GCP.Project,
		"--zone", p.cfg.Provider.GCP.Zone,
	)
}

// waitForRunning polls until the instance is RUNNING, with a 10-minute timeout.
func (p *Provider) waitForRunning(ctx context.Context) (provider.InstanceState, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return provider.StateUnknown, ctx.Err()
		default:
		}
		state, err := p.State(ctx)
		if err != nil {
			return provider.StateUnknown, err
		}
		if state == provider.StateRunning {
			return provider.StateRunning, nil
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("gcp: timed out waiting for instance %s to reach RUNNING (current: %s)", p.name, state)
		}
		shell.Debugf("gcp: waiting for instance %s to be RUNNING (current: %s)", p.name, state)
		select {
		case <-ctx.Done():
			return provider.StateUnknown, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// SSHConfig injects the instance SSH config via gcloud and returns connection params.
func (p *Provider) SSHConfig(ctx context.Context) (*provider.SSHConfig, error) {
	gcp := p.cfg.Provider.GCP

	// Inject host alias into ~/.ssh/config so mutagen can connect by alias.
	if err := p.cmd.Run(ctx, shell.RunOptions{}, "gcloud", "compute", "config-ssh",
		"--project", gcp.Project,
	); err != nil {
		return nil, fmt.Errorf("gcp: config-ssh: %w", err)
	}

	// Get external IP for direct Go SSH connections (no PuTTY/openssh required).
	out, err := p.cmd.Output(ctx, shell.RunOptions{}, "gcloud", "compute", "instances", "describe",
		p.name,
		"--project", gcp.Project,
		"--zone", gcp.Zone,
		"--format", "json(networkInterfaces[0].accessConfigs[0].natIP)",
	)
	if err != nil {
		return nil, fmt.Errorf("gcp: get instance IP: %w", err)
	}
	var ipResult struct {
		NetworkInterfaces []struct {
			AccessConfigs []struct {
				NatIP string `json:"natIP"`
			} `json:"accessConfigs"`
		} `json:"networkInterfaces"`
	}
	if err := json.Unmarshal(out, &ipResult); err != nil || len(ipResult.NetworkInterfaces) == 0 {
		return nil, fmt.Errorf("gcp: parse instance IP: %w", err)
	}
	ip := ipResult.NetworkInterfaces[0].AccessConfigs[0].NatIP

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("gcp: home dir: %w", err)
	}

	host := fmt.Sprintf("%s.%s.%s", p.name, gcp.Zone, gcp.Project)

	sshConfigPath := filepath.Join(home, ".ssh", "config")
	entry := parseSSHConfigEntry(sshConfigPath, host)
	invocation, err := p.resolveSSHInvocation(ctx)
	if err != nil {
		shell.Debugf("gcp: compute ssh --dry-run unavailable, falling back to ssh config heuristics: %v", err)
	}

	sshUser := invocation.User
	if sshUser == "" {
		sshUser = entry.User
	}
	if sshUser == "" {
		sshUser = gcpSSHUsername()
	}
	identityFile := invocation.IdentityFile
	if strings.EqualFold(filepath.Ext(identityFile), ".ppk") {
		// gcloud may use PuTTY on Windows, but our pure-Go SSH client needs the
		// OpenSSH-formatted private key from ~/.ssh/config.
		identityFile = ""
	}
	if identityFile == "" {
		identityFile = entry.IdentityFile
	}
	if identityFile == "" {
		identityFile = filepath.Join(home, ".ssh", "google_compute_engine")
	}
	if strings.HasPrefix(identityFile, "~/") {
		identityFile = filepath.Join(home, identityFile[2:])
	}

	shell.Debugf("gcp: SSH user=%s identityFile=%s", sshUser, identityFile)

	cfg := &provider.SSHConfig{
		Host:         host,
		IP:           ip,
		Port:         22,
		User:         sshUser,
		IdentityFile: identityFile,
	}
	p.sshCfg = cfg

	// Populate the known_hosts file that mutagen (and gcloud) uses, so host
	// key verification succeeds without ever running an interactive SSH client.
	knownHostsFile := invocation.KnownHostsFile
	if knownHostsFile == "" {
		knownHostsFile = entry.KnownHostsFile
	}
	if knownHostsFile == "" {
		knownHostsFile = filepath.Join(home, ".ssh", "google_compute_known_hosts")
	}
	if strings.HasPrefix(knownHostsFile, "~/") {
		knownHostsFile = filepath.Join(home, knownHostsFile[2:])
	}
	hostKeyAlias := invocation.HostKeyAlias
	if hostKeyAlias == "" {
		hostKeyAlias = entry.HostKeyAlias
	}
	if hostKeyAlias == "" {
		hostKeyAlias = ip
	}
	client := sshrun.New(ip, 22, sshUser, identityFile)
	if err := retrySSHReady(ctx, "trust host key", func() error {
		return trustHostFunc(client, knownHostsFile, hostKeyAlias)
	}); err != nil {
		return nil, fmt.Errorf("gcp: trust host key: %w", err)
	}
	shell.Debugf("gcp: host key trusted in %s for alias %s", knownHostsFile, hostKeyAlias)

	return cfg, nil
}

type sshInvocation struct {
	User           string
	IdentityFile   string
	KnownHostsFile string
	HostKeyAlias   string
}

func (p *Provider) resolveSSHInvocation(ctx context.Context) (sshInvocation, error) {
	out, err := p.cmd.Output(ctx, shell.RunOptions{}, "gcloud", "compute", "ssh",
		p.name,
		"--project", p.cfg.Provider.GCP.Project,
		"--zone", p.cfg.Provider.GCP.Zone,
		"--dry-run",
	)
	if err != nil {
		return sshInvocation{}, fmt.Errorf("gcp: compute ssh --dry-run: %w", err)
	}
	info, err := parseSSHInvocation(string(out))
	if err != nil {
		return sshInvocation{}, fmt.Errorf("gcp: parse compute ssh --dry-run: %w", err)
	}
	if info.User == "" {
		return sshInvocation{}, fmt.Errorf("gcp: compute ssh --dry-run did not include an SSH username")
	}
	return info, nil
}

// sshConfigEntry holds the fields we care about from a ~/.ssh/config Host block.
type sshConfigEntry struct {
	User           string
	IdentityFile   string
	KnownHostsFile string // UserKnownHostsFile
	HostKeyAlias   string
}

// parseSSHConfigEntry reads ~/.ssh/config and returns the fields for the given hostname block.
func parseSSHConfigEntry(configPath, hostname string) sshConfigEntry {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return sshConfigEntry{}
	}
	var e sshConfigEntry
	var inBlock bool
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "host ") {
			inBlock = strings.TrimSpace(line[5:]) == hostname
			continue
		}
		if !inBlock {
			continue
		}
		switch {
		case strings.HasPrefix(lower, "user "):
			e.User = strings.TrimSpace(line[5:])
		case strings.HasPrefix(lower, "identityfile "):
			e.IdentityFile = strings.TrimSpace(line[13:])
		case strings.HasPrefix(lower, "userknownhostsfile"):
			// handles both "UserKnownHostsFile file" and "UserKnownHostsFile=file"
			rest := strings.TrimLeft(line[18:], " \t=")
			e.KnownHostsFile = strings.Fields(rest)[0]
		case strings.HasPrefix(lower, "hostkeyalias"):
			rest := strings.TrimLeft(line[12:], " \t=")
			e.HostKeyAlias = rest
		}
	}
	return e
}

func parseSSHInvocation(raw string) (sshInvocation, error) {
	tokens, err := splitCommandLine(strings.TrimSpace(raw))
	if err != nil {
		return sshInvocation{}, err
	}
	if len(tokens) == 0 {
		return sshInvocation{}, fmt.Errorf("empty command line")
	}

	var info sshInvocation
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		switch token {
		case "-i":
			if i+1 >= len(tokens) {
				return sshInvocation{}, fmt.Errorf("missing value after -i")
			}
			info.IdentityFile = tokens[i+1]
			i++
		case "-l":
			if i+1 >= len(tokens) {
				return sshInvocation{}, fmt.Errorf("missing value after -l")
			}
			info.User = tokens[i+1]
			i++
		case "-o":
			if i+1 >= len(tokens) {
				return sshInvocation{}, fmt.Errorf("missing value after -o")
			}
			consumed, err := applySSHOption(&info, tokens, i+1)
			if err != nil {
				return sshInvocation{}, err
			}
			i += consumed
		default:
			if strings.HasPrefix(token, "-o") && len(token) > 2 {
				if err := applySSHOptionToken(&info, token[2:]); err != nil {
					return sshInvocation{}, err
				}
				continue
			}
			if strings.HasPrefix(token, "-") {
				continue
			}
			if info.User == "" && strings.Count(token, "@") == 1 {
				parts := strings.SplitN(token, "@", 2)
				info.User = parts[0]
			}
		}
	}
	return info, nil
}

func applySSHOption(info *sshInvocation, tokens []string, valueIndex int) (int, error) {
	option := tokens[valueIndex]
	if strings.Contains(option, "=") {
		return 1, applySSHOptionToken(info, option)
	}
	if valueIndex+1 >= len(tokens) {
		return 1, fmt.Errorf("missing value for ssh option %q", option)
	}
	return 2, applySSHOptionToken(info, option+"="+tokens[valueIndex+1])
}

func applySSHOptionToken(info *sshInvocation, option string) error {
	parts := strings.SplitN(option, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid ssh option %q", option)
	}
	switch strings.ToLower(parts[0]) {
	case "userknownhostsfile":
		info.KnownHostsFile = parts[1]
	case "hostkeyalias":
		info.HostKeyAlias = parts[1]
	case "identityfile":
		info.IdentityFile = parts[1]
	}
	return nil
}

func splitCommandLine(s string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	var quote rune

	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	flush()
	return tokens, nil
}

// gcpSSHUsername is a fallback when gcloud does not expose the effective SSH
// user via `gcloud compute ssh --dry-run`.
func gcpSSHUsername() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Username
	if idx := strings.LastIndex(name, `\`); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.ToLower(name)
}

// sshClient returns a pure-Go SSH client using the cached SSHConfig.
func (p *Provider) sshClient() (*sshrun.Client, error) {
	if p.sshCfg == nil {
		return nil, fmt.Errorf("gcp: SSHConfig must be called before Exec/CopyFile")
	}
	return sshrun.New(p.sshCfg.IP, p.sshCfg.Port, p.sshCfg.User, p.sshCfg.IdentityFile), nil
}

// Exec runs a command on the remote instance.
// Non-interactive commands use pure-Go SSH (no PuTTY/openssh).
// Interactive TTY sessions fall back to gcloud compute ssh.
func (p *Provider) Exec(ctx context.Context, cmd []string, opts provider.ExecOptions) error {
	if opts.Tty {
		// Interactive shell: delegate to gcloud which handles PTY/terminal correctly.
		gcp := p.cfg.Provider.GCP
		args := []string{
			"compute", "ssh", p.name,
			"--project", gcp.Project,
			"--zone", gcp.Zone,
			"--",
		}
		args = append(args, cmd...)
		return p.cmd.Run(ctx, shell.RunOptions{
			Stdin:  opts.Stdin,
			Stdout: opts.Stdout,
			Stderr: opts.Stderr,
		}, "gcloud", args...)
	}

	client, err := p.sshClient()
	if err != nil {
		return err
	}
	stdout := opts.Stdout
	stderr := opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return client.Run(ctx, joinShellArgs(cmd), opts.Stdin, stdout, stderr)
}

// CopyFile copies a local file to the remote instance via pure-Go SSH.
func (p *Provider) CopyFile(ctx context.Context, localPath, remotePath string) error {
	client, err := p.sshClient()
	if err != nil {
		return err
	}
	return client.Upload(ctx, localPath, remotePath)
}

// StopInstance stops the instance.
func (p *Provider) StopInstance(ctx context.Context) error {
	state, err := p.State(ctx)
	if err != nil {
		return err
	}
	if state == provider.StateStopped || state == provider.StateNotFound {
		return nil // already stopped
	}
	return p.cmd.Run(ctx, shell.RunOptions{}, "gcloud", "compute", "instances", "stop",
		p.name,
		"--project", p.cfg.Provider.GCP.Project,
		"--zone", p.cfg.Provider.GCP.Zone,
	)
}

// DeleteInstance destroys the instance.
func (p *Provider) DeleteInstance(ctx context.Context) error {
	return p.cmd.Run(ctx, shell.RunOptions{}, "gcloud", "compute", "instances", "delete",
		p.name,
		"--project", p.cfg.Provider.GCP.Project,
		"--zone", p.cfg.Provider.GCP.Zone,
		"--quiet",
	)
}

// InstanceID returns the full GCP resource name of the instance.
func (p *Provider) InstanceID() string {
	gcp := p.cfg.Provider.GCP
	return fmt.Sprintf("projects/%s/zones/%s/instances/%s", gcp.Project, gcp.Zone, p.name)
}

func formatLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "was not found") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "does not exist")
}

func isSSHStartupError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection attempt failed") ||
		strings.Contains(msg, "failed to respond") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "actively refused") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "operation timed out") ||
		strings.Contains(msg, "permission denied (publickey)") ||
		strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "eof")
}

func retrySSHReady(ctx context.Context, operation string, fn func() error) error {
	deadline := time.Now().Add(sshReadyTimeout)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if !isSSHStartupError(err) || time.Now().After(deadline) {
			return err
		}

		shell.Debugf("gcp: waiting for SSH to become ready for %s: %v", operation, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sshReadyRetryPeriod):
		}
	}
}

func joinShellArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
