package vscode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutapod/mutapod/internal/compose"
	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/profiles"
	"github.com/mutapod/mutapod/internal/shell"
)

const attachedConfigStorageDir = "ms-vscode-remote.remote-containers"

var userConfigDirLookup = os.UserConfigDir
var userHomeDirLookup = os.UserHomeDir

type attachedContainerConfig struct {
	Extensions      []string          `json:"extensions,omitempty"`
	Settings        map[string]any    `json:"settings,omitempty"`
	RemoteEnv       map[string]string `json:"remoteEnv,omitempty"`
	RemoteUser      string            `json:"remoteUser,omitempty"`
	WorkspaceFolder string            `json:"workspaceFolder,omitempty"`
}

// ConfigureAttachedContainer pre-generates the VS Code "Attach to Running
// Container" defaults for the primary service so attach opens in the right
// folder with the expected extensions.
func ConfigureAttachedContainer(ctx context.Context, cfg *config.Config, dockerContext string, activeProfiles []profiles.Spec, cmd shell.Commander) (string, error) {
	if cfg.Compose.PrimaryService == "" {
		return "", nil
	}

	imageName, err := primaryServiceImage(ctx, cfg, dockerContext, cmd)
	if err != nil {
		return "", fmt.Errorf("vscode: resolve attached container image: %w", err)
	}
	containerName, err := primaryServiceContainerName(ctx, cfg, dockerContext, cmd)
	if err != nil {
		return "", fmt.Errorf("vscode: resolve attached container name: %w", err)
	}

	workspaceFolder := cfg.Compose.WorkspaceFolder
	if workspaceFolder == "" {
		workspaceFolder, err = compose.DetectWorkspaceFolder(cfg, cfg.Compose.PrimaryService)
		if err != nil {
			shell.Debugf("vscode: infer workspace folder: %v", err)
		}
	}

	extensions := append([]string(nil), cfg.Compose.Extensions...)
	if cfg.Compose.CopyLocalExtensionsEnabled() {
		localExtensions, err := listLocalExtensions()
		if err != nil {
			shell.Debugf("vscode: list local extensions: %v", err)
		} else {
			extensions = mergeUnique(localExtensions, extensions)
		}
	}

	settings := make(map[string]any)
	remoteEnv := make(map[string]string)
	for _, profile := range activeProfiles {
		mergeAnyMap(settings, profile.AttachedContainerSettings())
		mergeStringMap(remoteEnv, profile.AttachedContainerRemoteEnv())
	}
	settings["remote.autoForwardPorts"] = false

	storageDir, err := attachedContainerStorageDir()
	if err != nil {
		return "", err
	}

	data := attachedContainerConfig{
		Extensions:      extensions,
		Settings:        settings,
		RemoteEnv:       remoteEnv,
		WorkspaceFolder: workspaceFolder,
	}
	encoded, err := json.MarshalIndent(data, "", "\t")
	if err != nil {
		return "", fmt.Errorf("vscode: marshal attached config: %w", err)
	}
	imageConfigDir := filepath.Join(storageDir, "imageConfigs")
	if err := writeAttachedContainerConfig(imageConfigDir, attachedConfigFilenames(imageName), encoded); err != nil {
		return "", err
	}
	nameConfigDir := filepath.Join(storageDir, "nameConfigs")
	nameFilenames := attachedConfigFilenames(containerName)
	if err := writeAttachedContainerConfig(nameConfigDir, nameFilenames, encoded); err != nil {
		return "", err
	}
	return filepath.Join(nameConfigDir, nameFilenames[0]), nil
}

func attachedContainerStorageDir() (string, error) {
	root, err := userConfigDirLookup()
	if err != nil {
		return "", fmt.Errorf("vscode: resolve user config dir: %w", err)
	}
	return filepath.Join(root, "Code", "User", "globalStorage", attachedConfigStorageDir), nil
}

func writeAttachedContainerConfig(dir string, filenames []string, encoded []byte) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("vscode: create attached config dir: %w", err)
	}
	for _, name := range filenames {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, append(encoded, '\n'), 0644); err != nil {
			return fmt.Errorf("vscode: write attached config: %w", err)
		}
	}
	return nil
}

func primaryServiceImage(ctx context.Context, cfg *config.Config, dockerContext string, cmd shell.Commander) (string, error) {
	composeArgs, err := compose.LocalComposeArgs(cfg)
	if err != nil {
		return "", err
	}

	psArgs := append([]string{"--context", dockerContext}, composeArgs...)
	psArgs = append(psArgs, "ps", "-q", cfg.Compose.PrimaryService)
	containerID, err := cmd.Output(ctx, shell.RunOptions{Dir: cfg.Dir}, "docker", psArgs...)
	if err != nil {
		return "", err
	}
	trimmedContainerID := strings.TrimSpace(string(containerID))
	if trimmedContainerID == "" {
		return "", fmt.Errorf("no running container found for service %q", cfg.Compose.PrimaryService)
	}

	imageName, err := cmd.Output(ctx, shell.RunOptions{Dir: cfg.Dir}, "docker",
		"--context", dockerContext,
		"inspect", "--format", "{{.Config.Image}}", trimmedContainerID,
	)
	if err != nil {
		return "", err
	}
	trimmedImageName := strings.TrimSpace(string(imageName))
	if trimmedImageName == "" {
		return "", fmt.Errorf("container %s did not report an image name", trimmedContainerID)
	}
	return trimmedImageName, nil
}

type extensionPackage struct {
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
}

func listLocalExtensions() ([]string, error) {
	homeDir, err := userHomeDirLookup()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(filepath.Join(homeDir, ".vscode", "extensions"))
	if err != nil {
		return nil, err
	}

	var extensions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packagePath := filepath.Join(homeDir, ".vscode", "extensions", entry.Name(), "package.json")
		data, err := os.ReadFile(packagePath)
		if err != nil {
			continue
		}

		var pkg extensionPackage
		if err := json.Unmarshal(data, &pkg); err != nil {
			continue
		}
		if pkg.Publisher == "" || pkg.Name == "" {
			continue
		}
		extensions = append(extensions, strings.ToLower(pkg.Publisher+"."+pkg.Name))
	}
	return mergeUnique(extensions), nil
}

func attachedConfigFilename(imageName string) string {
	var builder strings.Builder
	for _, r := range imageName {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '.', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	return builder.String() + ".json"
}

func attachedConfigFilenames(imageName string) []string {
	encoded := encodedAttachedConfigFilename(imageName)
	legacy := attachedConfigFilename(imageName)
	if encoded == legacy {
		return []string{encoded}
	}
	return []string{encoded, legacy}
}

func encodedAttachedConfigFilename(imageName string) string {
	return strings.ToLower(url.QueryEscape(imageName)) + ".json"
}

func mergeUnique(items ...[]string) []string {
	seen := make(map[string]bool, len(items))
	var merged []string
	for _, group := range items {
		for _, item := range group {
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			merged = append(merged, item)
		}
	}
	return merged
}

func mergeAnyMap(dst map[string]any, src map[string]any) {
	for key, value := range src {
		dst[key] = value
	}
}

func mergeStringMap(dst map[string]string, src map[string]string) {
	for key, value := range src {
		dst[key] = value
	}
}
