package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/profiles"
)

func writeCompose(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path
}

func cfg(dir string) *config.Config {
	return &config.Config{Dir: dir, Name: "test"}
}

func TestDetectFile_AutoDetect(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", "services: {}")

	path, err := DetectFile(cfg(dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "compose.yaml" {
		t.Errorf("expected compose.yaml, got %s", filepath.Base(path))
	}
}

func TestDetectFile_FallbackOrder(t *testing.T) {
	dir := t.TempDir()
	// Only docker-compose.yaml exists
	writeCompose(t, dir, "docker-compose.yaml", "services: {}")

	path, err := DetectFile(cfg(dir))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "docker-compose.yaml" {
		t.Errorf("expected docker-compose.yaml, got %s", filepath.Base(path))
	}
}

func TestDetectFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := DetectFile(cfg(dir))
	if err == nil {
		t.Fatal("expected error for missing compose file")
	}
}

func TestDetectFile_Override(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "myapp.yaml", "services: {}")

	c := cfg(dir)
	c.Compose.File = "myapp.yaml"
	path, err := DetectFile(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(path) != "myapp.yaml" {
		t.Errorf("expected myapp.yaml, got %s", filepath.Base(path))
	}
}

func TestParsePorts_ShortForm(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  web:
    ports:
      - "5000:5000"
      - "8080:8080"
`)
	ports, err := ParsePorts(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(ports)
	want := []int{5000, 8080}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports: got %v, want %v", ports, want)
	}
}

func TestParsePorts_ShortFormNoHost(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  web:
    ports:
      - "3000"
`)
	ports, err := ParsePorts(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 1 || ports[0] != 3000 {
		t.Errorf("ports: got %v, want [3000]", ports)
	}
}

func TestParsePorts_LongForm(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  web:
    ports:
      - published: 5000
        target: 5000
      - published: 9000
        target: 9000
`)
	ports, err := ParsePorts(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(ports)
	want := []int{5000, 9000}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports: got %v, want %v", ports, want)
	}
}

func TestParsePorts_MultipleServices(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  web:
    ports:
      - "5000:5000"
  db:
    ports:
      - "5432:5432"
  redis:
    ports:
      - "6379:6379"
`)
	ports, err := ParsePorts(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(ports)
	want := []int{5000, 5432, 6379}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports: got %v, want %v", ports, want)
	}
}

func TestParsePorts_NoPorts(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  worker:
    image: myworker
`)
	ports, err := ParsePorts(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected no ports, got %v", ports)
	}
}

func TestParsePorts_ExtraPorts(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  web:
    ports:
      - "5000:5000"
`)
	ports, err := ParsePorts(path, []int{8000, 9000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(ports)
	want := []int{5000, 8000, 9000}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports: got %v, want %v", ports, want)
	}
}

func TestParsePorts_NoDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose.yaml", `
services:
  web:
    ports:
      - "5000:5000"
  proxy:
    ports:
      - "5000:5000"
`)
	ports, err := ParsePorts(path, []int{5000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 1 || ports[0] != 5000 {
		t.Errorf("expected exactly one port 5000, got %v", ports)
	}
}

func TestRemoteComposeArgs_DefaultFileAtRoot(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", "services: {}")

	args, err := remoteComposeArgs(cfg(dir), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args != " -f 'compose.yaml'" {
		t.Fatalf("args: got %q", args)
	}
}

func TestRemoteComposeArgs_CustomRelativeFile(t *testing.T) {
	dir := t.TempDir()
	customDir := filepath.Join(dir, "deploy")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeCompose(t, customDir, "dev.yaml", "services: {}")

	c := cfg(dir)
	c.Compose.File = filepath.Join("deploy", "dev.yaml")

	args, err := remoteComposeArgs(c, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args != " -f 'deploy/dev.yaml'" {
		t.Fatalf("args: got %q", args)
	}
}

func TestComposeDevFileFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeCompose(t, dir, "compose-dev.yaml", `
services:
  web:
    ports:
      - "8000:8000"
  webpack:
    ports:
      - "3000:3000"
`)

	c := cfg(dir)
	c.Compose.File = "compose-dev.yaml"

	detected, err := DetectFile(c)
	if err != nil {
		t.Fatalf("DetectFile: unexpected error: %v", err)
	}
	if detected != path {
		t.Fatalf("DetectFile: got %q, want %q", detected, path)
	}

	ports, err := ParsePorts(detected, nil)
	if err != nil {
		t.Fatalf("ParsePorts: unexpected error: %v", err)
	}
	sort.Ints(ports)
	wantPorts := []int{3000, 8000}
	if !reflect.DeepEqual(ports, wantPorts) {
		t.Fatalf("ParsePorts: got %v, want %v", ports, wantPorts)
	}

	args, err := remoteComposeArgs(c, false)
	if err != nil {
		t.Fatalf("remoteComposeArgs: unexpected error: %v", err)
	}
	if args != " -f 'compose-dev.yaml'" {
		t.Fatalf("remoteComposeArgs: got %q", args)
	}

	remotePath, err := RemoteComposePath(c)
	if err != nil {
		t.Fatalf("RemoteComposePath: unexpected error: %v", err)
	}
	if remotePath != "/workspace/test/compose-dev.yaml" {
		t.Fatalf("RemoteComposePath: got %q", remotePath)
	}
}

func TestLocalComposeArgs_CustomRelativeFile(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose-dev.yaml", "services: {}")

	c := cfg(dir)
	c.Compose.File = "compose-dev.yaml"

	args, err := LocalComposeArgs(c)
	if err != nil {
		t.Fatalf("LocalComposeArgs: unexpected error: %v", err)
	}
	want := []string{"compose", "-f", "compose-dev.yaml"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("LocalComposeArgs: got %v, want %v", args, want)
	}
}

func TestDetectWorkspaceFolder_FromBindMount(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    volumes:
      - .:/app
`)

	c := cfg(dir)
	c.Compose.PrimaryService = "web"

	got, err := DetectWorkspaceFolder(c, "web")
	if err != nil {
		t.Fatalf("DetectWorkspaceFolder: unexpected error: %v", err)
	}
	if got != "/app" {
		t.Fatalf("DetectWorkspaceFolder: got %q, want %q", got, "/app")
	}
}

func TestDetectWorkspaceFolder_FromWorkingDir(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    working_dir: /workspace/app
`)

	c := cfg(dir)
	got, err := DetectWorkspaceFolder(c, "web")
	if err != nil {
		t.Fatalf("DetectWorkspaceFolder: unexpected error: %v", err)
	}
	if got != "/workspace/app" {
		t.Fatalf("DetectWorkspaceFolder: got %q, want %q", got, "/workspace/app")
	}
}

func TestUpCommand_DefaultSkipsBuild(t *testing.T) {
	got := upCommand("/workspace/testproject", " -f 'compose.yaml'", false)
	want := "cd '/workspace/testproject' && sudo docker compose -f 'compose.yaml' up -d"
	if got != want {
		t.Fatalf("upCommand: got %q, want %q", got, want)
	}
}

func TestUpCommand_BuildOverride(t *testing.T) {
	got := upCommand("/workspace/testproject", " -f 'compose.yaml'", true)
	want := "cd '/workspace/testproject' && sudo docker compose -f 'compose.yaml' up -d --build"
	if got != want {
		t.Fatalf("upCommand: got %q, want %q", got, want)
	}
}

func TestRemoteComposeArgs_WithWorkspaceOverride(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", "services: {}")

	args, err := remoteComposeArgs(cfg(dir), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args != " -f 'compose.yaml' -f '.mutapod.compose.override.yaml'" {
		t.Fatalf("args: got %q", args)
	}
}

func TestNeedsWorkspaceOverride_WhenPrimaryServiceHasNoWorkspaceMount(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    image: demo
`)
	c := cfg(dir)
	c.Compose.PrimaryService = "web"
	c.Compose.WorkspaceFolder = "/app"

	needed, err := NeedsWorkspaceOverride(c)
	if err != nil {
		t.Fatalf("NeedsWorkspaceOverride: %v", err)
	}
	if !needed {
		t.Fatal("expected workspace override to be needed")
	}
}

func TestNeedsWorkspaceOverride_WhenWorkspaceAlreadyMounted(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    volumes:
      - .:/app
`)
	c := cfg(dir)
	c.Compose.PrimaryService = "web"
	c.Compose.WorkspaceFolder = "/app"

	needed, err := NeedsWorkspaceOverride(c)
	if err != nil {
		t.Fatalf("NeedsWorkspaceOverride: %v", err)
	}
	if needed {
		t.Fatal("expected workspace override to be unnecessary")
	}
}

func TestRenderRemoteOverride_WorkspaceMount(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    image: demo
`)
	c := &config.Config{Dir: dir, Name: "test", Compose: config.ComposeConfig{
		PrimaryService:  "web",
		WorkspaceFolder: "/app",
	}, Sync: config.SyncConfig{RemotePath: "/workspace/test"}}

	data, err := renderRemoteOverride(c, nil)
	if err != nil {
		t.Fatalf("renderRemoteOverride: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "web:") || !strings.Contains(text, "/workspace/test:/app") {
		t.Fatalf("unexpected override: %s", text)
	}
}

func TestRenderRemoteOverride_WithProfileMountsAndEnv(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    image: demo
`)
	c := &config.Config{Dir: dir, Name: "test", Compose: config.ComposeConfig{
		PrimaryService: "web",
	}, Sync: config.SyncConfig{RemotePath: "/workspace/test"}}

	data, err := renderRemoteOverride(c, []profiles.Spec{{
		Name:                   "codex",
		NeedsSandboxNamespaces: true,
		Mounts: []profiles.Mount{
			{RemotePath: "/var/lib/mutapod/profiles/codex", ContainerPath: profiles.RootHomeDir + "/.codex"},
			{RemotePath: "/var/lib/mutapod/tools/codex", ContainerPath: "/var/lib/mutapod/tools/codex"},
		},
	}})
	if err != nil {
		t.Fatalf("renderRemoteOverride: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "/var/lib/mutapod/profiles/codex:"+profiles.RootHomeDir+"/.codex") {
		t.Fatalf("expected codex mount in override: %s", text)
	}
	if !strings.Contains(text, "/var/lib/mutapod/tools/codex:/var/lib/mutapod/tools/codex") {
		t.Fatalf("expected codex tool mount in override: %s", text)
	}
	if !strings.Contains(text, "SYS_ADMIN") {
		t.Fatalf("expected codex sandbox capability in override: %s", text)
	}
	if !strings.Contains(text, "apparmor=unconfined") {
		t.Fatalf("expected codex sandbox security option in override: %s", text)
	}
	if !strings.Contains(text, "seccomp=unconfined") {
		t.Fatalf("expected codex seccomp option in override: %s", text)
	}
}

func TestRenderRemoteOverride_WithReverseForwardsAddsHostDockerInternal(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, "compose.yaml", `
services:
  web:
    image: demo
`)
	c := &config.Config{Dir: dir, Name: "test", Compose: config.ComposeConfig{
		PrimaryService:  "web",
		ReverseForwards: []int{8154},
	}, Sync: config.SyncConfig{RemotePath: "/workspace/test"}}

	data, err := renderRemoteOverride(c, nil)
	if err != nil {
		t.Fatalf("renderRemoteOverride: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "host.docker.internal:host-gateway") {
		t.Fatalf("expected host.docker.internal mapping in override: %s", text)
	}
}

func TestNeedsRemoteOverride_WithReverseForwardsRequiresOverride(t *testing.T) {
	c := &config.Config{Name: "test", Compose: config.ComposeConfig{
		PrimaryService:  "web",
		ReverseForwards: []int{8154},
	}}

	needed, err := NeedsRemoteOverride(c, nil)
	if err != nil {
		t.Fatalf("NeedsRemoteOverride: %v", err)
	}
	if !needed {
		t.Fatal("expected reverse forwards to require a remote override")
	}
}

func TestNeedsRemoteOverride_WithSandboxNamespacesRequiresOverride(t *testing.T) {
	c := &config.Config{Name: "test", Compose: config.ComposeConfig{
		PrimaryService: "web",
	}}

	needed, err := NeedsRemoteOverride(c, []profiles.Spec{{Name: "codex", NeedsSandboxNamespaces: true}})
	if err != nil {
		t.Fatalf("NeedsRemoteOverride: %v", err)
	}
	if !needed {
		t.Fatal("expected sandbox namespace support to require a remote override")
	}
}
