package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/idle"
)

func TestConfirmDestroy(t *testing.T) {
	ok, err := confirmDestroy(strings.NewReader("yes\n"), &strings.Builder{}, "mutapod-demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected confirmation to succeed")
	}
}

func TestConfirmDestroyRejectsWrongValue(t *testing.T) {
	ok, err := confirmDestroy(strings.NewReader("no\n"), &strings.Builder{}, "mutapod-demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected confirmation to fail")
	}
}

func TestOtherWorkspaceLeases(t *testing.T) {
	now := time.Now().Unix()
	leases := []idle.LeaseInfo{
		{Workspace: "current", HostID: "a", ExpiresUnix: now},
		{Workspace: "other-a", HostID: "b", ExpiresUnix: now},
		{Workspace: "", HostID: "c", ExpiresUnix: now},
		{Workspace: "other-b", HostID: "d", ExpiresUnix: now},
	}

	other := otherWorkspaceLeases("current", leases)
	if len(other) != 2 {
		t.Fatalf("got %d other leases, want 2", len(other))
	}
	if other[0].Workspace != "other-a" || other[1].Workspace != "other-b" {
		t.Fatalf("unexpected filtered leases: %#v", other)
	}
}

func TestConfirmMissingIgnoreFile_AllowsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mutapodignore"), []byte("node_modules\n"), 0644); err != nil {
		t.Fatalf("write .mutapodignore: %v", err)
	}
	cfg := &config.Config{Name: "demo", Dir: dir}

	if err := confirmMissingIgnoreFile(strings.NewReader(""), &strings.Builder{}, cfg); err != nil {
		t.Fatalf("confirmMissingIgnoreFile: %v", err)
	}
}

func TestConfirmMissingIgnoreFile_RejectsByDefault(t *testing.T) {
	cfg := &config.Config{Name: "demo", Dir: t.TempDir()}
	var out strings.Builder

	err := confirmMissingIgnoreFile(strings.NewReader("\n"), &out, cfg)
	if err == nil {
		t.Fatal("expected confirmation failure")
	}
	if !strings.Contains(out.String(), "Continue without .mutapodignore? [y/N]: ") {
		t.Fatalf("unexpected prompt: %q", out.String())
	}
}

func TestConfirmMissingIgnoreFile_AcceptsYes(t *testing.T) {
	cfg := &config.Config{Name: "demo", Dir: t.TempDir()}

	if err := confirmMissingIgnoreFile(strings.NewReader("yes\n"), &strings.Builder{}, cfg); err != nil {
		t.Fatalf("confirmMissingIgnoreFile: %v", err)
	}
}
