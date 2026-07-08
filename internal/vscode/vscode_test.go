package vscode

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
)

func TestPrintInstructions_LocalWorkflow(t *testing.T) {
	cfg := &config.Config{
		Name:          "testproject",
		Dir:           `C:\Users\pavel\Desktop\mutapod\testproject`,
		InstanceOwner: "tester",
		Sync:          config.SyncConfig{RemotePath: "/workspace/testproject"},
		Compose: config.ComposeConfig{
			PrimaryService: "web",
		},
	}
	sshCfg := &provider.SSHConfig{Host: "mutapod-testproject.example"}

	output := captureStdout(t, func() {
		PrintInstructions(cfg, sshCfg, []int{5000, 8080}, LaunchAttached)
	})

	if !strings.Contains(output, `code "mutapod.code-workspace"`) {
		t.Fatalf("expected workspace file command, got:\n%s", output)
	}
	if !strings.Contains(output, "mutapod up auto-opens VS Code attached to the main container") {
		t.Fatalf("expected default launch hint, got:\n%s", output)
	}
	if !strings.Contains(output, "mutapod up local") {
		t.Fatalf("expected local launch hint, got:\n%s", output)
	}
	if !strings.Contains(output, "mutapod up headless") {
		t.Fatalf("expected headless launch hint, got:\n%s", output)
	}
	if strings.Contains(output, "vscode-remote://") {
		t.Fatalf("did not expect Remote-SSH folder URI, got:\n%s", output)
	}
	if !strings.Contains(output, "applies Docker overrides only for this project") {
		t.Fatalf("expected project-scoped workspace hint, got:\n%s", output)
	}
	if !strings.Contains(output, "localhost:5000, localhost:8080") {
		t.Fatalf("expected forwarded ports, got:\n%s", output)
	}
	if !strings.Contains(output, "Docker ctx: mutapod-tester-testproject") {
		t.Fatalf("expected docker context hint, got:\n%s", output)
	}
	if !strings.Contains(output, "Dev Containers: Attach to Running Container...") {
		t.Fatalf("expected attach workflow hint, got:\n%s", output)
	}
	if !strings.Contains(output, "docker compose ps web") {
		t.Fatalf("expected primary service terminal hint, got:\n%s", output)
	}
	if !strings.Contains(output, "copied your current local VS Code extensions") {
		t.Fatalf("expected attached profile extension hint, got:\n%s", output)
	}
	if strings.Contains(output, "Reopen in Container") {
		t.Fatalf("did not expect reopen-in-container recommendation, got:\n%s", output)
	}
}

func TestPrintInstructions_Headless(t *testing.T) {
	cfg := &config.Config{
		Name:          "testproject",
		InstanceOwner: "tester",
		Sync:          config.SyncConfig{RemotePath: "/workspace/testproject"},
	}
	sshCfg := &provider.SSHConfig{Host: "mutapod-testproject.example"}

	output := captureStdout(t, func() {
		PrintInstructions(cfg, sshCfg, nil, LaunchHeadless)
	})

	if !strings.Contains(output, "Headless mode is active; VS Code launch was skipped.") {
		t.Fatalf("expected headless hint, got:\n%s", output)
	}
	if strings.Contains(output, "auto-opens VS Code attached") {
		t.Fatalf("headless output should not claim VS Code was opened:\n%s", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stdout: %v", err)
	}
	return buf.String()
}
