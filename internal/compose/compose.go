// Package compose detects and parses Docker Compose files, extracts
// forwarded ports, and starts/stops services on the remote VM.
package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/profiles"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
)

// candidateFiles is the ordered list of compose filenames to try.
var candidateFiles = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

// File represents a minimal parsed compose file — just enough to extract ports.
type File struct {
	Services map[string]Service `yaml:"services"`
}

const remoteOverrideFilename = ".mutapod.compose.override.yaml"

// Service is a single compose service entry.
type Service struct {
	Ports      []PortEntry   `yaml:"ports"`
	Volumes    []VolumeEntry `yaml:"volumes"`
	WorkingDir string        `yaml:"working_dir"`
}

// PortEntry handles both short form ("5000:5000", "5000") and long form
// (published: 5000, target: 5000). yaml.v3 unmarshals into a custom type.
type PortEntry struct {
	Published int
	Target    int
}

// VolumeEntry handles both short form (".:/app") and long form bind mounts.
type VolumeEntry struct {
	Source string
	Target string
}

func (p *PortEntry) UnmarshalYAML(value *yaml.Node) error {
	// Long form: { published: 5000, target: 5000 }
	if value.Kind == yaml.MappingNode {
		var long struct {
			Published interface{} `yaml:"published"`
			Target    int         `yaml:"target"`
		}
		if err := value.Decode(&long); err != nil {
			return err
		}
		p.Target = long.Target
		switch v := long.Published.(type) {
		case int:
			p.Published = v
		case string:
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("compose: invalid published port %q", v)
			}
			p.Published = n
		}
		return nil
	}

	// Short form: "5000:5000" or "5000"
	var s string
	if err := value.Decode(&s); err != nil {
		// Could be a plain integer
		var n int
		if err2 := value.Decode(&n); err2 != nil {
			return err
		}
		p.Published = n
		p.Target = n
		return nil
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		host, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("compose: invalid port %q", s)
		}
		container, err := strconv.Atoi(parts[1])
		if err != nil {
			return fmt.Errorf("compose: invalid port %q", s)
		}
		p.Published = host
		p.Target = container
	} else {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("compose: invalid port %q", s)
		}
		p.Published = n
		p.Target = n
	}
	return nil
}

func (v *VolumeEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		var long struct {
			Source string `yaml:"source"`
			Target string `yaml:"target"`
		}
		if err := value.Decode(&long); err != nil {
			return err
		}
		v.Source = long.Source
		v.Target = long.Target
		return nil
	}

	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return nil
	}

	modeIndex := len(parts) - 1
	targetIndex := modeIndex
	if strings.HasPrefix(parts[modeIndex], "/") {
		targetIndex = modeIndex
	} else {
		targetIndex = modeIndex - 1
	}
	if targetIndex < 1 {
		return nil
	}

	v.Source = strings.Join(parts[:targetIndex], ":")
	v.Target = parts[targetIndex]
	return nil
}

// DetectFile returns the path to the compose file for the project.
// If cfg.Compose.File is set it is used directly; otherwise the working
// directory is scanned for candidate filenames.
func DetectFile(cfg *config.Config) (string, error) {
	if cfg.Compose.File != "" {
		path := cfg.Compose.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.Dir, path)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("compose: configured file %q not found", path)
		}
		return path, nil
	}
	for _, name := range candidateFiles {
		path := filepath.Join(cfg.Dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("compose: no compose file found in %s (tried: %s)",
		cfg.Dir, strings.Join(candidateFiles, ", "))
}

// ParsePorts parses a compose file and returns all unique host-side ports.
// Extra ports from cfg.Compose.ExtraPorts are appended.
func ParsePorts(composePath string, extra []int) ([]int, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("compose: read %s: %w", composePath, err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("compose: parse %s: %w", composePath, err)
	}

	seen := map[int]bool{}
	var ports []int
	for _, svc := range f.Services {
		for _, pe := range svc.Ports {
			if pe.Published > 0 && !seen[pe.Published] {
				seen[pe.Published] = true
				ports = append(ports, pe.Published)
			}
		}
	}
	for _, p := range extra {
		if !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// LocalComposeArgs returns docker compose CLI arguments for the selected file.
func LocalComposeArgs(cfg *config.Config) ([]string, error) {
	args := []string{"compose"}

	composePath, err := DetectFile(cfg)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(cfg.Dir, composePath)
	if err != nil {
		return nil, fmt.Errorf("compose: resolve compose file: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if rel != "." {
		args = append(args, "-f", rel)
	}
	return args, nil
}

func remoteComposeArgs(cfg *config.Config, includeOverride bool) (string, error) {
	composePath, err := DetectFile(cfg)
	if err != nil {
		return "", nil
	}

	rel, err := filepath.Rel(cfg.Dir, composePath)
	if err != nil {
		return "", fmt.Errorf("compose: resolve remote compose file: %w", err)
	}
	rel = filepath.ToSlash(rel)
	args := ""
	if rel != "." {
		args = " -f " + shellQuote(rel)
	}
	if includeOverride {
		args += " -f " + shellQuote(remoteOverrideFilename)
	}
	return args, nil
}

// RemoteComposePath returns the expected compose file path on the remote host.
// It mirrors DetectFile/compose.file selection relative to the synced workspace.
func RemoteComposePath(cfg *config.Config) (string, error) {
	composePath, err := DetectFile(cfg)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(cfg.Dir, composePath)
	if err != nil {
		return "", fmt.Errorf("compose: resolve remote compose path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	return strings.TrimSuffix(cfg.WorkspacePath(), "/") + "/" + rel, nil
}

// DetectWorkspaceFolder tries to infer the in-container project folder for a
// service from its bind mount or working_dir configuration.
func DetectWorkspaceFolder(cfg *config.Config, serviceName string) (string, error) {
	composePath, err := DetectFile(cfg)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(composePath)
	if err != nil {
		return "", fmt.Errorf("compose: read %s: %w", composePath, err)
	}

	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("compose: parse %s: %w", composePath, err)
	}

	svc, ok := f.Services[serviceName]
	if !ok {
		return "", fmt.Errorf("compose: service %q not found in %s", serviceName, composePath)
	}

	localSyncPath, err := cfg.LocalSyncPath()
	if err != nil {
		return "", err
	}
	composeDir := filepath.Dir(composePath)
	for _, vol := range svc.Volumes {
		if vol.Target == "" {
			continue
		}
		if volumeMatchesWorkspace(vol.Source, composeDir, localSyncPath) {
			return filepath.ToSlash(vol.Target), nil
		}
	}
	if svc.WorkingDir != "" {
		return filepath.ToSlash(svc.WorkingDir), nil
	}
	return "", nil
}

// NeedsWorkspaceOverride reports whether mutapod should inject a remote bind
// mount from the synced workspace into the primary service.
func NeedsWorkspaceOverride(cfg *config.Config) (bool, error) {
	if cfg.Compose.PrimaryService == "" || cfg.Compose.WorkspaceFolder == "" {
		return false, nil
	}

	current, err := DetectWorkspaceFolder(cfg, cfg.Compose.PrimaryService)
	if err != nil {
		return false, err
	}
	return current == "", nil
}

func remoteOverridePath(cfg *config.Config) string {
	return strings.TrimSuffix(cfg.WorkspacePath(), "/") + "/" + remoteOverrideFilename
}

func renderRemoteOverride(cfg *config.Config, activeProfiles []profiles.Spec) ([]byte, error) {
	type overrideService struct {
		Volumes     []string `yaml:"volumes,omitempty"`
		ExtraHosts  []string `yaml:"extra_hosts,omitempty"`
		CapAdd      []string `yaml:"cap_add,omitempty"`
		SecurityOpt []string `yaml:"security_opt,omitempty"`
	}
	type overrideFile struct {
		Services map[string]overrideService `yaml:"services"`
	}

	service := cfg.Compose.PrimaryService
	if service == "" {
		return nil, fmt.Errorf("compose: primary_service is required for remote overrides")
	}

	volumes := make([]string, 0, 1+len(activeProfiles)*2)
	extraHosts := make([]string, 0, 1)
	capAdd := make([]string, 0, 1)
	securityOpt := make([]string, 0, 1)
	needsWorkspaceMount, err := NeedsWorkspaceOverride(cfg)
	if err != nil {
		return nil, err
	}
	if needsWorkspaceMount {
		target := filepath.ToSlash(cfg.Compose.WorkspaceFolder)
		if target == "" {
			return nil, fmt.Errorf("compose: workspace_folder is required for workspace override")
		}
		source := filepath.ToSlash(cfg.WorkspacePath())
		volumes = append(volumes, source+":"+target)
	}
	for _, profile := range activeProfiles {
		for _, mount := range profile.Mounts {
			if mount.RemotePath == "" || mount.ContainerPath == "" {
				continue
			}
			volumes = append(volumes, filepath.ToSlash(mount.RemotePath)+":"+filepath.ToSlash(mount.ContainerPath))
		}
		if profile.NeedsSandboxNamespaces {
			capAdd = appendUnique(capAdd, "SYS_ADMIN")
			securityOpt = appendUnique(securityOpt, "apparmor=unconfined")
			securityOpt = appendUnique(securityOpt, "seccomp=unconfined")
		}
	}
	if len(cfg.Compose.ReverseForwards) > 0 {
		extraHosts = append(extraHosts, "host.docker.internal:host-gateway")
	}
	if len(volumes) == 0 && len(extraHosts) == 0 && len(capAdd) == 0 && len(securityOpt) == 0 {
		return nil, nil
	}

	doc := overrideFile{
		Services: map[string]overrideService{
			service: {
				Volumes:     volumes,
				ExtraHosts:  extraHosts,
				CapAdd:      capAdd,
				SecurityOpt: securityOpt,
			},
		},
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("compose: marshal remote override: %w", err)
	}
	return data, nil
}

// NeedsRemoteOverride reports whether mutapod should upload a remote compose
// override for the primary service.
func NeedsRemoteOverride(cfg *config.Config, activeProfiles []profiles.Spec) (bool, error) {
	if cfg.Compose.PrimaryService == "" {
		return false, nil
	}
	needsWorkspaceMount, err := NeedsWorkspaceOverride(cfg)
	if err != nil {
		return false, err
	}
	if needsWorkspaceMount {
		return true, nil
	}
	if len(cfg.Compose.ReverseForwards) > 0 {
		return true, nil
	}
	for _, profile := range activeProfiles {
		if profile.NeedsSandboxNamespaces {
			return true, nil
		}
		if len(profile.Mounts) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// EnsureRemoteOverride uploads the generated compose override when the base
// compose file needs extra mutapod-managed mounts or environment.
func EnsureRemoteOverride(ctx context.Context, p provider.Provider, cfg *config.Config, activeProfiles []profiles.Spec) (bool, error) {
	needed, err := NeedsRemoteOverride(cfg, activeProfiles)
	if err != nil {
		return false, err
	}
	if !needed {
		return false, nil
	}

	data, err := renderRemoteOverride(cfg, activeProfiles)
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}

	tmp, err := os.CreateTemp("", "mutapod-compose-override-*.yaml")
	if err != nil {
		return false, fmt.Errorf("compose: create temp override: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, fmt.Errorf("compose: write temp override: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("compose: close temp override: %w", err)
	}

	if err := p.CopyFile(ctx, tmpPath, remoteOverridePath(cfg)); err != nil {
		return false, fmt.Errorf("compose: upload remote override: %w", err)
	}
	return true, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func volumeMatchesWorkspace(source, composeDir, localSyncPath string) bool {
	if source == "" {
		return false
	}
	if source == "." || source == "./" {
		return true
	}

	resolved := source
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(composeDir, resolved)
	}
	resolved = filepath.Clean(resolved)
	localSyncPath = filepath.Clean(localSyncPath)
	return strings.EqualFold(resolved, localSyncPath)
}

// Up runs `docker compose up -d` on the remote VM via SSH.
// If build is true, `--build` is included.
func Up(ctx context.Context, p provider.Provider, cfg *config.Config, build bool) error {
	workspacePath := cfg.WorkspacePath()
	activeProfiles, err := profiles.Active(cfg)
	if err != nil {
		return err
	}
	includeOverride, err := NeedsRemoteOverride(cfg, activeProfiles)
	if err != nil {
		return err
	}
	composeArgs, err := remoteComposeArgs(cfg, includeOverride)
	if err != nil {
		return err
	}
	cmd := upCommand(workspacePath, composeArgs, build)
	shell.Debugf("compose: running: %s", cmd)

	var outWriter, errWriter = shell.DebugWriters()
	return p.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{
		Stdout: outWriter,
		Stderr: errWriter,
	})
}

func upCommand(workspacePath, composeArgs string, build bool) string {
	buildArg := ""
	if build {
		buildArg = " --build"
	}
	return fmt.Sprintf(
		"cd %s && sudo docker compose%s up -d%s",
		shellQuote(workspacePath),
		composeArgs,
		buildArg,
	)
}

// ConfigureGitSafeDirectory marks every path as safe for git inside the
// primary service container, so VS Code and CLI tools can use git even when
// the workspace files are owned by a different UID than the container user.
func ConfigureGitSafeDirectory(ctx context.Context, p provider.Provider, cfg *config.Config, activeProfiles []profiles.Spec) error {
	if cfg.Compose.PrimaryService == "" {
		return nil
	}
	script := "command -v git >/dev/null 2>&1 && git config --system --add safe.directory '*' || true"
	return ExecInPrimaryService(ctx, p, cfg, activeProfiles, script)
}

// ExecInPrimaryService runs a shell script inside the primary service
// container. It uses `docker compose exec -T --user root` so mutapod can
// install helper tooling predictably.
func ExecInPrimaryService(ctx context.Context, p provider.Provider, cfg *config.Config, activeProfiles []profiles.Spec, script string) error {
	if cfg.Compose.PrimaryService == "" {
		return fmt.Errorf("compose: primary_service is required for exec in primary service")
	}
	includeOverride, err := NeedsRemoteOverride(cfg, activeProfiles)
	if err != nil {
		return err
	}
	composeArgs, err := remoteComposeArgs(cfg, includeOverride)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(
		"cd %s && sudo docker compose%s exec -T --user root %s sh -lc %s",
		shellQuote(cfg.WorkspacePath()),
		composeArgs,
		shellQuote(cfg.Compose.PrimaryService),
		shellQuote(script),
	)
	shell.Debugf("compose: exec in primary service: %s", cmd)
	var outWriter, errWriter = shell.DebugWriters()
	return p.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{
		Stdout: outWriter,
		Stderr: errWriter,
	})
}

// Down runs `docker compose down` on the remote VM via SSH.
func Down(ctx context.Context, p provider.Provider, cfg *config.Config) error {
	workspacePath := cfg.WorkspacePath()
	activeProfiles, err := profiles.Active(cfg)
	if err != nil {
		shell.Debugf("compose: profile detection for down: %v", err)
		activeProfiles = nil
	}
	includeOverride, err := NeedsRemoteOverride(cfg, activeProfiles)
	if err != nil {
		return err
	}
	composeArgs, err := remoteComposeArgs(cfg, includeOverride)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(
		"cd %s && sudo docker compose%s down",
		shellQuote(workspacePath),
		composeArgs,
	)
	shell.Debugf("compose: running: %s", cmd)

	var outWriter, errWriter = shell.DebugWriters()
	return p.Exec(ctx, []string{"bash", "-c", cmd}, provider.ExecOptions{
		Stdout: outWriter,
		Stderr: errWriter,
	})
}
