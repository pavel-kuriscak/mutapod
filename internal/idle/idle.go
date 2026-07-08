package idle

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/sshrun"
)

const leaseDir = "/var/lib/mutapod/leases"

var (
	sshRetryTimeout  = 90 * time.Second
	sshRetryInterval = 2 * time.Second
)

//go:embed scripts/mutapod-idle-check.sh
var idleCheckScript []byte

//go:embed scripts/mutapod-idle-check.service
var idleCheckService []byte

//go:embed scripts/mutapod-idle-check.timer
var idleCheckTimer []byte

func InstallRemote(ctx context.Context, p provider.Provider) error {
	files := []struct {
		content []byte
		remote  string
		mode    string
	}{
		{idleCheckScript, "/usr/local/bin/mutapod-idle-check", "0755"},
		{idleCheckService, "/etc/systemd/system/mutapod-idle-check.service", "0644"},
		{idleCheckTimer, "/etc/systemd/system/mutapod-idle-check.timer", "0644"},
	}

	for _, file := range files {
		tmp, err := writeTemp(file.content)
		if err != nil {
			return err
		}
		defer os.Remove(tmp)

		remoteTmp := "/tmp/" + filepath.Base(file.remote)
		if err := retrySSHReady(ctx, "copy "+file.remote, func() error {
			return p.CopyFile(ctx, tmp, remoteTmp)
		}); err != nil {
			return fmt.Errorf("idle: copy %s: %w", file.remote, err)
		}

		cmd := fmt.Sprintf(
			"sudo install -m %s %s %s",
			file.mode,
			shellQuote(remoteTmp),
			shellQuote(file.remote),
		)
		if err := retrySSHReady(ctx, "install "+file.remote, func() error {
			return p.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{})
		}); err != nil {
			return fmt.Errorf("idle: install %s: %w", file.remote, err)
		}
	}

	if err := retrySSHReady(ctx, "create lease dir", func() error {
		return p.Exec(ctx, []string{"bash", "-c", "sudo mkdir -p " + shellQuote(leaseDir)}, provider.ExecOptions{})
	}); err != nil {
		return fmt.Errorf("idle: create lease dir: %w", err)
	}
	if err := retrySSHReady(ctx, "daemon-reload", func() error {
		return p.Exec(ctx, []string{"bash", "-c", "sudo systemctl daemon-reload"}, provider.ExecOptions{})
	}); err != nil {
		return fmt.Errorf("idle: daemon-reload: %w", err)
	}
	return nil
}

func EnableTimer(ctx context.Context, p provider.Provider) error {
	return retrySSHReady(ctx, "enable idle timer", func() error {
		return p.Exec(ctx, []string{"bash", "-c", "sudo systemctl enable --now mutapod-idle-check.timer"}, provider.ExecOptions{})
	})
}

func RemoveLease(ctx context.Context, p provider.Provider, workspace string) error {
	cmd := fmt.Sprintf("sudo rm -f %s", shellQuote(LeasePath(workspace)))
	return retrySSHReady(ctx, "remove lease", func() error {
		return p.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{})
	})
}

func TriggerCheckNow(ctx context.Context, p provider.Provider) error {
	return retrySSHReady(ctx, "trigger idle check", func() error {
		return p.Exec(ctx, []string{"bash", "-c", "sudo /usr/local/bin/mutapod-idle-check"}, provider.ExecOptions{})
	})
}

type LeaseInfo struct {
	Workspace   string `json:"workspace"`
	HostID      string `json:"host_id"`
	ExpiresUnix int64  `json:"expires_unix"`
}

func ListLeases(ctx context.Context, p provider.Provider) ([]LeaseInfo, error) {
	cmd := `python3 - <<'PY'
import json
import os
lease_dir = "/var/lib/mutapod/leases"
items = []
if os.path.isdir(lease_dir):
    for name in sorted(os.listdir(lease_dir)):
        if not name.endswith(".lease"):
            continue
        path = os.path.join(lease_dir, name)
        record = {"workspace": "", "host_id": "", "expires_unix": 0}
        with open(path, "r", encoding="utf-8") as f:
            for raw in f:
                line = raw.strip()
                if "=" not in line:
                    continue
                key, value = line.split("=", 1)
                if key == "workspace":
                    record["workspace"] = value
                elif key == "host_id":
                    record["host_id"] = value
                elif key == "expires_unix":
                    try:
                        record["expires_unix"] = int(value)
                    except ValueError:
                        record["expires_unix"] = 0
        items.append(record)
print(json.dumps(items))
PY`

	var out bytes.Buffer
	if err := p.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{
		Stdout: &out,
		Stderr: io.Discard,
	}); err != nil {
		return nil, fmt.Errorf("idle: list leases: %w", err)
	}

	var leases []LeaseInfo
	if err := json.Unmarshal(out.Bytes(), &leases); err != nil {
		return nil, fmt.Errorf("idle: parse leases: %w", err)
	}
	return leases, nil
}

func LeasePath(workspace string) string {
	return leaseDir + "/" + sanitizeToken(workspace) + ".lease"
}

func LeaseContent(workspace, hostID string, expiresAt time.Time) string {
	return fmt.Sprintf(
		"workspace=%s\nhost_id=%s\nexpires_unix=%d\n",
		workspace,
		hostID,
		expiresAt.Unix(),
	)
}

func WriteLeaseWithClient(ctx context.Context, client *sshrun.Client, workspace, hostID string, expiresAt time.Time) error {
	cmd := fmt.Sprintf("sudo tee %s >/dev/null", shellQuote(LeasePath(workspace)))
	content := LeaseContent(workspace, hostID, expiresAt)
	return client.Run(ctx, cmd, strings.NewReader(content), io.Discard, io.Discard)
}

func WriteLeaseWithRetry(ctx context.Context, client *sshrun.Client, workspace, hostID string, expiresAt time.Time) error {
	return retrySSHReady(ctx, "write lease", func() error {
		return WriteLeaseWithClient(ctx, client, workspace, hostID, expiresAt)
	})
}

func HeartbeatLock(workspace string) (*flock.Flock, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("idle: home dir: %w", err)
	}
	dir := filepath.Join(home, ".mutapod", "heartbeat")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("idle: mkdir heartbeat dir: %w", err)
	}
	return flock.New(filepath.Join(dir, sanitizeToken(workspace)+".lock")), nil
}

func writeTemp(content []byte) (string, error) {
	f, err := os.CreateTemp("", "mutapod-idle-*")
	if err != nil {
		return "", fmt.Errorf("idle: create temp file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, bytes.NewReader(content)); err != nil {
		return "", fmt.Errorf("idle: write temp file: %w", err)
	}
	return f.Name(), nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func sanitizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "workspace"
	}
	re := regexp.MustCompile(`[^a-z0-9._-]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "workspace"
	}
	return s
}

func HeartbeatInterval(cfg *config.Config) time.Duration {
	seconds := cfg.Idle.CheckIntervalSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func LeaseExpiry(cfg *config.Config, now time.Time) time.Time {
	minutes := cfg.Idle.TimeoutMinutes
	if minutes <= 0 {
		minutes = 30
	}
	return now.Add(time.Duration(minutes) * time.Minute)
}

func LeaseExpiryWithMinimum(cfg *config.Config, now time.Time, minimum time.Duration) time.Time {
	expiresAt := LeaseExpiry(cfg, now)
	if minimum <= 0 {
		return expiresAt
	}
	minimumExpiry := now.Add(minimum)
	if expiresAt.Before(minimumExpiry) {
		return minimumExpiry
	}
	return expiresAt
}

func retrySSHReady(ctx context.Context, operation string, fn func() error) error {
	deadline := time.Now().Add(sshRetryTimeout)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if !isSSHStartupError(err) || time.Now().After(deadline) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sshRetryInterval):
		}
	}
}

func isSSHStartupError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
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
