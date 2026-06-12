package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/deps"
	"github.com/mutapod/mutapod/internal/idle"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
	"github.com/mutapod/mutapod/internal/state"
	mutagensync "github.com/mutapod/mutapod/internal/sync"
)

var destroyYes bool

func destroyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroy the remote VM and clean up local mutapod state",
		RunE:  runDestroy,
	}
	cmd.Flags().BoolVar(&destroyYes, "yes", false, "skip the interactive confirmation prompt")
	return cmd
}

func runDestroy(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	prov, err := provider.New(cfg, shell.DefaultCommander)
	if err != nil {
		return err
	}

	instanceState, err := prov.State(ctx)
	if err != nil {
		return err
	}

	leases, leaseWarn := inspectDestroyLeases(ctx, prov, instanceState, cfg)
	otherLeases := otherWorkspaceLeases(cfg.Name, leases)

	printDestroySummary(cfg, instanceState, otherLeases, leaseWarn)

	if !destroyYes {
		confirmed, err := confirmDestroy(os.Stdin, os.Stdout, cfg.InstanceName())
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("destroy cancelled")
		}
	}

	if err := cleanupLocalWorkspace(ctx, cfg); err != nil {
		return err
	}

	if instanceState == provider.StateNotFound {
		if err := state.Delete(cfg.Name); err != nil {
			return err
		}
		ok("VM already absent; local mutapod state cleaned up for %s", cfg.Name)
		return nil
	}

	step("Deleting VM...")
	if err := prov.DeleteInstance(ctx); err != nil {
		return err
	}

	if err := state.Delete(cfg.Name); err != nil {
		return err
	}

	ok("VM destroyed: %s", cfg.InstanceName())
	return nil
}

func inspectDestroyLeases(ctx context.Context, prov provider.Provider, instanceState provider.InstanceState, cfg *config.Config) ([]idle.LeaseInfo, string) {
	if instanceState != provider.StateRunning {
		return nil, fmt.Sprintf("Could not inspect VM-side leases because %s is %s.", cfg.InstanceName(), instanceState)
	}

	if _, err := prov.SSHConfig(ctx); err != nil {
		return nil, fmt.Sprintf("Could not inspect VM-side leases before destroy: %v", err)
	}

	leases, err := idle.ListLeases(ctx, prov)
	if err != nil {
		return nil, fmt.Sprintf("Could not inspect VM-side leases before destroy: %v", err)
	}
	return leases, ""
}

func printDestroySummary(cfg *config.Config, instanceState provider.InstanceState, otherLeases []idle.LeaseInfo, leaseWarn string) {
	fmt.Fprintf(os.Stdout, "About to destroy VM %q for workspace %q.\n", cfg.InstanceName(), cfg.Name)
	fmt.Fprintf(os.Stdout, "Current instance state: %s\n", instanceState)
	fmt.Fprintln(os.Stdout, "This will permanently delete the VM and remove local mutapod state for this workspace.")

	if leaseWarn != "" {
		fmt.Fprintf(os.Stdout, "Warning: %s\n", leaseWarn)
	}
	if len(otherLeases) == 0 {
		return
	}

	sort.Slice(otherLeases, func(i, j int) bool {
		return otherLeases[i].Workspace < otherLeases[j].Workspace
	})

	fmt.Fprintln(os.Stdout, "Warning: other mutapod workspace leases were found on this VM:")
	now := time.Now()
	for _, lease := range otherLeases {
		expiresAt := time.Unix(lease.ExpiresUnix, 0)
		remaining := expiresAt.Sub(now).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(os.Stdout, "  - %s (host=%s, expires=%s, time_left=%s)\n",
			lease.Workspace,
			lease.HostID,
			expiresAt.Format(time.RFC3339),
			remaining,
		)
	}
	fmt.Fprintln(os.Stdout, "Destroying the VM will interrupt every workspace still using it.")
}

func confirmDestroy(in io.Reader, out io.Writer, instanceName string) (bool, error) {
	return confirmYesNo(in, out, fmt.Sprintf("Delete VM %q? [y/N]: ", instanceName))
}

func otherWorkspaceLeases(currentWorkspace string, leases []idle.LeaseInfo) []idle.LeaseInfo {
	other := make([]idle.LeaseInfo, 0, len(leases))
	for _, lease := range leases {
		if lease.Workspace == "" || lease.Workspace == currentWorkspace {
			continue
		}
		other = append(other, lease)
	}
	return other
}

func cleanupLocalWorkspace(ctx context.Context, cfg *config.Config) error {
	st, err := state.Load(cfg.Name)
	if err != nil {
		return err
	}

	step("Cleaning up local Mutagen sessions...")

	mutagenPath, err := deps.MutagenPath()
	if err != nil {
		return err
	}

	sshCfg := &provider.SSHConfig{
		Host:         st.SSH.Host,
		IP:           st.Instance.LastKnownIP,
		Port:         st.SSH.Port,
		User:         st.SSH.User,
		IdentityFile: st.SSH.IdentityFile,
	}
	syncMgr := mutagensync.New(cfg, sshCfg, mutagenPath, shell.DefaultCommander)

	forwardPorts, reversePorts, err := portsForSessionCleanup(cfg, st)
	if err != nil {
		return err
	}
	syncMgr.TerminateAllSessions(ctx, forwardPorts, reversePorts)
	for _, profileState := range st.Profiles {
		if profileState.SessionName == "" {
			continue
		}
		if err := mutagensync.TerminateSyncSession(ctx, mutagenPath, shell.DefaultCommander, profileState.SessionName); err != nil {
			shell.Debugf("terminate profile sync %s: %v", profileState.Name, err)
		}
	}
	return nil
}
