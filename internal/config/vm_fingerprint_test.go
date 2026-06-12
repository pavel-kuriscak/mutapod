package config

import (
	"strings"
	"testing"
)

func TestVMConfigFingerprint_GCPDeterministicForUnorderedFields(t *testing.T) {
	left := fingerprintTestConfig("gcp")
	left.Provider.GCP.Tags = []string{"web", "ssh", "web"}
	left.Provider.GCP.Labels = map[string]string{"team": "dev", "managed-by": "mutapod"}

	right := fingerprintTestConfig("gcp")
	right.Provider.GCP.Tags = []string{"ssh", "web"}
	right.Provider.GCP.Labels = map[string]string{"managed-by": "mutapod", "team": "dev"}

	if got, want := mustFingerprint(t, left), mustFingerprint(t, right); got != want {
		t.Fatalf("ordering changed fingerprint: %q != %q", got, want)
	}
}

func TestVMConfigFingerprint_AzureDeterministicForUnorderedFields(t *testing.T) {
	left := fingerprintTestConfig("azure")
	left.Provider.Azure.SSHSources = []string{"10.2.0.0/24", "10.1.0.0/24", "10.2.0.0/24"}
	left.Provider.Azure.Tags = map[string]string{"team": "dev", "managed-by": "mutapod"}

	right := fingerprintTestConfig("azure")
	right.Provider.Azure.SSHSources = []string{"10.1.0.0/24", "10.2.0.0/24"}
	right.Provider.Azure.Tags = map[string]string{"managed-by": "mutapod", "team": "dev"}

	if got, want := mustFingerprint(t, left), mustFingerprint(t, right); got != want {
		t.Fatalf("ordering changed fingerprint: %q != %q", got, want)
	}
}

func TestVMConfigFingerprint_ChangesForManagedFields(t *testing.T) {
	tests := []struct {
		name   string
		base   *Config
		change func(*Config)
	}{
		{"gcp machine type", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.MachineType = "n2-standard-8" }},
		{"gcp disk size", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.DiskSizeGB++ }},
		{"gcp disk type", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.DiskType = "pd-ssd" }},
		{"gcp image family", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.ImageFamily = "ubuntu-2404-lts-amd64" }},
		{"gcp image project", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.ImageProject = "custom-images" }},
		{"gcp network", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.Network = "other" }},
		{"gcp subnet", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.Subnet = "other" }},
		{"gcp service account", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.ServiceAccount = "vm@example.test" }},
		{"gcp tags", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.Tags = []string{"ssh"} }},
		{"gcp preemptible", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.Preemptible = true }},
		{"gcp provisioning", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.Spot = true }},
		{"gcp labels", fingerprintTestConfig("gcp"), func(c *Config) { c.Provider.GCP.Labels["team"] = "platform" }},
		{"azure location", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.Location = "eastus" }},
		{"azure size", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.VMSize = "Standard_D8s_v5" }},
		{"azure disk size", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.DiskSizeGB++ }},
		{"azure storage sku", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.StorageSKU = "Premium_LRS" }},
		{"azure image", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.Image = "Ubuntu2404" }},
		{"azure vnet", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.VNet = "other" }},
		{"azure subnet", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.Subnet = "other" }},
		{"azure admin", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.AdminUsername = "developer" }},
		{"azure identity", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.Identity = "workload" }},
		{"azure public ip", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.PublicIP = true }},
		{"azure public ip sku", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.PublicIPSku = "Basic" }},
		{"azure private key", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.SSHPrivateKeyFile = "~/.ssh/other" }},
		{"azure public key", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.SSHPublicKeyFile = "~/.ssh/other.pub" }},
		{"azure sources", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.SSHSources = []string{"10.2.0.0/24"} }},
		{"azure tags", fingerprintTestConfig("azure"), func(c *Config) { c.Provider.Azure.Tags["team"] = "platform" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := mustFingerprint(t, tt.base)
			tt.change(tt.base)
			after := mustFingerprint(t, tt.base)
			if before == after {
				t.Fatalf("managed field did not change fingerprint %q", before)
			}
		})
	}
}

func TestVMConfigFingerprint_SpotMakesPreemptibleFlagIrrelevant(t *testing.T) {
	left := fingerprintTestConfig("gcp")
	left.Provider.GCP.Spot = true
	right := fingerprintTestConfig("gcp")
	right.Provider.GCP.Spot = true
	right.Provider.GCP.Preemptible = true

	if got, want := mustFingerprint(t, left), mustFingerprint(t, right); got != want {
		t.Fatalf("preemptible changed effective Spot fingerprint: %q != %q", got, want)
	}
}

func TestVMConfigFingerprint_ExcludesTargetAndLocalFields(t *testing.T) {
	gcp := fingerprintTestConfig("gcp")
	before := mustFingerprint(t, gcp)
	gcp.Provider.GCP.Project = "other-project"
	gcp.Provider.GCP.Zone = "other-zone"
	if after := mustFingerprint(t, gcp); before != after {
		t.Fatalf("GCP target fields changed fingerprint: %q != %q", before, after)
	}

	azure := fingerprintTestConfig("azure")
	before = mustFingerprint(t, azure)
	azure.Provider.Azure.Tenant = "other-tenant"
	azure.Provider.Azure.Subscription = "other-subscription"
	azure.Provider.Azure.ResourceGroup = "other-group"
	azure.Provider.Azure.PreferPrivateIP = !azure.Provider.Azure.PreferPrivateIP
	if after := mustFingerprint(t, azure); before != after {
		t.Fatalf("Azure target/local fields changed fingerprint: %q != %q", before, after)
	}
}

func TestVMConfigFingerprint_Format(t *testing.T) {
	got := mustFingerprint(t, fingerprintTestConfig("gcp"))
	if !strings.HasPrefix(got, "v1-") || len(got) != len("v1-")+32 {
		t.Fatalf("unexpected fingerprint format %q", got)
	}
}

func fingerprintTestConfig(providerType string) *Config {
	cfg := &Config{
		Name: "test",
		Provider: ProviderConfig{
			Type: providerType,
			GCP: GCPConfig{
				Project:        "project",
				Zone:           "zone-a",
				MachineType:    "e2-standard-4",
				DiskSizeGB:     30,
				DiskType:       "pd-balanced",
				ImageFamily:    "ubuntu-2204-lts",
				ImageProject:   "ubuntu-os-cloud",
				Network:        "network",
				Subnet:         "subnet",
				ServiceAccount: "default",
				Tags:           []string{"web"},
				Labels:         map[string]string{"managed-by": "mutapod", "team": "dev"},
			},
			Azure: AzureConfig{
				Tenant:            "tenant",
				Subscription:      "subscription",
				ResourceGroup:     "group",
				Location:          "westeurope",
				VMSize:            "Standard_D4s_v5",
				DiskSizeGB:        30,
				StorageSKU:        "StandardSSD_LRS",
				Image:             "Ubuntu2204",
				VNet:              "vnet",
				Subnet:            "subnet",
				AdminUsername:     "azureuser",
				Identity:          "identity",
				PublicIPSku:       "Standard",
				SSHSources:        []string{"10.1.0.0/24"},
				SSHPrivateKeyFile: "~/.ssh/id_rsa",
				SSHPublicKeyFile:  "~/.ssh/id_rsa.pub",
				Tags:              map[string]string{"managed-by": "mutapod", "team": "dev"},
			},
		},
	}
	return cfg
}

func mustFingerprint(t *testing.T, cfg *Config) string {
	t.Helper()
	got, err := cfg.VMConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return got
}
