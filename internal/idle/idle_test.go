package idle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mutapod/mutapod/internal/config"
)

func TestLeasePath_SanitizesWorkspace(t *testing.T) {
	got := LeasePath("My App/Feature")
	if got != "/var/lib/mutapod/leases/my-app-feature.lease" {
		t.Fatalf("LeasePath: got %q", got)
	}
}

func TestLeaseContent(t *testing.T) {
	expires := time.Unix(1700000000, 0)
	got := LeaseContent("testproject", "host-1", expires)
	if !strings.Contains(got, "workspace=testproject\n") {
		t.Fatalf("missing workspace field: %q", got)
	}
	if !strings.Contains(got, "host_id=host-1\n") {
		t.Fatalf("missing host_id field: %q", got)
	}
	if !strings.Contains(got, "expires_unix=1700000000\n") {
		t.Fatalf("missing expires field: %q", got)
	}
}

func TestLeaseExpiry_Defaults(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := &config.Config{}
	got := LeaseExpiry(cfg, now)
	if got.Sub(now) != 30*time.Minute {
		t.Fatalf("LeaseExpiry: got %v", got.Sub(now))
	}
}

func TestIdleCheckScriptAllowsBootGraceBeforeShutdown(t *testing.T) {
	script := string(idleCheckScript)
	grace := strings.Index(script, "boot_grace_seconds=600")
	shutdown := strings.Index(script, "shutdown -h now")
	if grace < 0 {
		t.Fatalf("idle check script missing boot grace:\n%s", script)
	}
	if shutdown < 0 {
		t.Fatalf("idle check script missing shutdown command:\n%s", script)
	}
	if grace > shutdown {
		t.Fatalf("idle check boot grace must be evaluated before shutdown:\n%s", script)
	}
}

func TestIsSSHStartupError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{err: errors.New("dial tcp: connection refused"), want: true},
		{err: errors.New("read tcp: i/o timeout"), want: true},
		{err: errors.New("permission denied (publickey)"), want: true},
		{err: errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"), want: true},
		{err: errors.New("some permanent error"), want: false},
	}

	for _, tt := range tests {
		if got := isSSHStartupError(tt.err); got != tt.want {
			t.Fatalf("isSSHStartupError(%q): got %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestRetrySSHReadyRetriesTransientErrors(t *testing.T) {
	oldTimeout := sshRetryTimeout
	oldInterval := sshRetryInterval
	sshRetryTimeout = 50 * time.Millisecond
	sshRetryInterval = time.Millisecond
	t.Cleanup(func() {
		sshRetryTimeout = oldTimeout
		sshRetryInterval = oldInterval
	})

	attempts := 0
	err := retrySSHReady(context.Background(), "install idle assets", func() error {
		attempts++
		if attempts < 3 {
			return errors.New("dial tcp: connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retrySSHReady: unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("retrySSHReady attempts: got %d, want 3", attempts)
	}
}

func TestRetrySSHReadyStopsOnNonTransientError(t *testing.T) {
	oldTimeout := sshRetryTimeout
	oldInterval := sshRetryInterval
	sshRetryTimeout = 50 * time.Millisecond
	sshRetryInterval = time.Millisecond
	t.Cleanup(func() {
		sshRetryTimeout = oldTimeout
		sshRetryInterval = oldInterval
	})

	attempts := 0
	err := retrySSHReady(context.Background(), "install idle assets", func() error {
		attempts++
		return errors.New("some permanent error")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("retrySSHReady attempts: got %d, want 1", attempts)
	}
}
