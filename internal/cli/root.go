package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/mutapod/mutapod/internal/buildinfo"
	"github.com/mutapod/mutapod/internal/shell"
)

var (
	cfgFile          string
	debug            bool
	providerOverride string
)

// Root returns the root cobra command.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "mutapod",
		Short: "Cloud dev environment: provision VM, sync files, run devcontainer",
		Long: `mutapod provisions a cloud VM on GCP, syncs your local project
to it via Mutagen, starts a devcontainer, and forwards ports — all with one command.`,
		SilenceUsage: true,
		Version:      buildinfo.DisplayVersion(),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			shell.SetDebug(debug)
			maybeCheckForUpdate(cmd)
		},
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "path to mutapod.yaml (default: search up from cwd)")
	root.PersistentFlags().StringVar(&providerOverride, "provider", "", "active provider for this run (default: provider.type in mutapod.yaml)")
	root.PersistentFlags().BoolVar(&debug, "debug", false, "enable verbose debug output")

	root.AddCommand(upCmd())
	root.AddCommand(resetCmd())
	root.AddCommand(downCmd())
	root.AddCommand(destroyCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(sshCmd())
	root.AddCommand(leasesCmd())
	root.AddCommand(idleHeartbeatCmd())
	root.AddCommand(versionCmd())
	root.AddCommand(updateCmd())

	return root
}

// Execute runs the root command.
func Execute() {
	if err := Root().Execute(); err != nil {
		os.Exit(1)
	}
}
