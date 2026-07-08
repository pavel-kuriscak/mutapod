package vscode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/shell"
)

func TestLaunchLocalWorkspace_UsesWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Name: "testproject", Dir: dir}
	fake := shell.NewFakeCommander()

	if err := Launch(context.Background(), cfg, "mutapod-testproject", LaunchLocal, fake); err != nil {
		t.Fatalf("Launch(local): %v", err)
	}
	if !fake.CalledWith(codeCLI(), filepath.Join(dir, WorkspaceFilename())) {
		t.Fatalf("expected code workspace launch, got %#v", fake.Calls)
	}
}

func TestLaunchAttachedContainer_UsesAttachedContainerURI(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Name: "testproject",
		Dir:  dir,
		Compose: config.ComposeConfig{
			PrimaryService:  "web",
			WorkspaceFolder: "/app",
		},
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {web: {image: testproject-web}}"), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	fake := shell.NewFakeCommander()
	fake.Stub("43ee2d7e7408\n", "docker", "--context", "mutapod-testproject", "compose", "-f", "compose.yaml", "ps", "-q", "web")
	fake.Stub("/testproject-web-1\n", "docker", "--context", "mutapod-testproject", "inspect", "--format", "{{.Name}}", "43ee2d7e7408")

	if err := Launch(context.Background(), cfg, "mutapod-testproject", LaunchAttached, fake); err != nil {
		t.Fatalf("Launch(container): %v", err)
	}
	if len(fake.Calls) == 0 {
		t.Fatal("expected code launch call")
	}
	last := fake.Calls[len(fake.Calls)-1]
	if last.Name != codeCLI() || len(last.Args) != 2 || last.Args[0] != "--folder-uri" {
		t.Fatalf("unexpected final call: %#v", last)
	}
	if !strings.HasPrefix(last.Args[1], "vscode-remote://attached-container+") {
		t.Fatalf("expected attached-container uri, got %q", last.Args[1])
	}
	if !strings.HasSuffix(last.Args[1], "/app") {
		t.Fatalf("expected workspace path suffix, got %q", last.Args[1])
	}
}

func TestLaunchHeadless_DoesNotInvokeVSCode(t *testing.T) {
	cfg := &config.Config{Name: "testproject", Dir: t.TempDir()}
	fake := shell.NewFakeCommander()

	if err := Launch(context.Background(), cfg, "mutapod-testproject", LaunchHeadless, fake); err != nil {
		t.Fatalf("Launch(headless): %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("expected no launch calls, got %#v", fake.Calls)
	}
}
