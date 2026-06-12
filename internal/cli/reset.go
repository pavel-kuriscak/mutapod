package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
	"github.com/mutapod/mutapod/internal/state"
	"github.com/mutapod/mutapod/internal/vscode"
)

var resetYes bool

func resetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reset [local|container]",
		Short: "Recreate the VM and local mutapod sessions from scratch",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runReset,
	}
	cmd.Flags().BoolVar(&resetYes, "yes", false, "skip the interactive confirmation prompt")
	cmd.Flags().Bool("build", false, "force docker compose to rebuild images after recreating the VM")
	return cmd
}

func runReset(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	launchMode, err := parseUpLaunchMode(args)
	if err != nil {
		return err
	}
	buildImages, err := cmd.Flags().GetBool("build")
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "About to reset workspace %q on VM %q.\n", cfg.Name, cfg.InstanceName())
	fmt.Fprintln(os.Stdout, "This will terminate local Mutagen sessions, delete the current VM if it exists, clear local mutapod state, and run `mutapod up` again.")
	if !resetYes {
		confirmed, err := confirmDestroy(os.Stdin, os.Stdout, cfg.InstanceName())
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("reset cancelled")
		}
	}

	if err := cleanupLocalWorkspace(ctx, cfg); err != nil {
		shell.Debugf("reset local cleanup: %v", err)
	}

	prov, err := provider.New(cfg, shell.DefaultCommander)
	if err != nil {
		return err
	}
	instanceState, err := prov.State(ctx)
	if err != nil {
		return err
	}
	if instanceState != provider.StateNotFound {
		step("Deleting VM...")
		if err := prov.DeleteInstance(ctx); err != nil {
			return err
		}
		ok("VM deleted: %s", cfg.InstanceName())
	}

	if err := state.Delete(cfg.Name); err != nil {
		return err
	}
	ok("Local mutapod state cleared: %s", cfg.Name)

	return runUpWithConfig(ctx, cfg, launchMode, buildImages, vmUpOptions{
		Interactive: isTerminal(os.Stdin) && isTerminal(os.Stdout),
		In:          os.Stdin,
		Out:         os.Stdout,
	})
}

var _ = vscode.LaunchLocal
