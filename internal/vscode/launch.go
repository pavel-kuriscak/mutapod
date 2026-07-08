package vscode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mutapod/mutapod/internal/compose"
	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/shell"
)

type LaunchMode string

const (
	LaunchAttached LaunchMode = "container"
	LaunchLocal    LaunchMode = "local"
	LaunchHeadless LaunchMode = "headless"
)

func Launch(ctx context.Context, cfg *config.Config, dockerContext string, mode LaunchMode, cmd shell.Commander) error {
	switch mode {
	case LaunchLocal:
		return launchLocalWorkspace(ctx, cfg, cmd)
	case LaunchAttached:
		return launchAttachedContainer(ctx, cfg, dockerContext, cmd)
	case LaunchHeadless:
		return nil
	default:
		return fmt.Errorf("vscode: unsupported launch mode %q", mode)
	}
}

func launchLocalWorkspace(ctx context.Context, cfg *config.Config, cmd shell.Commander) error {
	workspacePath := filepath.Join(cfg.Dir, WorkspaceFilename())
	return cmd.Run(ctx, shell.RunOptions{}, codeCLI(), workspacePath)
}

func launchAttachedContainer(ctx context.Context, cfg *config.Config, dockerContext string, cmd shell.Commander) error {
	if cfg.Compose.PrimaryService == "" {
		return fmt.Errorf("vscode: compose.primary_service is required for attached-container launch")
	}

	workspaceFolder := cfg.Compose.WorkspaceFolder
	if workspaceFolder == "" {
		var err error
		workspaceFolder, err = compose.DetectWorkspaceFolder(cfg, cfg.Compose.PrimaryService)
		if err != nil {
			return err
		}
	}
	if workspaceFolder == "" {
		return fmt.Errorf("vscode: could not determine workspace folder for service %q", cfg.Compose.PrimaryService)
	}
	if !strings.HasPrefix(workspaceFolder, "/") {
		workspaceFolder = "/" + workspaceFolder
	}

	containerName, err := primaryServiceContainerName(ctx, cfg, dockerContext, cmd)
	if err != nil {
		return err
	}

	folderURI, err := attachedContainerFolderURI(containerName, dockerContext, workspaceFolder)
	if err != nil {
		return err
	}
	return cmd.Run(ctx, shell.RunOptions{}, codeCLI(), "--folder-uri", folderURI)
}

func primaryServiceContainerName(ctx context.Context, cfg *config.Config, dockerContext string, cmd shell.Commander) (string, error) {
	containerID, err := primaryServiceContainerID(ctx, cfg, dockerContext, cmd)
	if err != nil {
		return "", err
	}

	containerName, err := cmd.Output(ctx, shell.RunOptions{Dir: cfg.Dir}, "docker",
		"--context", dockerContext,
		"inspect", "--format", "{{.Name}}", containerID,
	)
	if err != nil {
		return "", err
	}

	name := strings.TrimSpace(string(containerName))
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", fmt.Errorf("container %s did not report a name", containerID)
	}
	return name, nil
}

func primaryServiceContainerID(ctx context.Context, cfg *config.Config, dockerContext string, cmd shell.Commander) (string, error) {
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

	id := strings.TrimSpace(string(containerID))
	if id == "" {
		return "", fmt.Errorf("no running container found for service %q", cfg.Compose.PrimaryService)
	}
	return id, nil
}

func attachedContainerFolderURI(containerName, dockerContext, workspaceFolder string) (string, error) {
	payload := map[string]any{
		"containerName": containerName,
		"settings": map[string]string{
			"context": dockerContext,
		},
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("vscode: marshal attached-container payload: %w", err)
	}
	return "vscode-remote://attached-container+" + hex.EncodeToString(encoded) + workspaceFolder, nil
}

func codeCLI() string {
	if runtime.GOOS != "windows" {
		return "code"
	}

	localAppData := os.Getenv("LocalAppData")
	if localAppData != "" {
		candidate := filepath.Join(localAppData, "Programs", "Microsoft VS Code", "bin", "code.cmd")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "code.cmd"
}
