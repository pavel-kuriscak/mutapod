package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/agents"
	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/vscode"
)

func testStartupConfig(dir string) *config.Config {
	return &config.Config{
		Name: "demo",
		Dir:  dir,
		Provider: config.ProviderConfig{
			Type: "gcp",
		},
		Sync: config.SyncConfig{
			Mode: "two-way-resolved",
		},
	}
}

func TestEnsureAgentsForStartupCreatesOnInteractiveDefaultYes(t *testing.T) {
	dir := t.TempDir()
	cfg := testStartupConfig(dir)
	var out strings.Builder

	path, ensured, err := ensureAgentsForStartup(strings.NewReader("\n"), &out, true, cfg)
	if err != nil {
		t.Fatalf("ensureAgentsForStartup: %v", err)
	}
	if !ensured {
		t.Fatal("expected AGENTS.md to be ensured")
	}
	if path != filepath.Join(dir, "AGENTS.md") {
		t.Fatalf("path: got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.HasPrefix(string(data), "<!-- mutapod:begin -->") {
		t.Fatalf("expected managed block at top:\n%s", string(data))
	}
}

func TestEnsureAgentsForStartupSkipsWhenInteractiveNo(t *testing.T) {
	dir := t.TempDir()
	cfg := testStartupConfig(dir)
	var out strings.Builder

	_, ensured, err := ensureAgentsForStartup(strings.NewReader("no\n"), &out, true, cfg)
	if err != nil {
		t.Fatalf("ensureAgentsForStartup: %v", err)
	}
	if ensured {
		t.Fatal("expected AGENTS.md to be skipped")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("expected AGENTS.md not to be created, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "Skipped the mutapod-managed AGENTS.md block.") {
		t.Fatalf("expected skip message, got %q", out.String())
	}
}

func TestEnsureAgentsForStartupNoninteractiveAutoAddsMissingBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := testStartupConfig(dir)
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	var out strings.Builder

	_, ensured, err := ensureAgentsForStartup(strings.NewReader(""), &out, false, cfg)
	if err != nil {
		t.Fatalf("ensureAgentsForStartup: %v", err)
	}
	if !ensured {
		t.Fatal("expected AGENTS.md to be ensured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "<!-- mutapod:begin -->") {
		t.Fatalf("expected managed block at top:\n%s", text)
	}
	if !strings.Contains(text, "# Existing") {
		t.Fatalf("expected existing content to be preserved:\n%s", text)
	}
}

func TestEnsureAgentsForStartupUpdatesExistingManagedBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := testStartupConfig(dir)
	path := filepath.Join(dir, "AGENTS.md")
	original := "# Existing\n\n" + "<!-- mutapod:begin -->\nold\n<!-- mutapod:end -->\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	_, ensured, err := ensureAgentsForStartup(strings.NewReader("no\n"), &strings.Builder{}, true, cfg)
	if err != nil {
		t.Fatalf("ensureAgentsForStartup: %v", err)
	}
	if !ensured {
		t.Fatal("expected existing managed block to be ensured without prompting")
	}
	status, err := agents.Inspect(cfg)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !status.HasManagedBlock {
		t.Fatalf("expected managed block status: %#v", status)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "<!-- mutapod:begin -->") {
		t.Fatalf("expected managed block moved to top:\n%s", text)
	}
	if strings.Contains(text, "\nold\n") {
		t.Fatalf("expected old managed block replaced:\n%s", text)
	}
}

func TestParseUpLaunchModeHeadless(t *testing.T) {
	mode, err := parseUpLaunchMode([]string{"headless"})
	if err != nil {
		t.Fatalf("parseUpLaunchMode: %v", err)
	}
	if mode != vscode.LaunchHeadless {
		t.Fatalf("mode: got %q, want %q", mode, vscode.LaunchHeadless)
	}
}

func TestLeaseOptionsForLaunchModeHeadless(t *testing.T) {
	got := leaseOptionsForLaunchMode(vscode.LaunchHeadless)
	if got.MinimumExpiry != headlessMinimumLease {
		t.Fatalf("MinimumExpiry: got %v, want %v", got.MinimumExpiry, headlessMinimumLease)
	}
	if normal := leaseOptionsForLaunchMode(vscode.LaunchAttached); normal.MinimumExpiry != 0 {
		t.Fatalf("normal mode should not force minimum lease, got %v", normal.MinimumExpiry)
	}
}
