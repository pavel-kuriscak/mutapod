package vscode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/profiles"
	"github.com/mutapod/mutapod/internal/shell"
)

func TestConfigureAttachedContainer_GeneratesImageConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(`
services:
  web:
    image: testproject-web
    volumes:
      - .:/app
`), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	cfg := &config.Config{
		Name: "testproject",
		Dir:  dir,
		Compose: config.ComposeConfig{
			PrimaryService: "web",
			Extensions:     []string{"ms-python.python"},
		},
	}

	fake := shell.NewFakeCommander()
	fake.Stub("43ee2d7e7408\n", "docker", "--context", "mutapod-testproject", "compose", "-f", "compose.yaml", "ps", "-q", "web")
	fake.Stub("testproject-web\n", "docker", "--context", "mutapod-testproject", "inspect", "--format", "{{.Config.Image}}", "43ee2d7e7408")

	oldLookup := userConfigDirLookup
	userConfigDirLookup = func() (string, error) { return t.TempDir(), nil }
	defer func() {
		userConfigDirLookup = oldLookup
	}()
	oldHomeLookup := userHomeDirLookup
	homeDir := t.TempDir()
	userHomeDirLookup = func() (string, error) { return homeDir, nil }
	defer func() {
		userHomeDirLookup = oldHomeLookup
	}()
	writeExtensionPackage(t, homeDir, "github.copilot-chat-0.42.3", "GitHub", "copilot-chat")
	writeExtensionPackage(t, homeDir, "ms-python.python-2023.18.0", "ms-python", "python")

	path, err := ConfigureAttachedContainer(context.Background(), cfg, "mutapod-testproject", nil, fake)
	if err != nil {
		t.Fatalf("ConfigureAttachedContainer: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read attached config: %v", err)
	}

	var got attachedContainerConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse attached config: %v", err)
	}

	if got.WorkspaceFolder != "/app" {
		t.Fatalf("workspaceFolder: got %q", got.WorkspaceFolder)
	}
	wantExtensions := []string{"github.copilot-chat", "ms-python.python"}
	if !reflect.DeepEqual(got.Extensions, wantExtensions) {
		t.Fatalf("extensions: got %v, want %v", got.Extensions, wantExtensions)
	}
	if filepath.Base(path) != "testproject-web.json" {
		t.Fatalf("path: got %q", path)
	}
}

func TestConfigureAttachedContainer_IncludesProfileSettingsAndRemoteEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(`
services:
  web:
    image: testproject-web
    volumes:
      - .:/app
`), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	cfg := &config.Config{
		Name: "testproject",
		Dir:  dir,
		Compose: config.ComposeConfig{
			PrimaryService:      "web",
			CopyLocalExtensions: boolPtr(false),
		},
	}

	homeDir := t.TempDir()
	t.Setenv("USERPROFILE", homeDir)
	t.Setenv("HOME", homeDir)
	touchExtensionDir(t, filepath.Join(homeDir, ".codex"))
	touchExtensionDir(t, filepath.Join(homeDir, ".claude"))
	binDir := filepath.Join(homeDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	writeFakeBinary(t, filepath.Join(binDir, "codex.cmd"))
	writeFakeBinary(t, filepath.Join(binDir, "claude.cmd"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	activeProfiles, err := profiles.Active(cfg)
	if err != nil {
		t.Fatalf("profiles.Active: %v", err)
	}
	if len(activeProfiles) != 2 {
		t.Fatalf("profiles.Active: got %d profiles, want 2", len(activeProfiles))
	}

	fake := shell.NewFakeCommander()
	fake.Stub("43ee2d7e7408\n", "docker", "--context", "mutapod-testproject", "compose", "-f", "compose.yaml", "ps", "-q", "web")
	fake.Stub("testproject-web\n", "docker", "--context", "mutapod-testproject", "inspect", "--format", "{{.Config.Image}}", "43ee2d7e7408")

	oldLookup := userConfigDirLookup
	userConfigDirLookup = func() (string, error) { return t.TempDir(), nil }
	defer func() {
		userConfigDirLookup = oldLookup
	}()
	oldHomeLookup := userHomeDirLookup
	userHomeDirLookup = func() (string, error) { return t.TempDir(), nil }
	defer func() {
		userHomeDirLookup = oldHomeLookup
	}()

	path, err := ConfigureAttachedContainer(context.Background(), cfg, "mutapod-testproject", activeProfiles, fake)
	if err != nil {
		t.Fatalf("ConfigureAttachedContainer: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read attached config: %v", err)
	}

	var got attachedContainerConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse attached config: %v", err)
	}

	if _, ok := got.Settings["chatgpt.cliExecutable"]; ok {
		t.Fatalf("chatgpt.cliExecutable should not be set, got %#v", got.Settings["chatgpt.cliExecutable"])
	}
	if got.Settings["claudeCode.claudeProcessWrapper"] != "/usr/local/bin/claude" {
		t.Fatalf("claudeCode.claudeProcessWrapper: got %#v", got.Settings["claudeCode.claudeProcessWrapper"])
	}
	if got.Settings["claudeCode.disableLoginPrompt"] != true {
		t.Fatalf("claudeCode.disableLoginPrompt: got %#v", got.Settings["claudeCode.disableLoginPrompt"])
	}
	if got.RemoteUser != "" {
		t.Fatalf("RemoteUser: got %q, want empty", got.RemoteUser)
	}
	if got.RemoteEnv["CODEX_HOME"] != profiles.RootHomeDir+"/.codex" {
		t.Fatalf("CODEX_HOME: got %q", got.RemoteEnv["CODEX_HOME"])
	}
	if got.RemoteEnv["CLAUDE_CONFIG_DIR"] != profiles.RootHomeDir+"/.claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR: got %q", got.RemoteEnv["CLAUDE_CONFIG_DIR"])
	}
}

func TestAttachedConfigFilenames_IncludeEncodedVariant(t *testing.T) {
	got := attachedConfigFilenames("gtctrm/webapp:latest")
	want := []string{"gtctrm%2fwebapp%3alatest.json", "gtctrm_webapp_latest.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachedConfigFilenames: got %v, want %v", got, want)
	}
}

func TestConfigureAttachedContainer_UsesExplicitWorkspaceFolderAndCanSkipLocalExtensions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {web: {image: testproject-web}}"), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	copyLocal := false
	cfg := &config.Config{
		Name: "testproject",
		Dir:  dir,
		Compose: config.ComposeConfig{
			PrimaryService:      "web",
			WorkspaceFolder:     "/workspace/app",
			Extensions:          []string{"ms-python.python"},
			CopyLocalExtensions: &copyLocal,
		},
	}

	fake := shell.NewFakeCommander()
	fake.Stub("43ee2d7e7408\n", "docker", "--context", "mutapod-testproject", "compose", "-f", "compose.yaml", "ps", "-q", "web")
	fake.Stub("testproject-web\n", "docker", "--context", "mutapod-testproject", "inspect", "--format", "{{.Config.Image}}", "43ee2d7e7408")

	oldLookup := userConfigDirLookup
	userConfigDirLookup = func() (string, error) { return t.TempDir(), nil }
	defer func() {
		userConfigDirLookup = oldLookup
	}()
	oldHomeLookup := userHomeDirLookup
	userHomeDirLookup = func() (string, error) { return t.TempDir(), nil }
	defer func() {
		userHomeDirLookup = oldHomeLookup
	}()

	path, err := ConfigureAttachedContainer(context.Background(), cfg, "mutapod-testproject", nil, fake)
	if err != nil {
		t.Fatalf("ConfigureAttachedContainer: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read attached config: %v", err)
	}

	var got attachedContainerConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse attached config: %v", err)
	}
	if got.WorkspaceFolder != "/workspace/app" {
		t.Fatalf("workspaceFolder: got %q", got.WorkspaceFolder)
	}
	wantExtensions := []string{"ms-python.python"}
	if !reflect.DeepEqual(got.Extensions, wantExtensions) {
		t.Fatalf("extensions: got %v, want %v", got.Extensions, wantExtensions)
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func touchExtensionDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeFakeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("@echo off\r\n"), 0644); err != nil {
		t.Fatalf("write fake binary %s: %v", path, err)
	}
}

func writeExtensionPackage(t *testing.T, homeDir, dirName, publisher, name string) {
	t.Helper()
	extDir := filepath.Join(homeDir, ".vscode", "extensions", dirName)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatalf("mkdir extension dir: %v", err)
	}
	content := fmt.Sprintf(`{"publisher":%q,"name":%q}`, publisher, name)
	if err := os.WriteFile(filepath.Join(extDir, "package.json"), []byte(content), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
}
