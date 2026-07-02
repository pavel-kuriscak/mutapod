package sync

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
)

func testManager() (*Manager, *shell.FakeCommander) {
	fake := shell.NewFakeCommander()
	cfg := &config.Config{Name: "myapp"}
	sshCfg := &provider.SSHConfig{Host: "example-host", User: "alice"}
	return New(cfg, sshCfg, "mutagen", fake), fake
}

func TestEnsureForward_RecreatesWithForwardTerminate(t *testing.T) {
	manager, fake := testManager()
	fake.Stub("Status: disconnected\n", "mutagen", "forward", "list", "mutapod-myapp-5000")

	if err := manager.EnsureForward(context.Background(), 5000); err != nil {
		t.Fatalf("EnsureForward: %v", err)
	}

	if !fake.CalledWith("mutagen", "forward", "terminate", "mutapod-myapp-5000") {
		t.Fatal("expected forward terminate to be used for stale forward sessions")
	}
	if fake.CalledWith("mutagen", "sync", "terminate", "mutapod-myapp-5000") {
		t.Fatal("did not expect sync terminate for a forward session")
	}
}

func TestEnsureForwardToContainer_CreatesDockerEndpoint(t *testing.T) {
	manager, fake := testManager()
	manager.ForwardToContainer("ssh://alice@example-host", "myapp-web-1")

	if err := manager.EnsureForward(context.Background(), 5000); err != nil {
		t.Fatalf("EnsureForward: %v", err)
	}

	if !fake.CalledWith("mutagen", "forward", "terminate", "mutapod-myapp-5000") {
		t.Fatal("expected legacy VM forward session to be terminated")
	}
	if !fake.CalledWith("mutagen",
		"forward", "create",
		"--name", "mutapod-myapp-container-5000",
		"--label", "mutapod-name=myapp",
		"tcp:localhost:5000",
		"docker://myapp-web-1:tcp:localhost:5000",
	) {
		t.Fatalf("expected container forward create, got %#v", fake.Calls)
	}
	createCall, ok := findCall(fake.Calls, "mutagen",
		"forward", "create",
		"--name", "mutapod-myapp-container-5000",
		"--label", "mutapod-name=myapp",
		"tcp:localhost:5000",
		"docker://myapp-web-1:tcp:localhost:5000",
	)
	if !ok {
		t.Fatal("expected container forward create call")
	}
	if len(createCall.Opts.Env) != 1 || createCall.Opts.Env[0] != "DOCKER_HOST=ssh://alice@example-host" {
		t.Fatalf("DOCKER_HOST env: got %#v", createCall.Opts.Env)
	}
}

func TestEnsureReverseForward_CreatesRemoteListener(t *testing.T) {
	manager, fake := testManager()

	if err := manager.EnsureReverseForward(context.Background(), 8154); err != nil {
		t.Fatalf("EnsureReverseForward: %v", err)
	}

	if !fake.CalledWith("mutagen",
		"forward", "create",
		"--name", "mutapod-myapp-reverse-8154",
		"--label", "mutapod-name=myapp",
		"alice@example-host:tcp::8154",
		"tcp:localhost:8154",
	) {
		t.Fatalf("expected reverse forward create, got %#v", fake.Calls)
	}
}

func findCall(calls []shell.Call, name string, args ...string) (shell.Call, bool) {
	for _, call := range calls {
		if call.Name == name && reflect.DeepEqual(call.Args, args) {
			return call, true
		}
	}
	return shell.Call{}, false
}

func TestTerminateAllSessions_UsesMatchingTerminateCommands(t *testing.T) {
	manager, fake := testManager()

	manager.TerminateAllSessions(context.Background(), []int{5000, 8080}, []int{8154})

	if !fake.CalledWith("mutagen", "sync", "terminate", "mutapod-myapp") {
		t.Fatal("expected sync terminate for the sync session")
	}
	if !fake.CalledWith("mutagen", "forward", "terminate", "mutapod-myapp-5000") {
		t.Fatal("expected forward terminate for port 5000")
	}
	if !fake.CalledWith("mutagen", "forward", "terminate", "mutapod-myapp-8080") {
		t.Fatal("expected forward terminate for port 8080")
	}
	if !fake.CalledWith("mutagen", "forward", "terminate", "mutapod-myapp-reverse-8154") {
		t.Fatal("expected reverse forward terminate for port 8154")
	}
}

func TestVerifySyncReady_FailsOnTransitionProblems(t *testing.T) {
	manager, fake := testManager()
	fake.Stub(`Status: Watching for changes
Transition problems: 1
`, "mutagen", "sync", "list", "mutapod-myapp")

	err := manager.VerifySyncReady(context.Background())
	if err == nil {
		t.Fatal("expected VerifySyncReady to fail")
	}
}

func TestVerifySyncReady_PassesForActiveCleanSession(t *testing.T) {
	manager, fake := testManager()
	fake.Stub(`Status: Watching for changes
Transition problems: 0
`, "mutagen", "sync", "list", "mutapod-myapp")

	if err := manager.VerifySyncReady(context.Background()); err != nil {
		t.Fatalf("VerifySyncReady: %v", err)
	}
}

func TestCreateSync_DisablesGlobalMutagenConfiguration(t *testing.T) {
	manager, fake := testManager()
	cfg := manager.cfg
	localDir := t.TempDir()
	cfg.Dir = localDir
	cfg.Sync.Mode = "two-way-resolved"
	manager.cfg = cfg

	fake.Stub("Status: Watching for changes\n", "mutagen", "sync", "list", "mutapod-myapp")
	if err := manager.createSync(context.Background()); err != nil {
		t.Fatalf("createSync: %v", err)
	}

	if !fake.CalledWith("mutagen",
		"sync", "create",
		"--name", "mutapod-myapp",
		"--label", "mutapod-name=myapp",
		"--no-global-configuration",
		"--sync-mode", "two-way-resolved",
		"--ignore", "mutapod.code-workspace",
		localDir,
		"alice@example-host:/workspace/myapp",
	) {
		t.Fatalf("expected mutagen sync create with --no-global-configuration, got %#v", fake.Calls)
	}
}

func TestParseSyncProgress(t *testing.T) {
	progress := parseSyncProgress([]byte(`Alpha:
        Synchronizable contents:
                10 directories
                90 files (100 MB)
                2 symbolic links
Beta:
        Synchronizable contents:
                4 directories
                30 files (33 MB)
                1 symbolic link
Status: Reconciling changes
`))

	if progress.AlphaDirs != 10 || progress.AlphaFiles != 90 || progress.AlphaLinks != 2 {
		t.Fatalf("unexpected alpha counts: %#v", progress)
	}
	if progress.BetaDirs != 4 || progress.BetaFiles != 30 || progress.BetaLinks != 1 {
		t.Fatalf("unexpected beta counts: %#v", progress)
	}
	if progress.Status != "reconciling" {
		t.Fatalf("unexpected status: %#v", progress)
	}
}

func TestSyncProgressDisplay(t *testing.T) {
	progress := SyncProgress{
		Status:     "reconciling",
		AlphaDirs:  10,
		AlphaFiles: 90,
		AlphaLinks: 2,
		BetaDirs:   4,
		BetaFiles:  30,
		BetaLinks:  1,
	}
	display := progress.Display()
	if !strings.Contains(display, "34%") {
		t.Fatalf("unexpected display: %q", display)
	}
	if !strings.Contains(display, "35/102") {
		t.Fatalf("unexpected display: %q", display)
	}
}
