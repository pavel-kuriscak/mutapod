package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsSSHStartupError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{err: errors.New("dial tcp 34.6.187.9:22: connectex: No connection could be made because the target machine actively refused it."), want: true},
		{err: errors.New("dial tcp 10.150.170.36:22: connectex: A connection attempt failed because the connected party did not properly respond after a period of time"), want: true},
		{err: errors.New("dial tcp: connection refused"), want: true},
		{err: errors.New("read tcp: i/o timeout"), want: true},
		{err: errors.New("permission denied (publickey)"), want: true},
		{err: errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"), want: true},
		{err: errors.New("some other error"), want: false},
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
	err := retrySSHReady(context.Background(), "copy bootstrap script", func() error {
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
	err := retrySSHReady(context.Background(), "copy bootstrap script", func() error {
		attempts++
		return errors.New("some other error")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("retrySSHReady attempts: got %d, want 1", attempts)
	}
}
