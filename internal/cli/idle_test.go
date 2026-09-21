package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mutapod/mutapod/internal/config"
)

func TestIdleHeartbeatArgsIncludesMinimumLease(t *testing.T) {
	oldCfgFile := cfgFile
	oldProviderOverride := providerOverride
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		providerOverride = oldProviderOverride
	})
	cfgFile = ""
	providerOverride = ""

	cfg := &config.Config{Name: "demo", Dir: t.TempDir()}
	args := idleHeartbeatArgs(cfg, leaseOptions{MinimumExpiry: headlessMinimumLease})
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "idle-heartbeat") {
		t.Fatalf("expected idle-heartbeat command, got %v", args)
	}
	if !strings.Contains(joined, "--min-lease-minutes=60") {
		t.Fatalf("expected minimum lease flag, got %v", args)
	}
}

func TestIdleHeartbeatRecoversAfterTransientStatusFailures(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	statuses := []struct {
		status string
		err    error
	}{
		{err: errors.New("mutagen daemon unavailable")},
		{status: "connecting to beta"},
		{status: "watching"},
		{status: "paused"},
	}
	var writes []time.Time
	var diagnostics []string

	worker := idleHeartbeatWorker{
		interval: time.Minute,
		leaseExpiry: func(t time.Time) time.Time {
			return t.Add(5 * time.Minute)
		},
		syncStatus: func(context.Context) (string, error) {
			result := statuses[0]
			statuses = statuses[1:]
			return result.status, result.err
		},
		writeLease: func(_ context.Context, expiresAt time.Time) error {
			writes = append(writes, expiresAt)
			return nil
		},
		now: func() time.Time { return now },
		wait: func(context.Context, time.Duration) bool {
			now = now.Add(time.Minute)
			return true
		},
		diagnosticf: func(format string, args ...any) {
			diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
		},
	}

	worker.run(context.Background())

	if len(writes) != 1 {
		t.Fatalf("got %d lease writes, want 1", len(writes))
	}
	wantExpiry := time.Date(2026, 9, 21, 8, 7, 0, 0, time.UTC)
	if !writes[0].Equal(wantExpiry) {
		t.Fatalf("lease expiry = %s, want %s", writes[0], wantExpiry)
	}
	joined := strings.Join(diagnostics, "\n")
	for _, want := range []string{"sync status unavailable", `sync status is "connecting to beta"`, "sync and lease refresh recovered", "sync session is paused"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, joined)
		}
	}
}

func TestIdleHeartbeatRetriesLeaseWriteWithoutExtendingRecoveryWindow(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	writes := 0

	worker := idleHeartbeatWorker{
		interval: time.Minute,
		leaseExpiry: func(t time.Time) time.Time {
			return t.Add(2 * time.Minute)
		},
		syncStatus: func(context.Context) (string, error) {
			return "watching", nil
		},
		writeLease: func(context.Context, time.Time) error {
			writes++
			return errors.New("ssh unavailable")
		},
		now: func() time.Time { return now },
		wait: func(context.Context, time.Duration) bool {
			now = now.Add(time.Minute)
			return true
		},
		diagnosticf: func(string, ...any) {},
	}

	worker.run(context.Background())

	if writes != 3 {
		t.Fatalf("got %d lease write attempts, want 3 through the original lease expiry", writes)
	}
}

func TestIdleHeartbeatStopsWhenInactiveRecoveryWindowExpires(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	statusChecks := 0

	worker := idleHeartbeatWorker{
		interval:    time.Minute,
		leaseExpiry: func(t time.Time) time.Time { return t.Add(3 * time.Minute) },
		syncStatus: func(context.Context) (string, error) {
			statusChecks++
			return "connecting to beta", nil
		},
		writeLease: func(context.Context, time.Time) error {
			t.Fatal("inactive session must not renew the lease")
			return nil
		},
		now: func() time.Time { return now },
		wait: func(context.Context, time.Duration) bool {
			now = now.Add(time.Minute)
			return true
		},
		diagnosticf: func(string, ...any) {},
	}

	worker.run(context.Background())

	if statusChecks != 4 {
		t.Fatalf("got %d status checks, want 4 through the lease expiry", statusChecks)
	}
}

func TestIdleHeartbeatStopsForTerminalSessionStatus(t *testing.T) {
	for _, status := range []string{"", "paused", "terminated"} {
		t.Run(heartbeatStatusDescription(status), func(t *testing.T) {
			waited := false
			wrote := false
			worker := idleHeartbeatWorker{
				interval:    time.Minute,
				leaseExpiry: func(t time.Time) time.Time { return t.Add(time.Hour) },
				syncStatus:  func(context.Context) (string, error) { return status, nil },
				writeLease: func(context.Context, time.Time) error {
					wrote = true
					return nil
				},
				now: func() time.Time { return time.Now() },
				wait: func(context.Context, time.Duration) bool {
					waited = true
					return true
				},
				diagnosticf: func(string, ...any) {},
			}

			worker.run(context.Background())
			if wrote || waited {
				t.Fatalf("terminal status %q wrote=%v waited=%v, want immediate stop", status, wrote, waited)
			}
		})
	}
}
