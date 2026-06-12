// Package azure implements the Provider interface for Microsoft Azure VMs.
package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	provider.Register("azure", func(cfg *config.Config, cmd shell.Commander) (provider.Provider, error) {
		return New(cfg, cmd), nil
	})
}

// Provider implements provider.Provider for Azure VMs.
type Provider struct {
	cfg    *config.Config
	cmd    shell.Commander
	name   string
	sshCfg *provider.SSHConfig
}

// New creates a new Azure Provider.
func New(cfg *config.Config, cmd shell.Commander) *Provider {
	return &Provider{
		cfg:  cfg,
		cmd:  cmd,
		name: cfg.InstanceName(),
	}
}

func (p *Provider) Name() string { return "azure" }

func (p *Provider) PreferredSyncBackend() provider.SyncBackend {
	return provider.SyncMutagen
}

func (p *Provider) ForwardedWorkspacePath() string {
	return p.cfg.WorkspacePath()
}

// State returns the current power state of the Azure VM.
func (p *Provider) State(ctx context.Context) (provider.InstanceState, error) {
	az := p.cfg.Provider.Azure
	args := p.withSubscription([]string{
		"vm", "show",
		"--resource-group", az.ResourceGroup,
		"--name", p.name,
		"--show-details",
		"--query", "powerState",
		"--output", "tsv",
	})
	out, err := p.cmd.Output(ctx, shell.RunOptions{}, "az", args...)
	if err != nil {
		if isNotFound(err) {
			return provider.StateNotFound, nil
		}
		return provider.StateUnknown, fmt.Errorf("azure: show vm: %w", err)
	}
	return azurePowerStateToState(strings.TrimSpace(string(out))), nil
}

func azurePowerStateToState(s string) provider.InstanceState {
	switch strings.ToLower(s) {
	case "vm running":
		return provider.StateRunning
	case "vm stopped", "vm deallocated":
		return provider.StateStopped
	case "vm stopping", "vm deallocating":
		return provider.StateStopping
	case "vm starting":
		return provider.StateStarting
	case "":
		return provider.StateNotFound
	default:
		return provider.StateUnknown
	}
}

// EnsureInstance creates the VM if absent, or starts it if stopped.
func (p *Provider) EnsureInstance(ctx context.Context) (provider.InstanceState, error) {
	state, err := p.State(ctx)
	if err != nil {
		return provider.StateUnknown, err
	}

	switch state {
	case provider.StateRunning:
		shell.Debugf("azure: VM %s is already running", p.name)
		if err := p.ensureSSHNSGRule(ctx); err != nil {
			return provider.StateUnknown, err
		}
		return provider.StateRunning, nil

	case provider.StateNotFound:
		shell.Debugf("azure: creating VM %s", p.name)
		if err := p.createVM(ctx); err != nil {
			return provider.StateUnknown, err
		}

	case provider.StateStopped:
		shell.Debugf("azure: starting stopped VM %s", p.name)
		if err := p.startVM(ctx); err != nil {
			return provider.StateUnknown, err
		}

	case provider.StateStarting, provider.StateStopping:
		shell.Debugf("azure: VM %s is transitioning (%s), waiting...", p.name, state)

	default:
		return provider.StateUnknown, fmt.Errorf("azure: VM in unexpected state %q", state)
	}

	runningState, err := p.waitForRunning(ctx)
	if err != nil {
		return runningState, err
	}
	if err := p.ensureSSHNSGRule(ctx); err != nil {
		return provider.StateUnknown, err
	}
	return runningState, nil
}

func (p *Provider) createVM(ctx context.Context) error {
	az := p.cfg.Provider.Azure
	fingerprint, err := p.cfg.VMConfigFingerprint()
	if err != nil {
		return err
	}
	identityFile, err := p.identityFile()
	if err != nil {
		return err
	}
	publicKeyArg, err := p.publicKeyArg(identityFile)
	if err != nil {
		return err
	}

	args := []string{
		"vm", "create",
		"--resource-group", az.ResourceGroup,
		"--name", p.name,
		"--size", az.VMSize,
		"--image", az.Image,
		"--admin-username", az.AdminUsername,
		"--authentication-type", "ssh",
		"--os-disk-size-gb", fmt.Sprintf("%d", az.DiskSizeGB),
		"--os-disk-delete-option", "Delete",
		"--nic-delete-option", "Delete",
		"--storage-sku", az.StorageSKU,
	}
	if az.PublicIP {
		args = append(args,
			"--public-ip-sku", az.PublicIPSku,
			"--nsg-rule", "SSH",
		)
	} else {
		args = append(args,
			"--public-ip-address", "",
			"--nsg-rule", "NONE",
		)
	}
	if az.Location != "" {
		args = append(args, "--location", az.Location)
	}
	if publicKeyArg != "" {
		args = append(args, "--ssh-key-values", publicKeyArg)
	} else {
		args = append(args, "--generate-ssh-keys")
	}
	if az.VNet != "" {
		args = append(args, "--vnet-name", az.VNet)
	}
	if az.Subnet != "" {
		args = append(args, "--subnet", az.Subnet)
	}
	if az.Identity != "" {
		args = append(args, "--assign-identity", az.Identity)
	}
	tags := withConfigFingerprint(az.Tags, fingerprint)
	if len(tags) > 0 {
		args = append(args, "--tags")
		args = append(args, formatTags(tags)...)
	}
	args = p.withSubscription(args)
	args = append(args, "--output", "json")

	return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
}

func (p *Provider) startVM(ctx context.Context) error {
	az := p.cfg.Provider.Azure
	args := p.withSubscription([]string{
		"vm", "start",
		"--resource-group", az.ResourceGroup,
		"--name", p.name,
	})
	return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
}

func (p *Provider) ensureSSHNSGRule(ctx context.Context) error {
	az := p.cfg.Provider.Azure
	args := p.withSubscription([]string{
		"network", "nsg", "rule", "show",
		"--resource-group", az.ResourceGroup,
		"--nsg-name", p.nsgName(),
		"--name", "mutapod-ssh",
		"--output", "none",
	})
	err := p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
	ruleExists := err == nil
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("azure: inspect SSH NSG rule: %w", err)
	}

	if len(az.SSHSources) == 0 {
		if !ruleExists {
			return nil
		}
		args = p.withSubscription([]string{
			"network", "nsg", "rule", "delete",
			"--resource-group", az.ResourceGroup,
			"--nsg-name", p.nsgName(),
			"--name", "mutapod-ssh",
		})
		return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
	}

	action := "create"
	if ruleExists {
		action = "update"
	}
	args = []string{
		"network", "nsg", "rule", action,
		"--resource-group", az.ResourceGroup,
		"--nsg-name", p.nsgName(),
		"--name", "mutapod-ssh",
		"--priority", "1000",
		"--direction", "Inbound",
		"--access", "Allow",
		"--protocol", "Tcp",
		"--source-address-prefixes",
	}
	args = append(args, az.SSHSources...)
	args = append(args,
		"--source-port-ranges", "*",
		"--destination-address-prefixes", "*",
		"--destination-port-ranges", "22",
		"--description", "Allow mutapod SSH from configured private sources",
	)
	args = p.withSubscription(args)
	return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
}

// waitForRunning polls until the VM is running, with a 10-minute timeout.
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
			return state, fmt.Errorf("azure: timed out waiting for VM %s to reach running (current: %s)", p.name, state)
		}
		shell.Debugf("azure: waiting for VM %s to be running (current: %s)", p.name, state)
		select {
		case <-ctx.Done():
			return provider.StateUnknown, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// SSHConfig writes an OpenSSH config alias for Mutagen and returns connection params.
func (p *Provider) SSHConfig(ctx context.Context) (*provider.SSHConfig, error) {
	az := p.cfg.Provider.Azure
	identityFile, err := p.identityFile()
	if err != nil {
		return nil, err
	}
	ip, err := p.sshIP(ctx)
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("azure: home dir: %w", err)
	}
	host := p.sshHostAlias()
	knownHostsFile := filepath.Join(home, ".ssh", "known_hosts")
	sshConfigPath := filepath.Join(home, ".ssh", "config")
	if err := ensureSSHConfigEntry(sshConfigPath, sshConfigEntry{
		Alias:          host,
		HostName:       ip,
		User:           az.AdminUsername,
		Port:           22,
		IdentityFile:   identityFile,
		KnownHostsFile: knownHostsFile,
		HostKeyAlias:   host,
	}); err != nil {
		return nil, err
	}

	client := sshrun.New(ip, 22, az.AdminUsername, identityFile)
	if err := retrySSHReady(ctx, "trust host key", func() error {
		return trustHostFunc(client, knownHostsFile, host)
	}); err != nil {
		return nil, fmt.Errorf("azure: trust host key: %w", err)
	}
	shell.Debugf("azure: host key trusted in %s for alias %s", knownHostsFile, host)

	cfg := &provider.SSHConfig{
		Host:         host,
		IP:           ip,
		Port:         22,
		User:         az.AdminUsername,
		IdentityFile: identityFile,
	}
	p.sshCfg = cfg
	return cfg, nil
}

func (p *Provider) sshIP(ctx context.Context) (string, error) {
	az := p.cfg.Provider.Azure
	queries := []string{"publicIps"}
	if !az.PublicIP || az.PreferPrivateIP {
		queries = []string{"privateIps", "publicIps"}
	}

	var lastErr error
	for _, query := range queries {
		args := p.withSubscription([]string{
			"vm", "show",
			"--resource-group", az.ResourceGroup,
			"--name", p.name,
			"--show-details",
			"--query", query,
			"--output", "tsv",
		})
		out, err := p.cmd.Output(ctx, shell.RunOptions{}, "az", args...)
		if err != nil {
			lastErr = err
			continue
		}
		if ip := firstIP(string(out)); ip != "" {
			return ip, nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("azure: get VM IP: %w", lastErr)
	}
	if az.PreferPrivateIP {
		return "", fmt.Errorf("azure: VM %s has no private or public IP address", p.name)
	}
	return "", fmt.Errorf("azure: VM %s has no public IP address; set provider.azure.prefer_private_ip if you connect over a private network", p.name)
}

func firstIP(raw string) string {
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if s := strings.TrimSpace(part); s != "" {
			return s
		}
	}
	return ""
}

// Exec runs a command on the remote VM.
// Non-interactive commands use pure-Go SSH. Interactive TTY sessions delegate
// to Azure CLI/OpenSSH so the user's terminal behaves normally.
func (p *Provider) Exec(ctx context.Context, cmd []string, opts provider.ExecOptions) error {
	if opts.Tty {
		az := p.cfg.Provider.Azure
		identityFile, err := p.identityFile()
		if err != nil {
			return err
		}
		args := []string{
			"ssh", "vm",
			"--resource-group", az.ResourceGroup,
			"--name", p.name,
			"--local-user", az.AdminUsername,
			"--private-key-file", identityFile,
		}
		args = p.withSubscription(args)
		if len(cmd) > 0 {
			args = append(args, "--")
			args = append(args, cmd...)
		}
		return p.cmd.Run(ctx, shell.RunOptions{
			Stdin:  opts.Stdin,
			Stdout: opts.Stdout,
			Stderr: opts.Stderr,
		}, "az", args...)
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

// CopyFile copies a local file to the remote VM via pure-Go SSH.
func (p *Provider) CopyFile(ctx context.Context, localPath, remotePath string) error {
	client, err := p.sshClient()
	if err != nil {
		return err
	}
	return client.Upload(ctx, localPath, remotePath)
}

func (p *Provider) sshClient() (*sshrun.Client, error) {
	if p.sshCfg == nil {
		return nil, fmt.Errorf("azure: SSHConfig must be called before Exec/CopyFile")
	}
	return sshrun.New(p.sshCfg.IP, p.sshCfg.Port, p.sshCfg.User, p.sshCfg.IdentityFile), nil
}

// StopInstance deallocates the VM.
func (p *Provider) StopInstance(ctx context.Context) error {
	state, err := p.State(ctx)
	if err != nil {
		return err
	}
	if state == provider.StateStopped || state == provider.StateNotFound {
		return nil
	}
	az := p.cfg.Provider.Azure
	args := p.withSubscription([]string{
		"vm", "deallocate",
		"--resource-group", az.ResourceGroup,
		"--name", p.name,
	})
	return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
}

// DeleteInstance deletes the VM.
func (p *Provider) DeleteInstance(ctx context.Context) error {
	az := p.cfg.Provider.Azure
	args := p.withSubscription([]string{
		"vm", "delete",
		"--resource-group", az.ResourceGroup,
		"--name", p.name,
		"--yes",
	})
	return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
}

// InstanceMetadata returns the VM resource ID and stored config fingerprint.
func (p *Provider) InstanceMetadata(ctx context.Context) (provider.InstanceMetadata, error) {
	az := p.cfg.Provider.Azure
	args := p.withSubscription([]string{
		"vm", "show",
		"--resource-group", az.ResourceGroup,
		"--name", p.name,
		"--query", "{id:id,tags:tags}",
		"--output", "json",
	})
	out, err := p.cmd.Output(ctx, shell.RunOptions{}, "az", args...)
	if err != nil {
		return provider.InstanceMetadata{}, fmt.Errorf("azure: show VM metadata: %w", err)
	}
	var result struct {
		ID   string            `json:"id"`
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return provider.InstanceMetadata{}, fmt.Errorf("azure: parse VM metadata: %w", err)
	}
	fingerprint := ""
	for key, value := range result.Tags {
		if strings.EqualFold(key, config.VMConfigFingerprintKey) {
			fingerprint = value
			break
		}
	}
	return provider.InstanceMetadata{
		ID:                result.ID,
		ConfigFingerprint: fingerprint,
	}, nil
}

// AdoptInstance stamps the desired config fingerprint onto an existing VM.
func (p *Provider) AdoptInstance(ctx context.Context, fingerprint string) error {
	id, err := p.InstanceID(ctx)
	if err != nil {
		return err
	}
	args := p.withSubscription([]string{
		"tag", "update",
		"--resource-id", id,
		"--operation", "Merge",
		"--tags", config.VMConfigFingerprintKey + "=" + fingerprint,
	})
	return p.cmd.Run(ctx, shell.RunOptions{}, "az", args...)
}

// InstanceID returns the Azure resource ID for the configured target.
func (p *Provider) InstanceID(ctx context.Context) (string, error) {
	az := p.cfg.Provider.Azure
	subscription := strings.TrimSpace(az.Subscription)
	if subscription == "" {
		out, err := p.cmd.Output(ctx, shell.RunOptions{}, "az", "account", "show",
			"--query", "id",
			"--output", "tsv",
		)
		if err != nil {
			return "", fmt.Errorf("azure: resolve active subscription: %w", err)
		}
		subscription = strings.TrimSpace(string(out))
		if subscription == "" {
			return "", fmt.Errorf("azure: active subscription ID is empty")
		}
	}
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s", subscription, az.ResourceGroup, p.name), nil
}

func (p *Provider) withSubscription(args []string) []string {
	if p.cfg.Provider.Azure.Subscription == "" {
		return args
	}
	return append(args, "--subscription", p.cfg.Provider.Azure.Subscription)
}

func (p *Provider) sshHostAlias() string {
	return p.name + ".azure"
}

func (p *Provider) nsgName() string {
	return p.name + "NSG"
}

func (p *Provider) identityFile() (string, error) {
	az := p.cfg.Provider.Azure
	if az.SSHPrivateKeyFile != "" {
		return expandUserPath(az.SSHPrivateKeyFile)
	}
	if az.SSHPublicKeyFile != "" {
		publicKeyFile, err := expandUserPath(az.SSHPublicKeyFile)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(filepath.Ext(publicKeyFile), ".pub") {
			return strings.TrimSuffix(publicKeyFile, filepath.Ext(publicKeyFile)), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("azure: home dir: %w", err)
	}
	return filepath.Join(home, ".ssh", "id_rsa"), nil
}

func (p *Provider) publicKeyArg(identityFile string) (string, error) {
	az := p.cfg.Provider.Azure
	if az.SSHPublicKeyFile != "" {
		publicKeyFile, err := expandUserPath(az.SSHPublicKeyFile)
		if err != nil {
			return "", err
		}
		return "@" + publicKeyFile, nil
	}
	if az.SSHPrivateKeyFile != "" {
		publicKeyFile := identityFile + ".pub"
		if _, err := os.Stat(publicKeyFile); err != nil {
			return "", fmt.Errorf("azure: provider.azure.ssh_public_key_file is required when ssh_private_key_file is set and %q does not exist", publicKeyFile)
		}
		return "@" + publicKeyFile, nil
	}
	publicKeyFile := identityFile + ".pub"
	if _, err := os.Stat(publicKeyFile); err == nil {
		return "@" + publicKeyFile, nil
	}
	return "", nil
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("azure: home dir: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Clean(path), nil
}

type sshConfigEntry struct {
	Alias          string
	HostName       string
	User           string
	Port           int
	IdentityFile   string
	KnownHostsFile string
	HostKeyAlias   string
}

func ensureSSHConfigEntry(configPath string, entry sshConfigEntry) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return fmt.Errorf("azure: create ssh config dir: %w", err)
	}

	var existing string
	if data, err := os.ReadFile(configPath); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("azure: read ssh config: %w", err)
	}

	lines := strings.Split(strings.ReplaceAll(existing, "\r\n", "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	skip := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(strings.ToLower(line), "host ") {
			aliases := strings.Fields(line[5:])
			skip = false
			for _, alias := range aliases {
				if alias == entry.Alias {
					skip = true
					break
				}
			}
		}
		if skip {
			continue
		}
		filtered = append(filtered, raw)
	}
	trimmed := strings.TrimRight(strings.Join(filtered, "\n"), "\n")
	block := sshConfigBlock(entry)
	if trimmed != "" {
		trimmed += "\n\n"
	}
	if err := os.WriteFile(configPath, []byte(trimmed+block), 0600); err != nil {
		return fmt.Errorf("azure: write ssh config: %w", err)
	}
	return nil
}

func sshConfigBlock(entry sshConfigEntry) string {
	return strings.Join([]string{
		"Host " + entry.Alias,
		"    HostName " + entry.HostName,
		"    User " + entry.User,
		fmt.Sprintf("    Port %d", entry.Port),
		"    IdentityFile " + sshConfigPathValue(entry.IdentityFile),
		"    UserKnownHostsFile " + sshConfigPathValue(entry.KnownHostsFile),
		"    HostKeyAlias " + entry.HostKeyAlias,
		"    IdentitiesOnly yes",
		"",
	}, "\n")
}

func sshConfigPathValue(path string) string {
	value := filepath.ToSlash(path)
	if strings.ContainsAny(value, " \t\"") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

func formatTags(tags map[string]string) []string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if tags[key] == "" {
			values = append(values, key)
			continue
		}
		values = append(values, key+"="+tags[key])
	}
	return values
}

func withConfigFingerprint(tags map[string]string, fingerprint string) map[string]string {
	result := make(map[string]string, len(tags)+1)
	for key, value := range tags {
		result[key] = value
	}
	result[config.VMConfigFingerprintKey] = fingerprint
	return result
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "resourcenotfound") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "could not be found")
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

		shell.Debugf("azure: waiting for SSH to become ready for %s: %v", operation, err)
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
