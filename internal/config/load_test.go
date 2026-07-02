package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "mutapod.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func TestLoad_GCP(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: myservice
provider:
  type: gcp
  gcp:
    project: my-project
    zone: europe-west1-b
    machine_type: e2-standard-4
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "myservice" {
		t.Errorf("name: got %q, want %q", cfg.Name, "myservice")
	}
	if cfg.Provider.GCP.Project != "my-project" {
		t.Errorf("project: got %q", cfg.Provider.GCP.Project)
	}
	if cfg.Provider.GCP.Zone != "europe-west1-b" {
		t.Errorf("zone: got %q", cfg.Provider.GCP.Zone)
	}
	cfg.InstanceOwner = "tester"
	if cfg.InstanceName() != "mutapod-tester-myservice" {
		t.Errorf("instance name: got %q", cfg.InstanceName())
	}
}

func TestLoad_Azure(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: myservice
provider:
  type: azure
  azure:
    tenant: tenant-123
    subscription: sub-123
    resource_group: rg-dev
    location: westeurope
    vm_size: Standard_D4s_v5
    disk_size_gb: 64
    storage_sku: StandardSSD_LRS
    image: Ubuntu2204
    admin_username: devuser
    vnet: dev-vnet
    subnet: dev-subnet
    identity: "[system]"
    prefer_private_ip: true
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.Azure.Tenant != "tenant-123" {
		t.Errorf("tenant: got %q", cfg.Provider.Azure.Tenant)
	}
	if cfg.Provider.Azure.Subscription != "sub-123" {
		t.Errorf("subscription: got %q", cfg.Provider.Azure.Subscription)
	}
	if cfg.Provider.Azure.ResourceGroup != "rg-dev" {
		t.Errorf("resource_group: got %q", cfg.Provider.Azure.ResourceGroup)
	}
	if cfg.Provider.Azure.Location != "westeurope" {
		t.Errorf("location: got %q", cfg.Provider.Azure.Location)
	}
	if cfg.Provider.Azure.VMSize != "Standard_D4s_v5" {
		t.Errorf("vm_size: got %q", cfg.Provider.Azure.VMSize)
	}
	if cfg.Provider.Azure.DiskSizeGB != 64 {
		t.Errorf("disk_size_gb: got %d", cfg.Provider.Azure.DiskSizeGB)
	}
	if cfg.Provider.Azure.AdminUsername != "devuser" {
		t.Errorf("admin_username: got %q", cfg.Provider.Azure.AdminUsername)
	}
	if !cfg.Provider.Azure.PreferPrivateIP {
		t.Error("prefer_private_ip: got false, want true")
	}
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.GCP.Project != "proj" {
		t.Errorf("project: got %q", cfg.Provider.GCP.Project)
	}
	if cfg.Provider.GCP.Zone != "us-central1-a" {
		t.Errorf("default zone: got %q", cfg.Provider.GCP.Zone)
	}
	if cfg.Provider.GCP.MachineType != "e2-standard-4" {
		t.Errorf("default machine_type: got %q", cfg.Provider.GCP.MachineType)
	}
	if cfg.Provider.GCP.DiskSizeGB != 30 {
		t.Errorf("default disk_size_gb: got %d", cfg.Provider.GCP.DiskSizeGB)
	}
	if cfg.Provider.GCP.DiskType != "pd-balanced" {
		t.Errorf("default disk_type: got %q", cfg.Provider.GCP.DiskType)
	}
	if cfg.Provider.GCP.ImageFamily != "ubuntu-2204-lts" {
		t.Errorf("default image_family: got %q", cfg.Provider.GCP.ImageFamily)
	}
	if cfg.Idle.TimeoutMinutes != 30 {
		t.Errorf("default idle timeout: got %d", cfg.Idle.TimeoutMinutes)
	}
	if !cfg.Idle.IsEnabled() {
		t.Errorf("default idle.enabled: got false, want true")
	}
	if cfg.Sync.Mode != "two-way-resolved" {
		t.Errorf("default sync mode: got %q", cfg.Sync.Mode)
	}
	if cfg.Compose.ForwardBackend != "ssh" {
		t.Errorf("default compose.forward_backend: got %q, want ssh", cfg.Compose.ForwardBackend)
	}
	if cfg.Compose.ForwardToPrimaryService {
		t.Errorf("default compose.forward_to_primary_service: got true, want false")
	}
}

func TestLoad_AzureDefaults(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: azure
  azure:
    resource_group: rg-dev
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.Azure.VMSize != "Standard_D4s_v5" {
		t.Errorf("default vm_size: got %q", cfg.Provider.Azure.VMSize)
	}
	if cfg.Provider.Azure.DiskSizeGB != 30 {
		t.Errorf("default disk_size_gb: got %d", cfg.Provider.Azure.DiskSizeGB)
	}
	if cfg.Provider.Azure.StorageSKU != "StandardSSD_LRS" {
		t.Errorf("default storage_sku: got %q", cfg.Provider.Azure.StorageSKU)
	}
	if cfg.Provider.Azure.Image != "Ubuntu2204" {
		t.Errorf("default image: got %q", cfg.Provider.Azure.Image)
	}
	if cfg.Provider.Azure.AdminUsername != "azureuser" {
		t.Errorf("default admin_username: got %q", cfg.Provider.Azure.AdminUsername)
	}
	if cfg.Provider.Azure.PublicIPSku != "Standard" {
		t.Errorf("default public_ip_sku: got %q", cfg.Provider.Azure.PublicIPSku)
	}
	if cfg.Provider.Azure.PublicIP {
		t.Error("default public_ip: got true, want false")
	}
	if cfg.Provider.Azure.Tags["managed-by"] != "mutapod" {
		t.Errorf("default tags: got %v", cfg.Provider.Azure.Tags)
	}
}

func TestLoad_ProviderOverrideSelectsActiveProvider(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
  azure:
    resource_group: rg-dev
`)
	cfg, err := LoadWithOptions(dir, LoadOptions{ProviderOverride: "azure"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.Type != "azure" {
		t.Fatalf("provider.type: got %q, want azure", cfg.Provider.Type)
	}
	if cfg.Provider.Azure.ResourceGroup != "rg-dev" {
		t.Fatalf("azure.resource_group: got %q", cfg.Provider.Azure.ResourceGroup)
	}
	if cfg.Provider.Azure.VMSize != "Standard_D4s_v5" {
		t.Fatalf("azure default vm_size: got %q", cfg.Provider.Azure.VMSize)
	}
}

func TestLoad_ProviderOverrideAllowsMissingDefaultProviderType(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  azure:
    resource_group: rg-dev
`)
	cfg, err := LoadWithOptions(dir, LoadOptions{ProviderOverride: "azure"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.Type != "azure" {
		t.Fatalf("provider.type: got %q, want azure", cfg.Provider.Type)
	}
}

func TestLoad_ComposePrimaryService(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
compose:
  file: compose-dev.yaml
  primary_service: web
  workspace_folder: /app
  reverse_forwards: [8154, 8154]
  extensions:
    - ms-python.python
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Compose.File != "compose-dev.yaml" {
		t.Fatalf("compose.file: got %q", cfg.Compose.File)
	}
	if cfg.Compose.PrimaryService != "web" {
		t.Fatalf("compose.primary_service: got %q", cfg.Compose.PrimaryService)
	}
	if cfg.Compose.WorkspaceFolder != "/app" {
		t.Fatalf("compose.workspace_folder: got %q", cfg.Compose.WorkspaceFolder)
	}
	if len(cfg.Compose.ReverseForwards) != 1 || cfg.Compose.ReverseForwards[0] != 8154 {
		t.Fatalf("compose.reverse_forwards: got %v", cfg.Compose.ReverseForwards)
	}
	if len(cfg.Compose.Extensions) != 1 || cfg.Compose.Extensions[0] != "ms-python.python" {
		t.Fatalf("compose.extensions: got %v", cfg.Compose.Extensions)
	}
	if !cfg.Compose.CopyLocalExtensionsEnabled() {
		t.Fatal("compose.copy_local_extensions: got false, want true by default")
	}
}

func TestLoad_Profiles(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
profiles:
  codex:
    enabled: true
    mount_path: /root/.codex
  claude:
    enabled: false
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Profiles.Codex.Enabled == nil || !*cfg.Profiles.Codex.Enabled {
		t.Fatal("profiles.codex.enabled: got false/nil, want true")
	}
	if cfg.Profiles.Codex.MountPath != "/root/.codex" {
		t.Fatalf("profiles.codex.mount_path: got %q", cfg.Profiles.Codex.MountPath)
	}
	if cfg.Profiles.Claude.Enabled == nil || *cfg.Profiles.Claude.Enabled {
		t.Fatal("profiles.claude.enabled: got true/nil, want false")
	}
}

func TestLoad_ComposeCopyLocalExtensionsFalseIsPreserved(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
compose:
  primary_service: web
  copy_local_extensions: false
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Compose.CopyLocalExtensionsEnabled() {
		t.Fatal("compose.copy_local_extensions: got true, want false")
	}
}

func TestLoad_IdleEnabledFalseIsPreserved(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
idle:
  enabled: false
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Idle.IsEnabled() {
		t.Fatal("idle.enabled: got true, want false")
	}
}

func TestLoad_Validation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "missing name",
			yaml:    "provider:\n  type: gcp\n  gcp:\n    project: p\n",
			wantErr: "'name' is required",
		},
		{
			name:    "missing provider type",
			yaml:    "name: x\n",
			wantErr: "'provider.type' is required",
		},
		{
			name:    "unsupported provider",
			yaml:    "name: x\nprovider:\n  type: aws\n",
			wantErr: "unsupported provider type",
		},
		{
			name:    "gcp missing project",
			yaml:    "name: x\nprovider:\n  type: gcp\n",
			wantErr: "'provider.gcp.project' is required",
		},
		{
			name:    "azure missing resource group",
			yaml:    "name: x\nprovider:\n  type: azure\n",
			wantErr: "'provider.azure.resource_group' is required",
		},
		{
			name:    "invalid reverse forward port",
			yaml:    "name: x\nprovider:\n  type: gcp\n  gcp:\n    project: p\ncompose:\n  reverse_forwards: [0]\n",
			wantErr: "compose.reverse_forwards contains invalid port 0",
		},
		{
			name:    "forward to primary service requires primary service",
			yaml:    "name: x\nprovider:\n  type: gcp\n  gcp:\n    project: p\ncompose:\n  forward_to_primary_service: true\n",
			wantErr: "compose.forward_to_primary_service requires compose.primary_service",
		},
		{
			name:    "unsupported forward backend",
			yaml:    "name: x\nprovider:\n  type: gcp\n  gcp:\n    project: p\ncompose:\n  forward_backend: magic\n",
			wantErr: "unsupported compose.forward_backend",
		},
		{
			name:    "reserved GCP fingerprint label",
			yaml:    "name: x\nprovider:\n  type: gcp\n  gcp:\n    project: p\n    labels:\n      mutapod-config: custom\n",
			wantErr: "is reserved by mutapod",
		},
		{
			name:    "reserved Azure fingerprint tag",
			yaml:    "name: x\nprovider:\n  type: azure\n  azure:\n    resource_group: rg\n    tags:\n      MUTAPOD-CONFIG: custom\n",
			wantErr: "is reserved by mutapod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeYAML(t, dir, tt.yaml)
			_, err := Load(dir)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoad_ForwardToPrimaryServiceAllowedWithDefaultSSHBackend(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: test
provider:
  type: gcp
  gcp:
    project: proj
compose:
  primary_service: web
  forward_to_primary_service: true
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Compose.ForwardBackend != "ssh" {
		t.Fatalf("forward_backend: got %q, want ssh", cfg.Compose.ForwardBackend)
	}
	if !cfg.Compose.ForwardToPrimaryService {
		t.Fatal("forward_to_primary_service: got false, want true")
	}
}

func TestLoad_WalksUp(t *testing.T) {
	root := t.TempDir()
	writeYAML(t, root, `
name: walktest
provider:
  type: gcp
  gcp:
    project: p
`)
	// Load from a subdirectory — should find mutapod.yaml in parent
	subdir := filepath.Join(root, "src", "app")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(subdir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "walktest" {
		t.Errorf("name: got %q", cfg.Name)
	}
}

func TestLoad_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `
name: myapp
provider:
  type: gcp
  gcp:
    project: p
`)
	cfg, _ := Load(dir)
	if got := cfg.WorkspacePath(); got != "/workspace/myapp" {
		t.Errorf("workspace path: got %q", got)
	}
}

func TestDetectInstanceOwner_GCPPrefersActiveAccount(t *testing.T) {
	oldGcloud := gcloudAccountLookup
	oldLocal := localUsernameLookup
	t.Cleanup(func() {
		gcloudAccountLookup = oldGcloud
		localUsernameLookup = oldLocal
	})

	gcloudAccountLookup = func() string { return "pavel.kuriscak@gmail.com" }
	localUsernameLookup = func() string { return "ignored-local-user" }

	got := detectInstanceOwner("gcp")
	if got != "pavel-kuriscak" {
		t.Fatalf("detectInstanceOwner(gcp): got %q, want %q", got, "pavel-kuriscak")
	}
}

func TestDetectInstanceOwner_AzurePrefersActiveAccount(t *testing.T) {
	oldAzure := azureAccountLookup
	oldLocal := localUsernameLookup
	t.Cleanup(func() {
		azureAccountLookup = oldAzure
		localUsernameLookup = oldLocal
	})

	azureAccountLookup = func() string { return "pavel.kuriscak@example.com" }
	localUsernameLookup = func() string { return "ignored-local-user" }

	got := detectInstanceOwner("azure")
	if got != "pavel-kuriscak" {
		t.Fatalf("detectInstanceOwner(azure): got %q, want %q", got, "pavel-kuriscak")
	}
}

func TestBuildInstanceName_TruncatesDeterministically(t *testing.T) {
	got := buildInstanceName("very-long-owner-name", strings.Repeat("workspace", 10))
	if len(got) > 63 {
		t.Fatalf("instance name too long: %d", len(got))
	}
	if !contains(got, "mutapod-very-long-owner-name-") {
		t.Fatalf("expected instance name prefix, got %q", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
