// Package vscode prints local VS Code usage instructions.
package vscode

import (
	"fmt"
	"strings"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
)

// PrintInstructions prints everything the user needs to work locally while the
// runtime stays on the remote VM.
func PrintInstructions(cfg *config.Config, sshCfg *provider.SSHConfig, ports []int, launchMode LaunchMode) {
	workspaceFile := workspaceFilename
	dockerContext := cfg.InstanceName()

	fmt.Println()
	fmt.Println("=======================================================")
	fmt.Printf("  mutapod ready: %s\n", cfg.Name)
	fmt.Println("=======================================================")
	fmt.Println()
	switch launchMode {
	case LaunchHeadless:
		fmt.Println("  Headless mode is active; VS Code launch was skipped.")
		fmt.Println("  Use `mutapod up` later to open VS Code attached to the main container.")
	case LaunchLocal:
		fmt.Println("  mutapod opened the local workspace wrapper.")
		fmt.Println("  Use `mutapod up` if you want the attached-container window instead.")
	default:
		fmt.Println("  mutapod up auto-opens VS Code attached to the main container.")
		fmt.Println("  Use `mutapod up local` if you want the local mutapod workspace window instead.")
	}
	fmt.Println("  Use `mutapod up headless` when you want startup, sync, services, and keepalive without VS Code.")
	fmt.Println()
	fmt.Println("  Local workspace command:")
	fmt.Printf("    code %q\n", workspaceFile)
	fmt.Println()
	fmt.Println("  Your editor stays local. The runtime is on the VM.")
	fmt.Println("  The generated workspace file applies Docker overrides only for this project.")
	fmt.Printf("  SSH host:   %s\n", sshCfg.Host)
	fmt.Printf("  Docker ctx: %s\n", dockerContext)
	fmt.Printf("  VM path:    %s\n", cfg.WorkspacePath())
	if len(ports) > 0 {
		fmt.Printf("  App ports:  %s\n", formatPorts(ports))
	}
	fmt.Println()
	printDevContainerHint(cfg)
	fmt.Println()
	fmt.Println("  If the Containers view does not refresh immediately, reload this window once.")
	fmt.Println()
	fmt.Println("  When done: mutapod down")
	fmt.Println("=======================================================")
	fmt.Println()
}

func printDevContainerHint(cfg *config.Config) {
	fmt.Println("  Dev Containers attach:")
	fmt.Println("    Use this from the local mutapod workspace.")
	if cfg.Compose.PrimaryService != "" {
		fmt.Printf("    Main service: %s\n", cfg.Compose.PrimaryService)
		fmt.Printf("    Terminal check: docker compose ps %s\n", cfg.Compose.PrimaryService)
		fmt.Println("    mutapod pre-generated the attached-container profile for this service.")
		if cfg.Compose.CopyLocalExtensionsEnabled() {
			fmt.Println("    It also copied your current local VS Code extensions into that profile.")
		}
		fmt.Println("    Then run: Dev Containers: Attach to Running Container...")
		fmt.Printf("    Pick the running container for service %q on this remote Docker host.\n", cfg.Compose.PrimaryService)
		return
	}
	fmt.Println("    Run: Dev Containers: Attach to Running Container...")
	fmt.Println("    Pick the container you want from this remote Docker host.")
	fmt.Println("    Tip: set compose.primary_service in mutapod.yaml to get a more specific hint here.")
}

func formatPorts(ports []int) string {
	items := make([]string, 0, len(ports))
	for _, p := range ports {
		items = append(items, fmt.Sprintf("localhost:%d", p))
	}
	return strings.Join(items, ", ")
}
