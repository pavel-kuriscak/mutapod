// Package bootstrap installs Docker and the devcontainer CLI on a remote VM.
// The bootstrap script is embedded in the binary — no separate file needed.
package bootstrap

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
)

//go:embed scripts/bootstrap.sh
var bootstrapScript []byte

var (
	sshRetryTimeout  = 90 * time.Second
	sshRetryInterval = 2 * time.Second
)

// Run uploads bootstrap.sh to the remote VM and executes it as root.
// It is idempotent — safe to call on every `mutapod up`.
func Run(ctx context.Context, p provider.Provider) error {
	shell.Debugf("bootstrap: uploading script")

	// Write script to a temp file so we can scp it
	tmp, err := writeTempScript()
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	if err := retrySSHReady(ctx, "copy bootstrap script", func() error {
		return p.CopyFile(ctx, tmp, "/tmp/mutapod-bootstrap.sh")
	}); err != nil {
		return fmt.Errorf("bootstrap: copy script: %w", err)
	}

	shell.Debugf("bootstrap: running script on remote VM")
	var out bytes.Buffer
	err = retrySSHReady(ctx, "run bootstrap script", func() error {
		out.Reset()
		return p.Exec(ctx, []string{"sudo", "bash", "/tmp/mutapod-bootstrap.sh"}, provider.ExecOptions{
			Stdout: &out,
			Stderr: &out,
		})
	})
	// Always print bootstrap output so the user can see progress
	if out.Len() > 0 {
		fmt.Print(out.String())
	}
	if err != nil {
		return fmt.Errorf("bootstrap: script failed: %w", err)
	}
	return nil
}

func writeTempScript() (string, error) {
	dir := os.TempDir()
	path := filepath.Join(dir, "mutapod-bootstrap.sh")
	if err := os.WriteFile(path, bootstrapScript, 0600); err != nil {
		return "", fmt.Errorf("bootstrap: write temp script: %w", err)
	}
	return path, nil
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

		shell.Debugf("bootstrap: waiting for SSH to become ready for %s: %v", operation, err)
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
