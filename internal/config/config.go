package config

// Config is the parsed representation of mutapod.yaml.
type Config struct {
	Name     string         `yaml:"name"`
	Provider ProviderConfig `yaml:"provider"`
	Sync     SyncConfig     `yaml:"sync"`
	Compose  ComposeConfig  `yaml:"compose"`
	Profiles ProfilesConfig `yaml:"profiles"`
	Idle     IdleConfig     `yaml:"idle"`

	// Dir is the directory containing mutapod.yaml. Set by Load, not from YAML.
	Dir string `yaml:"-"`
	// InstanceOwner is the resolved account token used in the generated VM name.
	// It is internal/runtime state, not part of mutapod.yaml.
	InstanceOwner string `yaml:"-"`
}

type ProviderConfig struct {
	Type  string      `yaml:"type"` // "gcp" or "azure"
	GCP   GCPConfig   `yaml:"gcp"`
	Azure AzureConfig `yaml:"azure"`
}

type GCPConfig struct {
	Project        string            `yaml:"project"`
	Zone           string            `yaml:"zone"`
	MachineType    string            `yaml:"machine_type"`
	DiskSizeGB     int               `yaml:"disk_size_gb"`
	DiskType       string            `yaml:"disk_type"`
	ImageFamily    string            `yaml:"image_family"`
	ImageProject   string            `yaml:"image_project"`
	Network        string            `yaml:"network"`
	Subnet         string            `yaml:"subnet"`
	ServiceAccount string            `yaml:"service_account"`
	Tags           []string          `yaml:"tags"`
	Preemptible    bool              `yaml:"preemptible"`
	Spot           bool              `yaml:"spot"`
	Labels         map[string]string `yaml:"labels"`
}

type AzureConfig struct {
	Tenant            string            `yaml:"tenant"`
	Subscription      string            `yaml:"subscription"`
	ResourceGroup     string            `yaml:"resource_group"`
	Location          string            `yaml:"location"`
	VMSize            string            `yaml:"vm_size"`
	DiskSizeGB        int               `yaml:"disk_size_gb"`
	StorageSKU        string            `yaml:"storage_sku"`
	Image             string            `yaml:"image"`
	VNet              string            `yaml:"vnet"`
	Subnet            string            `yaml:"subnet"`
	AdminUsername     string            `yaml:"admin_username"`
	Identity          string            `yaml:"identity"`
	PublicIP          bool              `yaml:"public_ip"`
	PublicIPSku       string            `yaml:"public_ip_sku"`
	PreferPrivateIP   bool              `yaml:"prefer_private_ip"`
	SSHSources        []string          `yaml:"ssh_sources"`
	SSHPrivateKeyFile string            `yaml:"ssh_private_key_file"`
	SSHPublicKeyFile  string            `yaml:"ssh_public_key_file"`
	Tags              map[string]string `yaml:"tags"`
}

type SyncConfig struct {
	LocalPath  string `yaml:"local_path"`
	RemotePath string `yaml:"remote_path"`
	Mode       string `yaml:"mode"`
}

type ComposeConfig struct {
	// File is the path to the compose file relative to the project root.
	// If empty, mutapod auto-detects compose.yaml / docker-compose.yaml.
	File                string   `yaml:"file"`
	PrimaryService      string   `yaml:"primary_service"`
	WorkspaceFolder     string   `yaml:"workspace_folder"`
	Extensions          []string `yaml:"extensions"`
	CopyLocalExtensions *bool    `yaml:"copy_local_extensions"`
	ExtraPorts          []int    `yaml:"extra_ports"`
	ReverseForwards     []int    `yaml:"reverse_forwards"`
}

type ProfilesConfig struct {
	Codex  ProfileSyncConfig `yaml:"codex"`
	Claude ProfileSyncConfig `yaml:"claude"`
}

type ProfileSyncConfig struct {
	Enabled    *bool  `yaml:"enabled"`
	LocalPath  string `yaml:"local_path"`
	RemotePath string `yaml:"remote_path"`
	MountPath  string `yaml:"mount_path"`
}

type IdleConfig struct {
	Enabled              *bool `yaml:"enabled"`
	TimeoutMinutes       int   `yaml:"timeout_minutes"`
	CheckIntervalSeconds int   `yaml:"check_interval_seconds"`
}

func (i IdleConfig) IsEnabled() bool {
	if i.Enabled == nil {
		return true
	}
	return *i.Enabled
}

func (c ComposeConfig) CopyLocalExtensionsEnabled() bool {
	if c.CopyLocalExtensions == nil {
		return true
	}
	return *c.CopyLocalExtensions
}
