// Package sync manages Mutagen sync and forward sessions.
package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/ignore"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
)

// Manager handles Mutagen sync and forward sessions for a workspace.
type Manager struct {
	cfg               *config.Config
	sshCfg            *provider.SSHConfig
	mutagenPath       string
	cmd               shell.Commander
	sessionName       string
	forwardDockerHost string
	forwardContainer  string
}

// New creates a sync Manager.
func New(cfg *config.Config, sshCfg *provider.SSHConfig, mutagenPath string, cmd shell.Commander) *Manager {
	return &Manager{
		cfg:         cfg,
		sshCfg:      sshCfg,
		mutagenPath: mutagenPath,
		cmd:         cmd,
		sessionName: "mutapod-" + cfg.Name,
	}
}

// SessionName returns the mutagen sync session name.
func (m *Manager) SessionName() string { return m.sessionName }

// SyncStatus returns the current named sync session status token.
func (m *Manager) SyncStatus(ctx context.Context) (string, error) {
	return m.syncStatus(ctx, m.sessionName)
}

// ForwardSessionName returns the mutagen forward session name for a port.
func (m *Manager) ForwardSessionName(port int) string {
	if m.forwardContainer != "" {
		return m.containerForwardSessionName(port)
	}
	return m.vmForwardSessionName(port)
}

func (m *Manager) vmForwardSessionName(port int) string {
	return fmt.Sprintf("mutapod-%s-%d", m.cfg.Name, port)
}

func (m *Manager) containerForwardSessionName(port int) string {
	return fmt.Sprintf("mutapod-%s-container-%d", m.cfg.Name, port)
}

// ForwardToContainer configures future forward sessions to target a container's
// loopback namespace through Docker instead of targeting the remote VM.
func (m *Manager) ForwardToContainer(dockerHost, container string) {
	m.forwardDockerHost = dockerHost
	m.forwardContainer = container
}

// ReverseForwardSessionName returns the mutagen reverse forward session name for a port.
func (m *Manager) ReverseForwardSessionName(port int) string {
	return fmt.Sprintf("mutapod-%s-reverse-%d", m.cfg.Name, port)
}

// EnsureSync creates or resumes the mutagen sync session.
// Returns when the session is actively watching for changes.
func (m *Manager) EnsureSync(ctx context.Context) error {
	shell.Debugf("sync: checking session %s", m.sessionName)

	status, err := m.syncStatus(ctx, m.sessionName)
	if err != nil {
		return err
	}
	shell.Debugf("sync: session status=%q", status)

	switch status {
	case "watching", "scanning", "reconciling", "staging", "transitioning":
		// Already healthy
		shell.Debugf("sync: session already active")
		return nil
	case "paused":
		return m.resumeSync(ctx)
	case "":
		// Not found
		return m.createSync(ctx)
	default:
		// Error or halted state — terminate and recreate
		shell.Debugf("sync: session in state %q, recreating", status)
		_ = m.terminateSyncSession(ctx, m.sessionName)
		return m.createSync(ctx)
	}
}

func (m *Manager) createSync(ctx context.Context) error {
	localPath, err := m.cfg.LocalSyncPath()
	if err != nil {
		return err
	}

	// Load ignore patterns
	patterns, err := ignore.Load(m.cfg.Dir)
	if err != nil {
		return fmt.Errorf("sync: load ignore patterns: %w", err)
	}

	remote := fmt.Sprintf("%s@%s:%s",
		m.sshCfg.User, m.sshCfg.Host, m.cfg.WorkspacePath())

	args := []string{
		"sync", "create",
		"--name", m.sessionName,
		"--label", "mutapod-name=" + m.cfg.Name,
		"--no-global-configuration",
		"--sync-mode", m.cfg.Sync.Mode,
	}
	args = append(args, patterns.MutagenFlags()...)
	args = append(args, localPath, remote)

	if err := m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath, args...); err != nil {
		return fmt.Errorf("sync: create session: %w", err)
	}
	return m.waitForWatching(ctx)
}

// SessionConfigSignature returns a stable digest of the effective mutagen sync
// session settings that require recreation when changed.
func (m *Manager) SessionConfigSignature(ctx context.Context) (string, error) {
	localPath, err := m.cfg.LocalSyncPath()
	if err != nil {
		return "", err
	}
	patterns, err := ignore.Load(m.cfg.Dir)
	if err != nil {
		return "", fmt.Errorf("sync: load ignore patterns: %w", err)
	}
	remote := fmt.Sprintf("%s@%s:%s", m.sshCfg.User, m.sshCfg.Host, m.cfg.WorkspacePath())
	parts := []string{
		"v3",
		"no-global-configuration=true",
		"sync-mode=" + m.cfg.Sync.Mode,
		"local=" + localPath,
		"remote=" + remote,
		"ignore-signature=" + patterns.Signature(),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func (m *Manager) resumeSync(ctx context.Context) error {
	if err := m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"sync", "resume", m.sessionName,
	); err != nil {
		return fmt.Errorf("sync: resume session: %w", err)
	}
	return m.waitForWatching(ctx)
}

// waitForWatching polls until the sync session is actively watching.
func (m *Manager) waitForWatching(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		status, err := m.syncStatus(ctx, m.sessionName)
		if err != nil {
			return err
		}
		if status == "watching" || status == "scanning" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sync: timed out waiting for session to become active (status: %s)", status)
		}
		shell.Debugf("sync: waiting for session (status: %s)", status)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// PauseSync pauses the sync session (preserves history for fast resume).
func (m *Manager) PauseSync(ctx context.Context) error {
	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"sync", "pause", m.sessionName,
	)
}

// FlushSync forces a synchronization cycle and waits for completion.
func (m *Manager) FlushSync(ctx context.Context) error {
	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"sync", "flush", m.sessionName,
	)
}

// FlushSyncWithProgress forces a synchronization cycle, prints coarse progress
// updates to the supplied writer, and waits for completion.
func (m *Manager) FlushSyncWithProgress(ctx context.Context, out io.Writer) error {
	if out == nil {
		return m.FlushSync(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.FlushSync(ctx)
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastLine := ""
	for {
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			progress, err := m.SyncProgress(ctx)
			if err != nil {
				shell.Debugf("sync progress: %v", err)
				continue
			}
			line := progress.Display()
			if line == "" || line == lastLine {
				continue
			}
			fmt.Fprintf(out, "   Sync progress: %s\n", line)
			lastLine = line
		}
	}
}

// VerifySyncReady checks that the sync session is active and free of transition
// problems after a flush, so remote builds don't start from a partial sync.
func (m *Manager) VerifySyncReady(ctx context.Context) error {
	out, err := m.cmd.Output(ctx, shell.RunOptions{}, m.mutagenPath, "sync", "list", m.sessionName)
	if err != nil {
		return fmt.Errorf("sync: inspect session: %w", err)
	}

	status := parseSyncStatus(out)
	if !IsActiveSyncStatus(status) {
		return fmt.Errorf("sync: session is not active after flush (status: %s)", status)
	}

	if problems := parseMutagenCount(out, "Transition problems:"); problems > 0 {
		return fmt.Errorf("sync: session has %d transition problem(s) after flush", problems)
	}

	return nil
}

// SyncProgress returns best-effort coarse transfer progress for the current
// sync session based on `mutagen sync list -l`.
func (m *Manager) SyncProgress(ctx context.Context) (SyncProgress, error) {
	out, err := m.cmd.Output(ctx, shell.RunOptions{}, m.mutagenPath, "sync", "list", "-l", m.sessionName)
	if err != nil {
		return SyncProgress{}, fmt.Errorf("sync: inspect progress: %w", err)
	}
	return parseSyncProgress(out), nil
}

// TerminateSync terminates the sync session (for destroy).
func (m *Manager) TerminateSync(ctx context.Context) error {
	return m.terminateSyncSession(ctx, m.sessionName)
}

// EnsureForward creates or resumes a mutagen forward session for a port.
func (m *Manager) EnsureForward(ctx context.Context, port int) error {
	name := m.ForwardSessionName(port)
	shell.Debugf("forward: checking session %s", name)
	if m.forwardContainer != "" {
		_ = m.terminateForwardSession(ctx, m.vmForwardSessionName(port))
	} else {
		_ = m.terminateForwardSession(ctx, m.containerForwardSessionName(port))
	}

	status, err := m.forwardStatus(ctx, name)
	if err != nil {
		return err
	}

	switch status {
	case "connected":
		shell.Debugf("forward: session %s already active", name)
		return nil
	case "paused":
		return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
			"forward", "resume", name,
		)
	case "":
		return m.createForward(ctx, port, name)
	default:
		_ = m.terminateForwardSession(ctx, name)
		return m.createForward(ctx, port, name)
	}
}

func (m *Manager) createForward(ctx context.Context, port int, name string) error {
	local := fmt.Sprintf("tcp:localhost:%d", port)
	remote := fmt.Sprintf("tcp:localhost:%d", port)
	endpoint := fmt.Sprintf("%s@%s", m.sshCfg.User, m.sshCfg.Host)
	destination := endpoint + ":" + remote
	opts := shell.RunOptions{}
	if m.forwardContainer != "" {
		destination = fmt.Sprintf("docker://%s:tcp:localhost:%d", m.forwardContainer, port)
		opts.Env = []string{"DOCKER_HOST=" + m.forwardDockerHost}
	}

	return m.cmd.Run(ctx, opts, m.mutagenPath,
		"forward", "create",
		"--name", name,
		"--label", "mutapod-name="+m.cfg.Name,
		local,
		destination,
	)
}

// EnsureReverseForward creates or resumes a reverse Mutagen forward session for a port.
func (m *Manager) EnsureReverseForward(ctx context.Context, port int) error {
	name := m.ReverseForwardSessionName(port)
	shell.Debugf("reverse-forward: checking session %s", name)

	status, err := m.forwardStatus(ctx, name)
	if err != nil {
		return err
	}

	switch status {
	case "connected":
		shell.Debugf("reverse-forward: session %s already active", name)
		return nil
	case "paused":
		return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
			"forward", "resume", name,
		)
	case "":
		return m.createReverseForward(ctx, port, name)
	default:
		_ = m.terminateForwardSession(ctx, name)
		return m.createReverseForward(ctx, port, name)
	}
}

func (m *Manager) createReverseForward(ctx context.Context, port int, name string) error {
	local := fmt.Sprintf("tcp:localhost:%d", port)
	remote := fmt.Sprintf("tcp::%d", port)
	endpoint := fmt.Sprintf("%s@%s", m.sshCfg.User, m.sshCfg.Host)

	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"forward", "create",
		"--name", name,
		"--label", "mutapod-name="+m.cfg.Name,
		endpoint+":"+remote,
		local,
	)
}

// PauseForward pauses a forward session.
func (m *Manager) PauseForward(ctx context.Context, port int) error {
	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"forward", "pause", m.ForwardSessionName(port),
	)
}

// PauseAllForwards pauses all forward sessions for this workspace.
func (m *Manager) PauseAllForwards(ctx context.Context, ports []int) {
	for _, p := range ports {
		_ = m.PauseForward(ctx, p)
	}
}

// TerminateForwardVariants terminates all forward session variants that
// mutapod may have created for a port.
func (m *Manager) TerminateForwardVariants(ctx context.Context, port int) {
	_ = m.terminateForwardSession(ctx, m.vmForwardSessionName(port))
	_ = m.terminateForwardSession(ctx, m.containerForwardSessionName(port))
}

// PauseReverseForward pauses a reverse forward session.
func (m *Manager) PauseReverseForward(ctx context.Context, port int) error {
	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"forward", "pause", m.ReverseForwardSessionName(port),
	)
}

// PauseAllReverseForwards pauses all reverse forward sessions for this workspace.
func (m *Manager) PauseAllReverseForwards(ctx context.Context, ports []int) {
	for _, p := range ports {
		_ = m.PauseReverseForward(ctx, p)
	}
}

// TerminateAllSessions terminates sync + all forward sessions (for destroy).
func (m *Manager) TerminateAllSessions(ctx context.Context, forwardPorts, reversePorts []int) {
	_ = m.terminateSyncSession(ctx, m.sessionName)
	for _, p := range forwardPorts {
		_ = m.terminateForwardSession(ctx, m.vmForwardSessionName(p))
		_ = m.terminateForwardSession(ctx, m.containerForwardSessionName(p))
	}
	for _, p := range reversePorts {
		_ = m.terminateForwardSession(ctx, m.ReverseForwardSessionName(p))
	}
}

func (m *Manager) terminateSyncSession(ctx context.Context, name string) error {
	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"sync", "terminate", name,
	)
}

func (m *Manager) terminateForwardSession(ctx context.Context, name string) error {
	return m.cmd.Run(ctx, shell.RunOptions{}, m.mutagenPath,
		"forward", "terminate", name,
	)
}

// syncStatus returns the status of a named sync session by listing it by name.
// Returns ("", nil) when the session does not exist.
func (m *Manager) syncStatus(ctx context.Context, name string) (string, error) {
	out, err := m.cmd.Output(ctx, shell.RunOptions{}, m.mutagenPath, "sync", "list", name)
	if err != nil {
		if isNoSessions(err) {
			return "", nil
		}
		return "", fmt.Errorf("sync: list sessions: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return "", nil
	}
	return parseSyncStatus(out), nil
}

// forwardStatus returns the status of a named forward session.
// Returns ("", nil) when the session does not exist.
func (m *Manager) forwardStatus(ctx context.Context, name string) (string, error) {
	out, err := m.cmd.Output(ctx, shell.RunOptions{}, m.mutagenPath, "forward", "list", name)
	if err != nil {
		if isNoSessions(err) {
			return "", nil
		}
		return "", fmt.Errorf("forward: list sessions: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return "", nil
	}
	return parseForwardStatus(out), nil
}

// parseSyncStatus extracts a normalised status token from mutagen sync list output.
func parseSyncStatus(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "status:") {
			continue
		}
		s := strings.ToLower(strings.TrimSpace(line[7:]))
		switch {
		case strings.Contains(s, "watching"):
			return "watching"
		case strings.Contains(s, "scanning"):
			return "scanning"
		case strings.Contains(s, "reconciling"):
			return "reconciling"
		case strings.Contains(s, "staging"):
			return "staging"
		case strings.Contains(s, "applying") || strings.Contains(s, "transitioning"):
			return "transitioning"
		case strings.Contains(s, "paused"):
			return "paused"
		default:
			return s
		}
	}
	return ""
}

// IsActiveSyncStatus reports whether a sync status means the session is live
// enough to keep a workspace lease refreshed.
func IsActiveSyncStatus(status string) bool {
	switch status {
	case "watching", "scanning", "reconciling", "staging", "transitioning":
		return true
	default:
		return false
	}
}

// parseForwardStatus extracts a normalised status token from mutagen forward list output.
func parseForwardStatus(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "status:") {
			continue
		}
		s := strings.ToLower(strings.TrimSpace(line[7:]))
		switch {
		case strings.Contains(s, "paused"):
			return "paused"
		case s == "connected" || strings.HasPrefix(s, "connected "):
			return "connected"
		default:
			return s
		}
	}
	return ""
}

func isNoSessions(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no sessions found") ||
		strings.Contains(msg, "no matching sessions") ||
		strings.Contains(msg, "unable to locate requested sessions")
}

func parseMutagenCount(data []byte, prefix string) int {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), strings.ToLower(prefix)) {
			continue
		}
		value := strings.TrimSpace(line[len(prefix):])
		count, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return count
	}
	return 0
}

// SyncProgress captures coarse alpha/beta entry counts visible in Mutagen's
// long session listing.
type SyncProgress struct {
	Status     string
	AlphaFiles int
	BetaFiles  int
	AlphaDirs  int
	BetaDirs   int
	AlphaLinks int
	BetaLinks  int
}

func (p SyncProgress) Display() string {
	status := p.Status
	if status == "" {
		status = "syncing"
	}
	total := p.AlphaFiles + p.AlphaDirs + p.AlphaLinks
	done := p.BetaFiles + p.BetaDirs + p.BetaLinks
	if total <= 0 {
		return status
	}
	if done > total {
		done = total
	}
	percent := int(float64(done) * 100 / float64(total))
	return fmt.Sprintf("%d%% (%d/%d entries, status=%s)", percent, done, total, status)
}

func parseSyncProgress(data []byte) SyncProgress {
	lines := strings.Split(string(data), "\n")
	progress := SyncProgress{Status: parseSyncStatus(data)}
	section := ""
	inContents := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch line {
		case "Alpha:":
			section = "alpha"
			inContents = false
			continue
		case "Beta:":
			section = "beta"
			inContents = false
			continue
		case "Synchronizable contents:":
			inContents = true
			continue
		}

		if strings.HasSuffix(line, ":") {
			inContents = false
		}
		if !inContents || section == "" {
			continue
		}

		count, kind := parseSyncCountLine(line)
		switch section {
		case "alpha":
			switch kind {
			case "directory":
				progress.AlphaDirs = count
			case "file":
				progress.AlphaFiles = count
			case "symbolic link":
				progress.AlphaLinks = count
			}
		case "beta":
			switch kind {
			case "directory":
				progress.BetaDirs = count
			case "file":
				progress.BetaFiles = count
			case "symbolic link":
				progress.BetaLinks = count
			}
		}
	}

	return progress
}

func parseSyncCountLine(line string) (int, string) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, ""
	}
	count, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, ""
	}

	rest := strings.Join(fields[1:], " ")
	switch {
	case strings.HasPrefix(rest, "directory"), strings.HasPrefix(rest, "directories"):
		return count, "directory"
	case strings.HasPrefix(rest, "file"), strings.HasPrefix(rest, "files"):
		return count, "file"
	case strings.HasPrefix(rest, "symbolic link"), strings.HasPrefix(rest, "symbolic links"):
		return count, "symbolic link"
	default:
		return 0, ""
	}
}

// DaemonStart ensures the mutagen daemon is running.
func DaemonStart(ctx context.Context, mutagenPath string, cmd shell.Commander) error {
	return cmd.Run(ctx, shell.RunOptions{}, mutagenPath, "daemon", "start")
}
