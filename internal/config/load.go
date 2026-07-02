package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const filename = "mutapod.yaml"

// Load finds mutapod.yaml by walking up from dir and parses it.
// dir is typically the current working directory.
func Load(dir string) (*Config, error) {
	return LoadWithOptions(dir, LoadOptions{})
}

// LoadOptions controls how mutapod.yaml is interpreted.
type LoadOptions struct {
	// ProviderOverride selects the active provider for this invocation. When
	// empty, provider.type from mutapod.yaml is used as the default provider.
	ProviderOverride string
}

// LoadWithOptions finds mutapod.yaml by walking up from dir and parses it.
func LoadWithOptions(dir string, opts LoadOptions) (*Config, error) {
	path, err := find(dir)
	if err != nil {
		return nil, err
	}
	return loadFile(path, opts)
}

// LoadFile parses a specific mutapod.yaml file.
func LoadFile(path string) (*Config, error) {
	return LoadFileWithOptions(path, LoadOptions{})
}

// LoadFileWithOptions parses a specific mutapod.yaml file.
func LoadFileWithOptions(path string, opts LoadOptions) (*Config, error) {
	return loadFile(path, opts)
}

func find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("config: resolve dir: %w", err)
	}
	for {
		candidate := filepath.Join(dir, filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("config: %s not found in %s or any parent directory", filename, start)
}

func loadFile(path string, opts LoadOptions) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.Dir = filepath.Dir(path)
	if providerOverride := strings.ToLower(strings.TrimSpace(opts.ProviderOverride)); providerOverride != "" {
		cfg.Provider.Type = providerOverride
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Idle.Enabled == nil {
		enabled := true
		cfg.Idle.Enabled = &enabled
	}
	if cfg.Sync.LocalPath == "" {
		cfg.Sync.LocalPath = "."
	}
	if cfg.Sync.Mode == "" {
		cfg.Sync.Mode = "two-way-resolved"
	}
	if cfg.Compose.ForwardBackend == "" {
		cfg.Compose.ForwardBackend = "ssh"
	}
	if cfg.Idle.TimeoutMinutes == 0 {
		cfg.Idle.TimeoutMinutes = 30
	}
	if cfg.Idle.CheckIntervalSeconds == 0 {
		cfg.Idle.CheckIntervalSeconds = 60
	}
	cfg.Compose.ExtraPorts = dedupePorts(cfg.Compose.ExtraPorts)
	cfg.Compose.ReverseForwards = dedupePorts(cfg.Compose.ReverseForwards)
	// Apply GCP defaults
	if cfg.Provider.Type == "gcp" {
		gcp := &cfg.Provider.GCP
		if gcp.Zone == "" {
			gcp.Zone = "us-central1-a"
		}
		if gcp.MachineType == "" {
			gcp.MachineType = "e2-standard-4"
		}
		if gcp.DiskSizeGB == 0 {
			gcp.DiskSizeGB = 30
		}
		if gcp.DiskType == "" {
			gcp.DiskType = "pd-balanced"
		}
		if gcp.ImageFamily == "" {
			gcp.ImageFamily = "ubuntu-2204-lts"
		}
		if gcp.ImageProject == "" {
			gcp.ImageProject = "ubuntu-os-cloud"
		}
		if gcp.Labels == nil {
			gcp.Labels = map[string]string{"managed-by": "mutapod"}
		}
	}
	if cfg.Provider.Type == "azure" {
		az := &cfg.Provider.Azure
		if az.VMSize == "" {
			az.VMSize = "Standard_D4s_v5"
		}
		if az.DiskSizeGB == 0 {
			az.DiskSizeGB = 30
		}
		if az.StorageSKU == "" {
			az.StorageSKU = "StandardSSD_LRS"
		}
		if az.Image == "" {
			az.Image = "Ubuntu2204"
		}
		if az.AdminUsername == "" {
			az.AdminUsername = "azureuser"
		}
		if az.PublicIPSku == "" {
			az.PublicIPSku = "Standard"
		}
		if az.Tags == nil {
			az.Tags = map[string]string{"managed-by": "mutapod"}
		}
	}
}

func validate(cfg *Config) error {
	if cfg.Name == "" {
		return fmt.Errorf("config: 'name' is required")
	}
	if cfg.Provider.Type == "" {
		return fmt.Errorf("config: 'provider.type' is required")
	}
	switch cfg.Provider.Type {
	case "gcp":
		if cfg.Provider.GCP.Project == "" {
			return fmt.Errorf("config: 'provider.gcp.project' is required")
		}
		if _, exists := cfg.Provider.GCP.Labels[VMConfigFingerprintKey]; exists {
			return fmt.Errorf("config: provider.gcp.labels key %q is reserved by mutapod", VMConfigFingerprintKey)
		}
	case "azure":
		if cfg.Provider.Azure.ResourceGroup == "" {
			return fmt.Errorf("config: 'provider.azure.resource_group' is required")
		}
		for key := range cfg.Provider.Azure.Tags {
			if strings.EqualFold(key, VMConfigFingerprintKey) {
				return fmt.Errorf("config: provider.azure.tags key %q is reserved by mutapod", VMConfigFingerprintKey)
			}
		}
	default:
		return fmt.Errorf("config: unsupported provider type %q (supported: gcp, azure)", cfg.Provider.Type)
	}
	if err := validatePorts("compose.extra_ports", cfg.Compose.ExtraPorts); err != nil {
		return err
	}
	if err := validatePorts("compose.reverse_forwards", cfg.Compose.ReverseForwards); err != nil {
		return err
	}
	switch cfg.Compose.ForwardBackend {
	case "mutagen", "ssh":
	default:
		return fmt.Errorf("config: unsupported compose.forward_backend %q (supported: mutagen, ssh)", cfg.Compose.ForwardBackend)
	}
	if cfg.Compose.ForwardToPrimaryService && cfg.Compose.PrimaryService == "" {
		return fmt.Errorf("config: compose.forward_to_primary_service requires compose.primary_service")
	}
	return nil
}

// WorkspacePath returns the resolved remote workspace path.
// If RemotePath is set in config, that is used; otherwise /workspace/<name>.
func (c *Config) WorkspacePath() string {
	if c.Sync.RemotePath != "" {
		return c.Sync.RemotePath
	}
	return "/workspace/" + c.Name
}

// LocalSyncPath returns the absolute local path to sync.
func (c *Config) LocalSyncPath() (string, error) {
	p := c.Sync.LocalPath
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.Dir, p)
	}
	return filepath.Abs(p)
}

func validatePorts(path string, ports []int) error {
	for _, port := range ports {
		if port <= 0 || port > 65535 {
			return fmt.Errorf("config: %s contains invalid port %d", path, port)
		}
	}
	return nil
}

func dedupePorts(ports []int) []int {
	if len(ports) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(ports))
	deduped := make([]int, 0, len(ports))
	for _, port := range ports {
		if seen[port] {
			continue
		}
		seen[port] = true
		deduped = append(deduped, port)
	}
	return slices.Clip(deduped)
}
