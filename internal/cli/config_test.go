package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigUsesProviderOverride(t *testing.T) {
	oldCfgFile := cfgFile
	oldProviderOverride := providerOverride
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		providerOverride = oldProviderOverride
	})

	dir := t.TempDir()
	cfgFile = filepath.Join(dir, "mutapod.yaml")
	providerOverride = "Azure"
	if err := os.WriteFile(cfgFile, []byte(`
name: test
provider:
  type: gcp
  gcp:
    project: proj
  azure:
    resource_group: rg-dev
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Provider.Type != "azure" {
		t.Fatalf("provider.type: got %q, want azure", cfg.Provider.Type)
	}
	if cfg.Provider.Azure.ResourceGroup != "rg-dev" {
		t.Fatalf("azure.resource_group: got %q", cfg.Provider.Azure.ResourceGroup)
	}
}
