package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mutapod/mutapod/internal/agents"
	"github.com/mutapod/mutapod/internal/bootstrap"
	"github.com/mutapod/mutapod/internal/compose"
	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/deps"
	"github.com/mutapod/mutapod/internal/dockerctx"
	"github.com/mutapod/mutapod/internal/ignore"
	"github.com/mutapod/mutapod/internal/profiles"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
	"github.com/mutapod/mutapod/internal/state"
	mutagensync "github.com/mutapod/mutapod/internal/sync"
	"github.com/mutapod/mutapod/internal/vscode"
)

func upCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up [local|container]",
		Short: "Provision VM, sync files, and start services",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runUp,
	}
	cmd.Flags().Bool("build", false, "force docker compose to rebuild images before starting services")
	return cmd
}

func runUp(cmd *cobra.Command, args []string) error {
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
	return runUpWithConfig(ctx, cfg, launchMode, buildImages)
}

func runUpWithConfig(ctx context.Context, cfg *config.Config, launchMode vscode.LaunchMode, buildImages bool) error {
	step("Loaded config: %s (%s)", cfg.Name, cfg.Provider.Type)

	if err := confirmMissingIgnoreFile(os.Stdin, os.Stdout, cfg); err != nil {
		return err
	}

	step("Updating AGENTS.md...")
	agentsPath, err := agents.Ensure(cfg)
	if err != nil {
		return err
	}
	ok("AGENTS.md ready: %s", agentsPath)

	step("Checking local dependencies...")
	mutagenPath, err := deps.MutagenPath()
	if err != nil {
		return fmt.Errorf("deps: %w", err)
	}
	shell.Debugf("mutagen: %s", mutagenPath)

	st, err := state.Load(cfg.Name)
	if err != nil {
		return err
	}

	prov, err := provider.New(cfg, shell.DefaultCommander)
	if err != nil {
		return err
	}

	step("Ensuring VM is running...")
	instanceState, err := prov.EnsureInstance(ctx)
	if err != nil {
		return err
	}
	ok("VM running: %s (%s)", cfg.InstanceName(), instanceState)

	step("Configuring SSH access...")
	sshCfg, err := prov.SSHConfig(ctx)
	if err != nil {
		return err
	}
	ok("SSH host: %s", sshCfg.Host)

	idleRefresher, err := maybeConfigureIdleLease(ctx, cfg, prov, sshCfg)
	if err != nil {
		return err
	}
	defer func() {
		if idleRefresher != nil {
			idleRefresher.Stop()
		}
	}()

	ipChanged := st.Instance.LastKnownIP != "" && st.Instance.LastKnownIP != sshCfg.IP

	activeProfiles, err := profiles.Active(cfg)
	if err != nil {
		return err
	}

	step("Bootstrapping VM (docker, docker compose)...")
	if err := bootstrap.Run(ctx, prov); err != nil {
		return err
	}
	ok("Bootstrap complete")

	step("Preparing remote workspace...")
	if err := ensureRemoteWorkspace(ctx, prov, cfg.WorkspacePath(), sshCfg.User); err != nil {
		return err
	}
	ok("Remote workspace ready: %s", cfg.WorkspacePath())

	if len(activeProfiles) > 0 {
		step("Preparing personal AI profile directories...")
		if err := ensureRemoteProfilePaths(ctx, prov, activeProfiles, sshCfg.User); err != nil {
			return err
		}
		ok("Personal AI profiles ready: %s", strings.Join(profileNames(activeProfiles), ", "))
	}

	step("Starting Mutagen daemon...")
	if err := mutagensync.DaemonStart(ctx, mutagenPath, shell.DefaultCommander); err != nil {
		shell.Debugf("mutagen daemon start: %v (may already be running)", err)
	}

	step("Starting file sync...")
	syncMgr := mutagensync.New(cfg, sshCfg, mutagenPath, shell.DefaultCommander)

	ignorePatterns, err := ignore.Load(cfg.Dir)
	if err != nil {
		return fmt.Errorf("sync: load ignore patterns: %w", err)
	}
	ignoreSignature := ignorePatterns.Signature()
	sessionConfigSignature, err := syncMgr.SessionConfigSignature(ctx)
	if err != nil {
		return err
	}

	if ipChanged {
		forwardPorts, reversePorts, err := portsForSessionCleanup(cfg, st)
		if err != nil {
			return err
		}
		shell.Debugf("IP changed (%s -> %s), recreating Mutagen sessions", st.Instance.LastKnownIP, sshCfg.IP)
		syncMgr.TerminateAllSessions(ctx, forwardPorts, reversePorts)
		for _, profileState := range st.Profiles {
			if profileState.SessionName == "" {
				continue
			}
			_ = mutagensync.TerminateSyncSession(ctx, mutagenPath, shell.DefaultCommander, profileState.SessionName)
		}
	}
	if st.Sync.IgnoreSignature != "" && st.Sync.IgnoreSignature != ignoreSignature {
		shell.Debugf("ignore rules changed, recreating Mutagen sync session")
		if err := syncMgr.TerminateSync(ctx); err != nil {
			shell.Debugf("terminate sync for ignore refresh: %v", err)
		}
	} else if st.Sync.IgnoreSignature == "" && st.Sync.SessionName != "" {
		shell.Debugf("ignore signature missing from state, recreating Mutagen sync session once")
		if err := syncMgr.TerminateSync(ctx); err != nil {
			shell.Debugf("terminate sync for ignore refresh: %v", err)
		}
	} else if st.Sync.SessionConfig != "" && st.Sync.SessionConfig != sessionConfigSignature {
		shell.Debugf("sync session settings changed, recreating Mutagen sync session")
		if err := syncMgr.TerminateSync(ctx); err != nil {
			shell.Debugf("terminate sync for config refresh: %v", err)
		}
	} else if st.Sync.SessionConfig == "" && st.Sync.SessionName != "" {
		shell.Debugf("sync session config signature missing from state, recreating Mutagen sync session once")
		if err := syncMgr.TerminateSync(ctx); err != nil {
			shell.Debugf("terminate sync for config refresh: %v", err)
		}
	}

	if err := syncMgr.EnsureSync(ctx); err != nil {
		return err
	}
	localPath, err := cfg.LocalSyncPath()
	if err != nil {
		return err
	}
	ok("Sync active: %s -> %s:%s", localPath, sshCfg.Host, cfg.WorkspacePath())

	step("Waiting for initial sync...")
	if err := waitForInitialSync(ctx, prov, syncMgr, cfg); err != nil {
		return err
	}
	ok("Initial sync complete")

	profileStates := make([]state.ProfileSyncState, 0, len(activeProfiles))
	if len(activeProfiles) > 0 {
		step("Syncing personal AI profiles...")
		existingProfileState := make(map[string]state.ProfileSyncState, len(st.Profiles))
		for _, profileState := range st.Profiles {
			existingProfileState[profileState.Name] = profileState
		}
		activeProfileSet := make(map[string]bool, len(activeProfiles))
		for _, name := range profileStateKeys(activeProfiles) {
			activeProfileSet[name] = true
		}
		for _, profileState := range st.Profiles {
			if activeProfileSet[profileState.Name] {
				continue
			}
			if profileState.SessionName == "" {
				continue
			}
			if err := mutagensync.TerminateSyncSession(ctx, mutagenPath, shell.DefaultCommander, profileState.SessionName); err != nil {
				shell.Debugf("terminate stale profile sync %s: %v", profileState.Name, err)
			}
		}
		for _, spec := range activeProfiles {
			if spec.SessionName == "" || spec.LocalPath == "" || spec.SyncRemotePath == "" {
				if prior, ok := existingProfileState[spec.Name]; ok && prior.SessionName != "" {
					if err := mutagensync.TerminateSyncSession(ctx, mutagenPath, shell.DefaultCommander, prior.SessionName); err != nil {
						shell.Debugf("terminate profile sync for no-local-state refresh: %v", err)
					}
				}
				profileStates = append(profileStates, state.ProfileSyncState{
					Name:       spec.Name,
					LocalPath:  spec.LocalPath,
					RemotePath: spec.SyncRemotePath,
				})
				continue
			}
			session := mutagensync.NewSidecar(mutagensync.SidecarSpec{
				SessionName:    spec.SessionName,
				Label:          "mutapod-name=" + cfg.Name + "-profile-" + spec.Name,
				LocalPath:      spec.LocalPath,
				RemotePath:     spec.SyncRemotePath,
				Mode:           cfg.Sync.Mode,
				IgnorePatterns: spec.IgnorePatterns,
			}, sshCfg, mutagenPath, shell.DefaultCommander)
			signature := session.ConfigSignature()
			refreshed := false
			if prior, ok := existingProfileState[spec.Name]; shouldRefreshProfileSession(prior, ok, signature) {
				refreshed = true
				if ok {
					shell.Debugf("profile %s sync settings changed, recreating Mutagen session", spec.Name)
				} else {
					shell.Debugf("profile %s has no saved sync state, recreating Mutagen session once", spec.Name)
				}
				sessionName := spec.SessionName
				if ok && prior.SessionName != "" {
					sessionName = prior.SessionName
				}
				if err := mutagensync.TerminateSyncSession(ctx, mutagenPath, shell.DefaultCommander, sessionName); err != nil {
					shell.Debugf("terminate profile sync for refresh: %v", err)
				}
			}
			if err := session.Ensure(ctx); err != nil {
				return err
			}
			if err := session.Flush(ctx); err != nil {
				shell.Debugf("profile %s sync flush: %v", spec.Name, err)
			}
			if err := session.VerifyReady(ctx); err != nil {
				return err
			}
			if spec.Name == "codex" && refreshed {
				if err := cleanupRemoteCodexRuntimeSQLite(ctx, prov, spec.SyncRemotePath); err != nil {
					return fmt.Errorf("profile codex runtime SQLite cleanup: %w", err)
				}
			}
			profileStates = append(profileStates, state.ProfileSyncState{
				Name:          spec.Name,
				SessionName:   spec.SessionName,
				LocalPath:     spec.LocalPath,
				RemotePath:    spec.SyncRemotePath,
				SessionConfig: signature,
			})
			for _, extra := range spec.SupplementalSyncs {
				extraSession := mutagensync.NewSidecar(mutagensync.SidecarSpec{
					SessionName:    extra.SessionName,
					Label:          "mutapod-name=" + cfg.Name + "-profile-" + extra.Name,
					LocalPath:      extra.LocalPath,
					RemotePath:     extra.RemotePath,
					Mode:           cfg.Sync.Mode,
					IgnorePatterns: extra.IgnorePatterns,
				}, sshCfg, mutagenPath, shell.DefaultCommander)
				extraSignature := extraSession.ConfigSignature()
				if prior, ok := existingProfileState[extra.Name]; shouldRefreshProfileSession(prior, ok, extraSignature) {
					if ok {
						shell.Debugf("profile %s sync settings changed, recreating Mutagen session", extra.Name)
					} else {
						shell.Debugf("profile %s has no saved sync state, recreating Mutagen session once", extra.Name)
					}
					sessionName := extra.SessionName
					if ok && prior.SessionName != "" {
						sessionName = prior.SessionName
					}
					if err := mutagensync.TerminateSyncSession(ctx, mutagenPath, shell.DefaultCommander, sessionName); err != nil {
						shell.Debugf("terminate profile sync for refresh: %v", err)
					}
				}
				if err := extraSession.Ensure(ctx); err != nil {
					return err
				}
				if err := extraSession.Flush(ctx); err != nil {
					shell.Debugf("profile %s sync flush: %v", extra.Name, err)
				}
				if err := extraSession.VerifyReady(ctx); err != nil {
					return err
				}
				profileStates = append(profileStates, state.ProfileSyncState{
					Name:          extra.Name,
					SessionName:   extra.SessionName,
					LocalPath:     extra.LocalPath,
					RemotePath:    extra.RemotePath,
					SessionConfig: extraSignature,
				})
			}
		}
		ok("Personal AI profiles synced: %s", strings.Join(profileNames(activeProfiles), ", "))
	}

	if err := removeRemoteWorkspaceWrapper(ctx, prov, cfg); err != nil {
		shell.Debugf("remove remote workspace wrapper: %v", err)
	}

	if len(cfg.Compose.ReverseForwards) > 0 {
		step("Exposing local services to the remote VM: %v...", cfg.Compose.ReverseForwards)
		for _, port := range cfg.Compose.ReverseForwards {
			if err := syncMgr.EnsureReverseForward(ctx, port); err != nil {
				return fmt.Errorf("reverse forward %d: %w", port, err)
			}
		}
		ok("Local services exposed: %v", cfg.Compose.ReverseForwards)
	}

	step("Preparing compose overrides...")
	overrideApplied, err := compose.EnsureRemoteOverride(ctx, prov, cfg, activeProfiles)
	if err != nil {
		return err
	}
	if overrideApplied {
		ok("Compose override ready for service %s", cfg.Compose.PrimaryService)
	} else {
		ok("Compose overrides ready")
	}

	step("Starting services (docker compose up)...")
	if err := compose.Up(ctx, prov, cfg, buildImages); err != nil {
		return err
	}
	ok("Services running")

	if cfg.Compose.PrimaryService != "" {
		step("Protecting workspace sync permissions...")
		if err := ensureAttachedContainerWorkspaceACLs(ctx, prov, cfg, activeProfiles); err != nil {
			return err
		}
		ok("Workspace sync permissions protected")

		step("Configuring git safe directory in the main container...")
		if err := compose.ConfigureGitSafeDirectory(ctx, prov, cfg, activeProfiles); err != nil {
			shell.Debugf("git safe.directory: %v", err)
			fmt.Fprintf(os.Stderr, "  warning: could not configure git safe.directory in container: %v\n", err)
		} else {
			ok("Git safe directory configured")
		}
	}

	if len(activeProfiles) > 0 && cfg.Compose.PrimaryService != "" {
		step("Configuring personal AI tools in the main container...")
		if err := profiles.EnsureRemoteTools(ctx, composeProfileRunner{prov: prov}, cfg, activeProfiles); err != nil {
			return err
		}
		ok("Personal AI tools ready: %s", strings.Join(profileNames(activeProfiles), ", "))
	}

	var ports []int
	composePath, err := compose.DetectFile(cfg)
	if err != nil {
		shell.Debugf("compose file not found, skipping port forwarding: %v", err)
	} else {
		ports, err = compose.ParsePorts(composePath, cfg.Compose.ExtraPorts)
		if err != nil {
			return fmt.Errorf("port config: %w", err)
		}
		if len(ports) > 0 {
			step("Forwarding ports: %v...", ports)
			for _, p := range ports {
				if err := syncMgr.EnsureForward(ctx, p); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: port %d forward failed: %v\n", p, err)
				}
			}
			ok("Ports forwarded: %v", ports)
		}
	}

	st.Name = cfg.Name
	st.ProviderType = cfg.Provider.Type
	st.Instance.Name = cfg.InstanceName()
	st.Instance.LastKnownIP = sshCfg.IP
	st.Instance.Status = string(instanceState)
	st.SSH = state.SSHState{
		Host:         sshCfg.Host,
		Port:         sshCfg.Port,
		User:         sshCfg.User,
		IdentityFile: sshCfg.IdentityFile,
	}
	st.Sync = state.SyncState{
		Backend:                "mutagen",
		SessionName:            syncMgr.SessionName(),
		LocalPath:              localPath,
		RemotePath:             cfg.WorkspacePath(),
		SessionConfig:          sessionConfigSignature,
		IgnoreSignature:        ignoreSignature,
		ForwardSessions:        buildForwardSessionMap(syncMgr, ports),
		ReverseForwardSessions: buildReverseForwardSessionMap(syncMgr, cfg.Compose.ReverseForwards),
	}
	st.Profiles = profileStates
	if err := state.Save(st); err != nil {
		shell.Debugf("warning: save state: %v", err)
	}

	step("Configuring local Docker context...")
	dockerContext, err := dockerctx.EnsureContext(ctx, cfg, sshCfg, shell.DefaultCommander)
	if err != nil {
		return err
	}
	ok("Docker context configured: %s", dockerContext)

	step("Configuring local VS Code workspace...")
	workspaceFile, err := vscode.ConfigureWorkspace(cfg, sshCfg, dockerContext)
	if err != nil {
		return err
	}
	ok("VS Code workspace configured: %s", workspaceFile)

	attachedConfigPath, err := vscode.ConfigureAttachedContainer(ctx, cfg, dockerContext, activeProfiles, shell.DefaultCommander)
	if err != nil {
		return err
	}
	if attachedConfigPath != "" {
		ok("Attached-container defaults configured: %s", attachedConfigPath)
	}

	if err := maybeStartIdleHeartbeat(cfg); err != nil {
		return err
	}
	idleRefresher.Stop()
	idleRefresher = nil

	vscode.PrintInstructions(cfg, sshCfg, ports)
	step("Opening VS Code (%s)...", launchMode)
	if err := vscode.Launch(ctx, cfg, dockerContext, launchMode, shell.DefaultCommander); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: VS Code launch failed: %v\n", err)
	} else {
		ok("VS Code opened (%s)", launchMode)
	}
	return nil
}

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop services, pause sync, and stop the VM",
		RunE:  runDown,
	}
}

func runDown(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	st, err := state.Load(cfg.Name)
	if err != nil {
		return err
	}

	mutagenPath, err := deps.MutagenPath()
	if err != nil {
		return err
	}

	sshCfg := &provider.SSHConfig{
		Host: st.SSH.Host,
		Port: st.SSH.Port,
		User: st.SSH.User,
	}
	syncMgr := mutagensync.New(cfg, sshCfg, mutagenPath, shell.DefaultCommander)

	prov, err := provider.New(cfg, shell.DefaultCommander)
	if err != nil {
		return err
	}
	if _, err := prov.SSHConfig(ctx); err != nil {
		shell.Debugf("ssh config for compose down: %v", err)
	}

	step("Stopping services (docker compose down)...")
	if err := compose.Down(ctx, prov, cfg); err != nil {
		shell.Debugf("compose down: %v", err)
	}

	step("Pausing file sync...")
	if err := syncMgr.PauseSync(ctx); err != nil {
		shell.Debugf("pause sync: %v", err)
	}
	for _, profileState := range st.Profiles {
		if profileState.SessionName == "" {
			continue
		}
		if err := mutagensync.PauseSyncSession(ctx, mutagenPath, shell.DefaultCommander, profileState.SessionName); err != nil {
			shell.Debugf("pause profile sync %s: %v", profileState.Name, err)
		}
	}

	forwardPorts, reversePorts, _ := portsForSessionCleanup(cfg, st)
	if len(forwardPorts) > 0 {
		step("Pausing port forwards...")
		syncMgr.PauseAllForwards(ctx, forwardPorts)
	}
	if len(reversePorts) > 0 {
		step("Pausing reverse forwards...")
		syncMgr.PauseAllReverseForwards(ctx, reversePorts)
	}

	step("Stopping VM...")
	if err := maybeHandleIdleDown(ctx, cfg, prov); err != nil {
		return err
	}
	if cfg.Idle.IsEnabled() {
		ok("Lease released for %s; VM stops immediately if unused, otherwise after idle timeout", cfg.InstanceName())
	} else {
		ok("VM stopped: %s", cfg.InstanceName())
	}

	st.Instance.Status = string(provider.StateStopped)
	_ = state.Save(st)
	return nil
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current state of the workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			st, err := state.Load(cfg.Name)
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
			fmt.Printf("Workspace:  %s\n", cfg.Name)
			fmt.Printf("Provider:   %s\n", cfg.Provider.Type)
			fmt.Printf("VM:         %s (%s)\n", cfg.InstanceName(), instanceState)
			if st.SSH.Host != "" {
				fmt.Printf("SSH host:   %s\n", st.SSH.Host)
			}
			if st.Sync.SessionName != "" {
				fmt.Printf("Sync:       %s\n", st.Sync.SessionName)
			}
			return nil
		},
	}
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh",
		Short: "Open a shell on the remote VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			prov, err := provider.New(cfg, shell.DefaultCommander)
			if err != nil {
				return err
			}
			if _, err := prov.SSHConfig(ctx); err != nil {
				return err
			}
			return prov.Exec(ctx, []string{}, provider.ExecOptions{
				Stdin:  os.Stdin,
				Stdout: os.Stdout,
				Stderr: os.Stderr,
				Tty:    true,
			})
		},
	}
}

func step(format string, args ...any) {
	fmt.Printf("-> "+format+"\n", args...)
}

func ok(format string, args ...any) {
	fmt.Printf("OK "+format+"\n", args...)
}

func collectPorts(sessions map[string]string) []int {
	var ports []int
	for k := range sessions {
		var p int
		fmt.Sscanf(k, "%d", &p)
		if p > 0 {
			ports = append(ports, p)
		}
	}
	return ports
}

func portsForSessionCleanup(cfg *config.Config, st *state.State) ([]int, []int, error) {
	forwardPorts := collectPorts(st.Sync.ForwardSessions)
	if len(forwardPorts) == 0 {
		composePath, err := compose.DetectFile(cfg)
		if err != nil {
			forwardPorts = nil
		} else {
			forwardPorts, err = compose.ParsePorts(composePath, cfg.Compose.ExtraPorts)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	reversePorts := collectPorts(st.Sync.ReverseForwardSessions)
	if len(reversePorts) == 0 {
		reversePorts = append(reversePorts, cfg.Compose.ReverseForwards...)
	}
	return forwardPorts, reversePorts, nil
}

func buildForwardSessionMap(syncMgr *mutagensync.Manager, ports []int) map[string]string {
	if len(ports) == 0 {
		return nil
	}

	forwardSessions := make(map[string]string, len(ports))
	for _, port := range ports {
		forwardSessions[fmt.Sprintf("%d", port)] = syncMgr.ForwardSessionName(port)
	}
	return forwardSessions
}

func buildReverseForwardSessionMap(syncMgr *mutagensync.Manager, ports []int) map[string]string {
	if len(ports) == 0 {
		return nil
	}

	forwardSessions := make(map[string]string, len(ports))
	for _, port := range ports {
		forwardSessions[fmt.Sprintf("%d", port)] = syncMgr.ReverseForwardSessionName(port)
	}
	return forwardSessions
}

func ensureRemoteWorkspace(ctx context.Context, prov provider.Provider, workspacePath, user string) error {
	cmd := fmt.Sprintf(
		"sudo usermod -aG docker %s && sudo mkdir -p %s && sudo chown -R %s %s",
		shellQuote(user),
		shellQuote(workspacePath),
		shellQuote(user+":"+user),
		shellQuote(workspacePath),
	)
	return prov.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{})
}

func ensureRemoteProfilePaths(ctx context.Context, prov provider.Provider, activeProfiles []profiles.Spec, user string) error {
	if len(activeProfiles) == 0 {
		return nil
	}

	parts := []string{fmt.Sprintf("sudo usermod -aG docker %s", shellQuote(user))}
	for _, profile := range activeProfiles {
		for _, remotePath := range profile.RemoteDirectories() {
			parts = append(parts,
				fmt.Sprintf("sudo mkdir -p %s", shellQuote(remotePath)),
				fmt.Sprintf("sudo chown -R %s %s", shellQuote(user+":"+user), shellQuote(remotePath)),
			)
		}
	}
	cmd := strings.Join(parts, " && ")
	return prov.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{})
}

type composeProfileRunner struct {
	prov provider.Provider
}

func (r composeProfileRunner) RunProfileSetup(ctx context.Context, cfg *config.Config, active []profiles.Spec, spec profiles.Spec) error {
	return compose.ExecInPrimaryService(ctx, r.prov, cfg, active, spec.SetupScript())
}

func profileNames(activeProfiles []profiles.Spec) []string {
	names := make([]string, 0, len(activeProfiles))
	for _, profile := range activeProfiles {
		names = append(names, profile.Name)
	}
	return names
}

func profileStateKeys(activeProfiles []profiles.Spec) []string {
	keys := make([]string, 0, len(activeProfiles))
	for _, profile := range activeProfiles {
		keys = append(keys, profile.Name)
		for _, extra := range profile.SupplementalSyncs {
			keys = append(keys, extra.Name)
		}
	}
	return keys
}

func shouldRefreshProfileSession(prior state.ProfileSyncState, found bool, signature string) bool {
	if !found {
		return true
	}
	return prior.SessionConfig == "" || prior.SessionConfig != signature
}

func cleanupRemoteCodexRuntimeSQLite(ctx context.Context, prov provider.Provider, remotePath string) error {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return nil
	}
	return prov.Exec(ctx, []string{"bash", "-c", codexRuntimeSQLiteCleanupCommand(remotePath)}, provider.ExecOptions{})
}

func codexRuntimeSQLiteCleanupCommand(remotePath string) string {
	patterns := []string{
		"goals_*.sqlite",
		"goals_*.sqlite-shm",
		"goals_*.sqlite-wal",
		"logs_*.sqlite",
		"logs_*.sqlite-shm",
		"logs_*.sqlite-wal",
		"state_*.sqlite",
		"state_*.sqlite-shm",
		"state_*.sqlite-wal",
	}
	patternArgs := strings.Join(patterns, " ")
	return fmt.Sprintf(`set -eu
profile=%s
if [ ! -d "$profile" ]; then
  exit 0
fi
patterns=%s
found=0
for pattern in $patterns; do
  for f in "$profile"/$pattern; do
    if [ -e "$f" ]; then
      found=1
      break
    fi
  done
  if [ "$found" -eq 1 ]; then
    break
  fi
done
if [ "$found" -eq 0 ]; then
  exit 0
fi
backup_root=/var/lib/mutapod/profile-backups/codex-runtime-sqlite
backup="$backup_root/$(date -u +%%Y%%m%%dT%%H%%M%%SZ)"
sudo mkdir -p "$backup"
for pattern in $patterns; do
  for f in "$profile"/$pattern; do
    if [ -e "$f" ]; then
      sudo mv "$f" "$backup"/
    fi
  done
done
`, shellQuote(remotePath), shellQuote(patternArgs))
}

func ensureAttachedContainerWorkspaceACLs(ctx context.Context, prov provider.Provider, cfg *config.Config, activeProfiles []profiles.Spec) error {
	workspaceFolder, err := resolveAttachedWorkspaceFolder(cfg)
	if err != nil {
		return err
	}
	script := buildWorkspaceACLScript(workspaceFolder)
	return compose.ExecInPrimaryService(ctx, prov, cfg, activeProfiles, script)
}

func resolveAttachedWorkspaceFolder(cfg *config.Config) (string, error) {
	workspaceFolder := cfg.Compose.WorkspaceFolder
	if workspaceFolder == "" {
		detected, err := compose.DetectWorkspaceFolder(cfg, cfg.Compose.PrimaryService)
		if err != nil {
			return "", fmt.Errorf("detect workspace folder: %w", err)
		}
		workspaceFolder = detected
	}
	if workspaceFolder == "" {
		return "", fmt.Errorf("attached container user requires a workspace folder")
	}
	return workspaceFolder, nil
}

func buildWorkspaceACLScript(workspaceFolder string) string {
	return fmt.Sprintf(`set -eu
workspace=%s
uid=$(stat -c %%u "$workspace")
repair_debian_packages() {
  if command -v dpkg >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive dpkg --configure -a >/dev/null || true
  fi
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -f -y -qq >/dev/null
  fi
  if command -v dpkg >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive dpkg --configure -a >/dev/null
  fi
}
ensure_acl_tools() {
  if command -v setfacl >/dev/null 2>&1; then
    return 0
  fi
  if command -v apt-get >/dev/null 2>&1; then
    repair_debian_packages
    apt-get update -qq >/dev/null
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq acl >/dev/null
    return 0
  fi
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache acl >/dev/null
    return 0
  fi
  if command -v dnf >/dev/null 2>&1; then
    dnf install -y acl >/dev/null
    return 0
  fi
  if command -v yum >/dev/null 2>&1; then
    yum install -y acl >/dev/null
    return 0
  fi
  echo "mutapod: could not install ACL tooling in this container" >&2
  exit 1
}
ensure_acl_tools
apply_workspace_acls() {
  setfacl -m "u:${uid}:rwX" "$workspace" 2>/dev/null || true
  setfacl -m "d:u:${uid}:rwX" "$workspace" 2>/dev/null || true
  find "$workspace" -uid 0 -exec setfacl -m "u:${uid}:rwX" {} + 2>/dev/null || true
  find "$workspace" -uid 0 -type d -exec setfacl -m "d:u:${uid}:rwX" {} + 2>/dev/null || true
}
apply_workspace_acls
cat > /tmp/mutapod-acl-watch.sh <<EOF
#!/bin/sh
set -eu
workspace=%s
uid=$uid
apply_workspace_acls() {
  setfacl -m "u:\${uid}:rwX" "\$workspace" 2>/dev/null || true
  setfacl -m "d:u:\${uid}:rwX" "\$workspace" 2>/dev/null || true
  find "\$workspace" -uid 0 -exec setfacl -m "u:\${uid}:rwX" {} + 2>/dev/null || true
  find "\$workspace" -uid 0 -type d -exec setfacl -m "d:u:\${uid}:rwX" {} + 2>/dev/null || true
}
while :; do
  apply_workspace_acls
  sleep 2
done
EOF
chmod 0755 /tmp/mutapod-acl-watch.sh
if [ -f /tmp/mutapod-acl-watch.pid ]; then
  old_pid=$(cat /tmp/mutapod-acl-watch.pid 2>/dev/null || true)
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
    kill "$old_pid" 2>/dev/null || true
  fi
fi
nohup /tmp/mutapod-acl-watch.sh >/tmp/mutapod-acl-watch.log 2>&1 &
echo $! >/tmp/mutapod-acl-watch.pid`,
		shellQuote(workspaceFolder),
		shellQuote(workspaceFolder),
	)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func waitForInitialSync(ctx context.Context, prov provider.Provider, syncMgr *mutagensync.Manager, cfg *config.Config) error {
	if err := syncMgr.FlushSyncWithProgress(ctx, os.Stdout); err != nil {
		shell.Debugf("sync flush: %v", err)
	}
	if err := syncMgr.VerifySyncReady(ctx); err != nil {
		return err
	}

	remoteComposePath, err := compose.RemoteComposePath(cfg)
	if err != nil {
		shell.Debugf("remote compose path: %v", err)
		return nil
	}

	deadline := time.Now().Add(45 * time.Second)
	for {
		err := remotePathExists(ctx, prov, remoteComposePath)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("initial sync did not place %s on the remote host: %w", remoteComposePath, err)
		}
		shell.Debugf("waiting for remote file %s: %v", remoteComposePath, err)
		time.Sleep(2 * time.Second)
	}
}

func remotePathExists(ctx context.Context, prov provider.Provider, remotePath string) error {
	cmd := fmt.Sprintf("test -f %s", shellQuote(remotePath))
	return prov.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{})
}

func removeRemoteWorkspaceWrapper(ctx context.Context, prov provider.Provider, cfg *config.Config) error {
	remotePath := strings.TrimSuffix(cfg.WorkspacePath(), "/") + "/" + vscode.WorkspaceFilename()
	cmd := fmt.Sprintf("rm -f %s", shellQuote(remotePath))
	return prov.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{})
}

func loadConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.LoadFile(cfgFile)
	}
	cwd, err := currentDir()
	if err != nil {
		return nil, err
	}
	return config.Load(cwd)
}

func confirmMissingIgnoreFile(in io.Reader, out io.Writer, cfg *config.Config) error {
	path := filepath.Join(cfg.Dir, ignore.Filename)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check %s: %w", ignore.Filename, err)
	}

	fmt.Fprintf(out, "Warning: %s was not found in %s.\n", ignore.Filename, cfg.Dir)
	fmt.Fprintln(out, "mutapod will continue with only its built-in minimal ignores, which can cause large uploads.")
	fmt.Fprint(out, "Continue without .mutapodignore? [y/N]: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return nil
	}
	return fmt.Errorf("up cancelled because %s is missing", ignore.Filename)
}

func parseUpLaunchMode(args []string) (vscode.LaunchMode, error) {
	if len(args) == 0 {
		return vscode.LaunchAttached, nil
	}

	switch args[0] {
	case string(vscode.LaunchLocal):
		return vscode.LaunchLocal, nil
	case string(vscode.LaunchAttached):
		return vscode.LaunchAttached, nil
	default:
		return "", fmt.Errorf("up: unsupported mode %q (expected: local or container)", args[0])
	}
}
