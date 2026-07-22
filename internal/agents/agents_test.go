package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
)

func testConfig(dir string) *config.Config {
	return &config.Config{
		Name: "testproject",
		Dir:  dir,
		Provider: config.ProviderConfig{
			Type: "gcp",
		},
		Sync: config.SyncConfig{
			Mode: "two-way-resolved",
		},
		Compose: config.ComposeConfig{
			File:            "compose-dev.yaml",
			PrimaryService:  "web",
			WorkspaceFolder: "/app",
		},
	}
}

func TestEnsureCreatesAgentsFile(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)

	path, err := Ensure(cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if path != filepath.Join(dir, filename) {
		t.Fatalf("path: got %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, beginMarker) {
		t.Fatalf("expected managed block at top: %s", text)
	}
	if !strings.Contains(text, "mutapod keeps this repository's source on the local host") {
		t.Fatalf("missing mutapod guidance: %s", text)
	}
	if !strings.Contains(text, "Do not edit it by hand") {
		t.Fatalf("missing managed-block warning: %s", text)
	}
	if !strings.Contains(text, "`mutapod ssh -- <command>` runs a non-interactive command on the VM") {
		t.Fatalf("missing VM command guidance: %s", text)
	}
	if !strings.Contains(text, "`mutapod exec -- <command>` is container-level") {
		t.Fatalf("missing container exec guidance: %s", text)
	}
	if !strings.Contains(text, "streams stdout/stderr, and uses a non-login `sh -c` shell") {
		t.Fatalf("missing exec shell/output caveat: %s", text)
	}
	if !strings.Contains(text, "over provider-specific SSH commands such as `gcloud compute ssh` or `az ssh vm`") {
		t.Fatalf("missing provider-specific SSH guardrail: %s", text)
	}
	if !strings.Contains(text, "Docker Compose interpolates `$...` in `.env` values") {
		t.Fatalf("missing env interpolation caveat: %s", text)
	}
	if !strings.Contains(text, "File mirroring is asynchronous during active work") {
		t.Fatalf("missing file mirroring delay caveat: %s", text)
	}
	if !strings.Contains(text, "verify the local path, remote workspace path, and in-container workspace path") {
		t.Fatalf("missing file mirroring verification guidance: %s", text)
	}
	if !strings.Contains(text, "do not run `mutapod up`, `mutapod down`, `mutapod reset`, or `mutapod destroy`") {
		t.Fatalf("missing remote environment guardrail: %s", text)
	}
	if !strings.Contains(text, "`mutapod up --build`") {
		t.Fatalf("missing build guidance: %s", text)
	}
}

func TestEnsureAmendsManagedBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	path := filepath.Join(dir, filename)
	original := "# Existing\n\nKeep this.\n\n" + beginMarker + "\nold\n" + endMarker + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, err := Ensure(cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "Keep this.") {
		t.Fatal("expected existing content to be preserved")
	}
	if !strings.HasPrefix(text, beginMarker) {
		t.Fatalf("expected managed block to move to top:\n%s", text)
	}
	if strings.Contains(text, "\nold\n") {
		t.Fatal("expected old managed block to be replaced")
	}
	if !strings.Contains(text, "Primary service for attached-container workflows: `web`") {
		t.Fatal("expected regenerated managed block")
	}
}

func TestEnsurePreservesExistingCRLFLineEndings(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	path := filepath.Join(dir, filename)
	original := strings.Join([]string{
		"# Existing",
		"",
		"Keep this.",
		"",
		beginMarker,
		"old",
		endMarker,
		"",
	}, "\r\n")
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, err := Ensure(cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !bytes.Contains(data, []byte("\r\n")) {
		t.Fatalf("expected CRLF line endings:\n%s", string(data))
	}
	if bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("expected no lone LF line endings:\n%q", data)
	}
	text := string(data)
	if !strings.Contains(text, "# Existing") || !strings.Contains(text, "Keep this.") {
		t.Fatalf("expected existing content to be preserved:\n%s", text)
	}
	if strings.Contains(text, "\nold\r\n") || strings.Contains(text, "\r\nold\r\n") {
		t.Fatalf("expected old managed block to be replaced:\n%s", text)
	}
}

func TestEnsurePrependsManagedBlockWhenMissingMarkers(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, err := Ensure(cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "# Existing") {
		t.Fatal("expected existing content to remain")
	}
	if !strings.HasPrefix(text, beginMarker) {
		t.Fatalf("expected managed block at top:\n%s", text)
	}
	if !strings.Contains(text, beginMarker) || !strings.Contains(text, endMarker) {
		t.Fatal("expected managed block markers to be present")
	}
}

func TestEnsureReplacesFuzzyTopMutapodBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	path := filepath.Join(dir, filename)
	original := strings.Join([]string{
		"<!-- mutapod begin -->",
		"## Mutapod",
		"",
		"This managed block is managed by mutapod, but the markers were edited.",
		"",
		"Project setup:",
		"- Workspace name: `old`",
		"- Remote workspace path: `/workspace/old`",
		"",
		"<!-- mutapod end -->",
		"",
		"# Existing",
		"",
		"Keep this.",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, err := Ensure(cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if strings.Count(text, beginMarker) != 1 {
		t.Fatalf("expected exactly one managed begin marker:\n%s", text)
	}
	if strings.Contains(text, "mutapod begin") || strings.Contains(text, "mutapod end") || strings.Contains(text, "Workspace name: `old`") {
		t.Fatalf("expected damaged block to be replaced:\n%s", text)
	}
	if !strings.Contains(text, "# Existing") || !strings.Contains(text, "Keep this.") {
		t.Fatalf("expected existing content to be preserved:\n%s", text)
	}
}

func TestEnsureDoesNotFuzzyReplaceNonTopMutapodSection(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	path := filepath.Join(dir, filename)
	original := "# Existing\n\n## Mutapod\n\nHuman note mentioning mutapod up.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, err := Ensure(cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, beginMarker) {
		t.Fatalf("expected new managed block at top:\n%s", text)
	}
	if !strings.Contains(text, "# Existing") || !strings.Contains(text, "Human note mentioning mutapod up.") {
		t.Fatalf("expected existing non-top Mutapod note to be preserved:\n%s", text)
	}
}

func TestInspectReportsManagedBlockState(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)

	status, err := Inspect(cfg)
	if err != nil {
		t.Fatalf("Inspect missing file: %v", err)
	}
	if status.Exists || status.HasManagedBlock {
		t.Fatalf("unexpected missing-file status: %#v", status)
	}

	if _, err := Ensure(cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	status, err = Inspect(cfg)
	if err != nil {
		t.Fatalf("Inspect managed file: %v", err)
	}
	if !status.Exists || !status.HasManagedBlock {
		t.Fatalf("unexpected managed-file status: %#v", status)
	}
}

func TestInspectReportsFuzzyTopMutapodBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	path := filepath.Join(dir, filename)
	original := "<!-- mutapod begin -->\n## Mutapod\n\nWorkspace name: `old`\nRemote workspace path: `/workspace/old`\n<!-- mutapod end -->\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	status, err := Inspect(cfg)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !status.Exists || !status.HasManagedBlock {
		t.Fatalf("unexpected fuzzy managed-file status: %#v", status)
	}
}
