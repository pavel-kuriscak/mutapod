package cli

import (
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/state"
)

func TestActiveForwardBackendPrefersSavedBackend(t *testing.T) {
	cfg := &config.Config{}
	cfg.Compose.ForwardBackend = "ssh"
	st := &state.State{}
	st.Sync.ForwardBackend = "mutagen"

	if got := activeForwardBackend(cfg, st); got != "mutagen" {
		t.Fatalf("activeForwardBackend: got %q, want mutagen", got)
	}
}

func TestActiveForwardBackendInfersLegacyMutagenSession(t *testing.T) {
	cfg := &config.Config{}
	cfg.Compose.ForwardBackend = "ssh"
	st := &state.State{}
	st.Sync.ForwardSessions = map[string]string{
		"8000": "mutapod-demo-8000",
	}

	if got := activeForwardBackend(cfg, st); got != "mutagen" {
		t.Fatalf("activeForwardBackend: got %q, want mutagen", got)
	}
}

func TestActiveForwardBackendInfersLegacySSHSession(t *testing.T) {
	cfg := &config.Config{}
	cfg.Compose.ForwardBackend = "mutagen"
	st := &state.State{}
	st.Sync.ForwardSessions = map[string]string{
		"8000": "mutapod-demo-ssh-8000",
	}

	if got := activeForwardBackend(cfg, st); got != "ssh" {
		t.Fatalf("activeForwardBackend: got %q, want ssh", got)
	}
}

func TestActiveForwardBackendFallsBackToConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Compose.ForwardBackend = "ssh"
	st := &state.State{}

	if got := activeForwardBackend(cfg, st); got != "ssh" {
		t.Fatalf("activeForwardBackend: got %q, want ssh", got)
	}
}

func TestShouldPrepareAttachedContainerExtensionInstall(t *testing.T) {
	cfg := &config.Config{}
	if !shouldPrepareAttachedContainerExtensionInstall(cfg) {
		t.Fatal("default copy_local_extensions should prepare extension install")
	}

	disabled := false
	cfg.Compose.CopyLocalExtensions = &disabled
	if shouldPrepareAttachedContainerExtensionInstall(cfg) {
		t.Fatal("copy_local_extensions=false with no explicit extensions should not prepare extension install")
	}

	cfg.Compose.Extensions = []string{"ms-python.python"}
	if !shouldPrepareAttachedContainerExtensionInstall(cfg) {
		t.Fatal("explicit extensions should prepare extension install")
	}
}

func TestAttachedContainerExtensionInstallPrepScript(t *testing.T) {
	script := attachedContainerExtensionInstallPrepScript()
	for _, needle := range []string{
		".installExtensionsMarker",
		".vscode-server/extensions",
		"find \"$extensions_dir\" -mindepth 1 -maxdepth 1 -type d",
		"rm -f \"$marker\"",
		"pkill -f '[/]\\.vscode-server/bin/'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q:\n%s", needle, script)
		}
	}
}
