package cli

import (
	"strings"
	"testing"

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
