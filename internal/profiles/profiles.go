package profiles

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mutapod/mutapod/internal/config"
)

var (
	userHomeDir = os.UserHomeDir
	lookPath    = exec.LookPath
	retryDelay  = 3 * time.Second
)

const RootHomeDir = "/root"

// Mount describes a remote VM path that should be bind-mounted into the
// primary service container.
type Mount struct {
	RemotePath    string
	ContainerPath string
}

// Spec describes one automatically managed personal profile.
type Spec struct {
	Name              string
	SessionName       string
	LocalPath         string
	SyncRemotePath    string
	ToolRemotePath    string
	Mounts            []Mount
	SupplementalSyncs []SupplementalSync
	IgnorePatterns    []string
	LocalBinaryPath   string

	def Definition
}

// SupplementalSync describes an extra Mutagen-backed sync root that belongs to
// a profile but isn't naturally part of its main home directory.
type SupplementalSync struct {
	Name           string
	SessionName    string
	LocalPath      string
	RemotePath     string
	IgnorePatterns []string
}

// Definition is the pluggable contract for a personal profile integration.
type Definition interface {
	Name() string
	Detect(cfg *config.Config) (Spec, bool, error)
	SetupScript(spec Spec) string
	AttachedContainerSettings(spec Spec) map[string]any
	AttachedContainerRemoteEnv(spec Spec) map[string]string
}

// Active returns the detected personal profiles that should be enabled for the
// current workspace.
func Active(cfg *config.Config) ([]Spec, error) {
	specs := make([]Spec, 0, len(definitions))
	for _, definition := range definitions {
		spec, ok, err := definition.Detect(cfg)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if spec.Name == "claude" {
			if err := enrichClaudeSupplementalSyncs(cfg, &spec); err != nil {
				return nil, fmt.Errorf("profiles: %s: %w", spec.Name, err)
			}
		}
		spec.def = definition
		specs = append(specs, spec)
	}
	return specs, nil
}

// SetupScript renders the remote in-container setup script for a detected
// profile.
func (s Spec) SetupScript() string {
	if s.def == nil {
		return ""
	}
	return s.def.SetupScript(s)
}

// AttachedContainerSettings returns VS Code attached-container settings that
// should be applied for this profile.
func (s Spec) AttachedContainerSettings() map[string]any {
	if s.def == nil {
		return nil
	}
	return s.def.AttachedContainerSettings(s)
}

// AttachedContainerRemoteEnv returns remoteEnv entries that should be applied
// to the VS Code attached-container environment for this profile.
func (s Spec) AttachedContainerRemoteEnv() map[string]string {
	if s.def == nil {
		return nil
	}
	return s.def.AttachedContainerRemoteEnv(s)
}

// RemoteDirectories returns every VM directory mutapod should create for this
// profile.
func (s Spec) RemoteDirectories() []string {
	dirs := make([]string, 0, 2)
	if s.SyncRemotePath != "" {
		dirs = append(dirs, s.SyncRemotePath)
	}
	if s.ToolRemotePath != "" {
		dirs = append(dirs, s.ToolRemotePath)
	}
	for _, extra := range s.SupplementalSyncs {
		if extra.RemotePath != "" {
			dirs = append(dirs, extra.RemotePath)
		}
	}
	return uniqueStrings(dirs)
}

var definitions = []Definition{
	newCodexDefinition(),
	newClaudeDefinition(),
}

type nodeProfileDefinition struct {
	name                   string
	binaryName             string
	packageName            string
	defaultLocalHome       string
	defaultSyncRemotePath  string
	defaultToolRemotePath  string
	defaultConfigMountPath string
	defaultToolMountPath   string
	ignorePatterns         []string
	configSelector         func(*config.Config) config.ProfileSyncConfig
	binaryFallback         func() (string, bool)
	setupScriptBuilder     func(spec Spec, pkg string) string
	settingsBuilder        func(spec Spec) map[string]any
	remoteEnvBuilder       func(spec Spec) map[string]string
}

func (d nodeProfileDefinition) Name() string { return d.name }

func (d nodeProfileDefinition) Detect(cfg *config.Config) (Spec, bool, error) {
	profileCfg := d.configSelector(cfg)

	localBinaryPath, binaryInstalled := detectBinary(d.binaryName)
	if !binaryInstalled && d.binaryFallback != nil {
		localBinaryPath, binaryInstalled = d.binaryFallback()
	}
	if profileCfg.Enabled != nil && !*profileCfg.Enabled {
		return Spec{}, false, nil
	}
	if !binaryInstalled && (profileCfg.Enabled == nil || !*profileCfg.Enabled) {
		return Spec{}, false, nil
	}

	localHomePath, homeExists, err := resolveLocalProfilePath(cfg, profileCfg, d.defaultLocalHome)
	if err != nil {
		return Spec{}, false, fmt.Errorf("profiles: %s: %w", d.name, err)
	}
	if !homeExists {
		localHomePath = ""
	}

	syncRemotePath := profileCfg.RemotePath
	if syncRemotePath == "" {
		syncRemotePath = d.defaultSyncRemotePath
	}
	toolRemotePath := filepath.ToSlash(strings.TrimSpace(d.defaultToolRemotePath))
	mountPath := profileCfg.MountPath
	if mountPath == "" {
		mountPath = d.defaultConfigMountPath
	}
	toolMountPath := d.defaultToolMountPath

	spec := Spec{
		Name:            d.name,
		LocalPath:       localHomePath,
		SyncRemotePath:  syncRemotePath,
		ToolRemotePath:  toolRemotePath,
		IgnorePatterns:  append([]string(nil), d.ignorePatterns...),
		LocalBinaryPath: localBinaryPath,
		Mounts: []Mount{
			{RemotePath: syncRemotePath, ContainerPath: mountPath},
			{RemotePath: toolRemotePath, ContainerPath: toolMountPath},
		},
	}
	if localHomePath != "" {
		spec.SessionName = fmt.Sprintf("mutapod-%s-profile-%s", cfg.Name, d.name)
	}
	return spec, true, nil
}

func (d nodeProfileDefinition) SetupScript(spec Spec) string {
	return d.setupScriptBuilder(spec, d.packageName)
}

func (d nodeProfileDefinition) AttachedContainerSettings(spec Spec) map[string]any {
	if d.settingsBuilder == nil {
		return nil
	}
	return d.settingsBuilder(spec)
}

func (d nodeProfileDefinition) AttachedContainerRemoteEnv(spec Spec) map[string]string {
	if d.remoteEnvBuilder == nil {
		return nil
	}
	return d.remoteEnvBuilder(spec)
}

func newCodexDefinition() Definition {
	return nodeProfileDefinition{
		name:                   "codex",
		binaryName:             "codex",
		packageName:            "@openai/codex",
		defaultLocalHome:       ".codex",
		defaultSyncRemotePath:  "/var/lib/mutapod/profiles/codex",
		defaultToolRemotePath:  "/var/lib/mutapod/tools/codex",
		defaultConfigMountPath: RootHomeDir + "/.codex",
		defaultToolMountPath:   "/var/lib/mutapod/tools/codex",
		configSelector: func(cfg *config.Config) config.ProfileSyncConfig {
			return cfg.Profiles.Codex
		},
		binaryFallback: detectCodexFromVSCodeExtension,
		ignorePatterns: []string{
			".sandbox",
			".sandbox/**",
			".sandbox-bin",
			".sandbox-bin/**",
			".sandbox-secrets",
			".sandbox-secrets/**",
			".tmp",
			".tmp/**",
			"tmp",
			"tmp/**",
			"cache",
			"cache/**",
			"goals_*.sqlite",
			"goals_*.sqlite-shm",
			"goals_*.sqlite-wal",
			"logs_*.sqlite",
			"logs_*.sqlite-shm",
			"logs_*.sqlite-wal",
			"state_*.sqlite",
			"state_*.sqlite-shm",
			"state_*.sqlite-wal",
		},
		setupScriptBuilder: func(spec Spec, pkg string) string {
			configMount := spec.Mounts[0].ContainerPath
			toolMount := spec.Mounts[1].ContainerPath
			return nodeProfileSetupScript(nodeProfileSetup{
				PackageName: pkg,
				ToolPrefix:  toolMount,
				BinaryName:  "codex",
				WrapperName: "codex",
				WrapperBody: fmt.Sprintf("export CODEX_HOME=%s\nexec %s/bin/codex \"$@\"", shString(configMount), shString(toolMount)),
			})
		},
		remoteEnvBuilder: func(spec Spec) map[string]string {
			return map[string]string{
				"CODEX_HOME": spec.Mounts[0].ContainerPath,
			}
		},
	}
}

func newClaudeDefinition() Definition {
	return nodeProfileDefinition{
		name:                   "claude",
		binaryName:             "claude",
		packageName:            "@anthropic-ai/claude-code",
		defaultLocalHome:       ".claude",
		defaultSyncRemotePath:  "/var/lib/mutapod/profiles/claude",
		defaultToolRemotePath:  "/var/lib/mutapod/tools/claude",
		defaultConfigMountPath: RootHomeDir + "/.claude",
		defaultToolMountPath:   "/var/lib/mutapod/tools/claude",
		configSelector: func(cfg *config.Config) config.ProfileSyncConfig {
			return cfg.Profiles.Claude
		},
		ignorePatterns: []string{
			"cache",
			"cache/**",
			"debug",
			"debug/**",
			"downloads",
			"downloads/**",
		},
		setupScriptBuilder: func(spec Spec, pkg string) string {
			configMount := spec.Mounts[0].ContainerPath
			toolMount := spec.Mounts[1].ContainerPath
			return nodeProfileSetupScript(nodeProfileSetup{
				PackageName: pkg,
				ToolPrefix:  toolMount,
				BinaryName:  "claude",
				WrapperName: "claude",
				WrapperBody: fmt.Sprintf(
					"export HOME=%s\nexport CLAUDE_CONFIG_DIR=%s\nexec %s/bin/claude \"$@\"",
					shString(RootHomeDir),
					shString(configMount),
					shString(toolMount),
				),
			})
		},
		settingsBuilder: func(spec Spec) map[string]any {
			return map[string]any{
				"claudeCode.claudeProcessWrapper": "/usr/local/bin/claude",
				"claudeCode.disableLoginPrompt":   true,
			}
		},
		remoteEnvBuilder: func(spec Spec) map[string]string {
			return map[string]string{
				"CLAUDE_CONFIG_DIR": spec.Mounts[0].ContainerPath,
			}
		},
	}
}

func enrichClaudeSupplementalSyncs(cfg *config.Config, spec *Spec) error {
	if spec == nil {
		return nil
	}

	home, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	bridgeDir, fileName, ok, err := prepareSyncedFileBridge(filepath.Join(home, ".claude.json"), "claude-homefile")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	remoteDir := "/var/lib/mutapod/profiles/claude-homefile"
	spec.SupplementalSyncs = append(spec.SupplementalSyncs, SupplementalSync{
		Name:        spec.Name + "-homefile",
		SessionName: fmt.Sprintf("mutapod-%s-profile-claude-homefile", cfg.Name),
		LocalPath:   bridgeDir,
		RemotePath:  remoteDir,
	})
	spec.Mounts = append(spec.Mounts, Mount{
		RemotePath:    remoteDir + "/" + fileName,
		ContainerPath: RootHomeDir + "/.claude.json",
	})
	return nil
}

type nodeProfileSetup struct {
	PackageName   string
	ToolPrefix    string
	BinaryName    string
	WrapperName   string
	BeforeWrapper string
	WrapperBody   string
}

func nodeProfileSetupScript(setup nodeProfileSetup) string {
	lines := []string{
		"set -eu",
		fmt.Sprintf("tool_prefix=%s", shString(setup.ToolPrefix)),
		fmt.Sprintf("binary_name=%s", shString(setup.BinaryName)),
		fmt.Sprintf("package_name=%s", shString(setup.PackageName)),
		"start_mutapod_profile_heartbeat() {",
		"  (",
		"    while :; do",
		"      sleep 15",
		"      echo \"mutapod: profile setup still running for $package_name\" >&2",
		"    done",
		"  ) &",
		"  mutapod_profile_heartbeat_pid=$!",
		"}",
		"stop_mutapod_profile_heartbeat() {",
		"  if [ -n \"${mutapod_profile_heartbeat_pid:-}\" ]; then",
		"    kill \"$mutapod_profile_heartbeat_pid\" 2>/dev/null || true",
		"    wait \"$mutapod_profile_heartbeat_pid\" 2>/dev/null || true",
		"  fi",
		"}",
		"start_mutapod_profile_heartbeat",
		"trap stop_mutapod_profile_heartbeat EXIT INT TERM",
		"repair_debian_packages() {",
		"  if command -v dpkg >/dev/null 2>&1; then",
		"    DEBIAN_FRONTEND=noninteractive dpkg --configure -a >/dev/null || true",
		"  fi",
		"  if command -v apt-get >/dev/null 2>&1; then",
		"    DEBIAN_FRONTEND=noninteractive apt-get install -f -y -qq >/dev/null",
		"  fi",
		"  if command -v dpkg >/dev/null 2>&1; then",
		"    DEBIAN_FRONTEND=noninteractive dpkg --configure -a >/dev/null",
		"  fi",
		"}",
		"install_node_runtime() {",
		"  if command -v npm >/dev/null 2>&1; then",
		"    return 0",
		"  fi",
		"  if command -v apt-get >/dev/null 2>&1; then",
		"    repair_debian_packages",
		"    apt-get update -qq >/dev/null",
		"    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs npm >/dev/null",
		"    return 0",
		"  fi",
		"  if command -v apk >/dev/null 2>&1; then",
		"    apk add --no-cache nodejs npm >/dev/null",
		"    return 0",
		"  fi",
		"  if command -v dnf >/dev/null 2>&1; then",
		"    dnf install -y nodejs npm >/dev/null",
		"    return 0",
		"  fi",
		"  if command -v yum >/dev/null 2>&1; then",
		"    yum install -y nodejs npm >/dev/null",
		"    return 0",
		"  fi",
		"  echo \"mutapod: could not install Node.js/npm in this container\" >&2",
		"  exit 1",
		"}",
		"install_node_runtime",
		"mkdir -p \"$tool_prefix\"",
		"if [ ! -x \"$tool_prefix/bin/$binary_name\" ]; then",
		"  echo \"mutapod: installing $package_name in $tool_prefix\" >&2",
		"  npm install -g --prefix \"$tool_prefix\" \"$package_name\" >/dev/null",
		"fi",
	}
	if setup.BeforeWrapper != "" {
		lines = append(lines, setup.BeforeWrapper)
	}
	lines = append(lines,
		fmt.Sprintf("cat > /usr/local/bin/%s <<'EOF'", setup.WrapperName),
		"#!/bin/sh",
		"set -eu",
		setup.WrapperBody,
		"EOF",
		fmt.Sprintf("chmod +x /usr/local/bin/%s", setup.WrapperName),
	)
	return strings.Join(lines, "\n")
}

func resolveLocalProfilePath(cfg *config.Config, profileCfg config.ProfileSyncConfig, defaultHomeDir string) (string, bool, error) {
	localPath := profileCfg.LocalPath
	if localPath == "" {
		home, err := userHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("home dir: %w", err)
		}
		localPath = filepath.Join(home, defaultHomeDir)
	} else if !filepath.IsAbs(localPath) {
		localPath = filepath.Join(cfg.Dir, localPath)
	}

	localPath, err := filepath.Abs(localPath)
	if err != nil {
		return "", false, fmt.Errorf("resolve local path: %w", err)
	}

	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return localPath, false, nil
		}
		return "", false, fmt.Errorf("stat local path: %w", err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("local path %q is not a directory", localPath)
	}
	return localPath, true, nil
}

func detectBinary(name string) (string, bool) {
	path, err := lookPath(name)
	if err != nil || path == "" {
		return "", false
	}
	return path, true
}

func prepareSyncedFileBridge(sourcePath, bridgeName string) (string, string, bool, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("stat %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		return "", "", false, fmt.Errorf("%s is a directory, expected a file", sourcePath)
	}

	home, err := userHomeDir()
	if err != nil {
		return "", "", false, fmt.Errorf("home dir: %w", err)
	}
	bridgeDir := filepath.Join(home, ".mutapod", "profile-links", bridgeName)
	if err := os.MkdirAll(bridgeDir, 0700); err != nil {
		return "", "", false, fmt.Errorf("mkdir %s: %w", bridgeDir, err)
	}

	fileName := filepath.Base(sourcePath)
	bridgePath := filepath.Join(bridgeDir, fileName)
	if bridgeInfo, err := os.Stat(bridgePath); err == nil {
		if os.SameFile(info, bridgeInfo) {
			return bridgeDir, fileName, true, nil
		}
		if err := os.Remove(bridgePath); err != nil {
			return "", "", false, fmt.Errorf("remove stale bridge %s: %w", bridgePath, err)
		}
	} else if !os.IsNotExist(err) {
		return "", "", false, fmt.Errorf("stat bridge %s: %w", bridgePath, err)
	}

	if err := os.Link(sourcePath, bridgePath); err != nil {
		return "", "", false, fmt.Errorf("link %s -> %s: %w", bridgePath, sourcePath, err)
	}
	return bridgeDir, fileName, true, nil
}

func detectCodexFromVSCodeExtension() (string, bool) {
	home, err := userHomeDir()
	if err != nil {
		return "", false
	}

	var roots []string
	for _, dir := range []string{".vscode", ".vscode-insiders"} {
		roots = append(roots, filepath.Join(home, dir, "extensions"))
	}

	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{"codex.exe", "codex.cmd", "codex.bat"}
	} else {
		candidates = []string{"codex"}
	}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(strings.ToLower(entry.Name()), "openai.chatgpt-") {
				continue
			}
			for _, candidate := range candidates {
				matches, err := filepath.Glob(filepath.Join(root, entry.Name(), "bin", "*", candidate))
				if err != nil {
					continue
				}
				for _, match := range matches {
					if info, err := os.Stat(match); err == nil && !info.IsDir() {
						return match, true
					}
				}
			}
		}
	}

	return "", false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func shString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// EnsureRemoteTools runs each active profile's remote setup inside the primary
// service container.
func EnsureRemoteTools(ctx context.Context, runner interface {
	RunProfileSetup(context.Context, *config.Config, []Spec, Spec) error
}, cfg *config.Config, active []Spec) error {
	for _, spec := range active {
		if err := runner.RunProfileSetup(ctx, cfg, active, spec); err != nil {
			if isMissingRemoteExitStatus(err) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(retryDelay):
				}
				if retryErr := runner.RunProfileSetup(ctx, cfg, active, spec); retryErr == nil {
					continue
				} else {
					return fmt.Errorf("profiles: %s remote setup retry after lost SSH session: %w (original error: %v)", spec.Name, retryErr, err)
				}
			}
			return fmt.Errorf("profiles: %s remote setup: %w", spec.Name, err)
		}
	}
	return nil
}

func isMissingRemoteExitStatus(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "without exit status or exit signal")
}
