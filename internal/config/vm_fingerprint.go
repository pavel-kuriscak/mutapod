package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	VMConfigFingerprintKey     = "mutapod-config"
	vmConfigFingerprintVersion = "v1"
)

// VMConfigFingerprint returns a stable fingerprint of the active provider's
// VM-facing configuration. Cloud target selectors and local-only connection
// preferences are intentionally excluded.
func (c *Config) VMConfigFingerprint() (string, error) {
	var spec any
	switch c.Provider.Type {
	case "gcp":
		gcp := c.Provider.GCP
		provisioningModel := "standard"
		if gcp.Spot {
			provisioningModel = "spot"
		} else if gcp.Preemptible {
			provisioningModel = "preemptible"
		}
		spec = struct {
			MachineType      string            `json:"machine_type"`
			DiskSizeGB       int               `json:"disk_size_gb"`
			DiskType         string            `json:"disk_type"`
			ImageFamily      string            `json:"image_family"`
			ImageProject     string            `json:"image_project"`
			Network          string            `json:"network"`
			Subnet           string            `json:"subnet"`
			ServiceAccount   string            `json:"service_account"`
			Tags             []string          `json:"tags"`
			ProvisioningMode string            `json:"provisioning_model"`
			Labels           map[string]string `json:"labels"`
		}{
			MachineType:      gcp.MachineType,
			DiskSizeGB:       gcp.DiskSizeGB,
			DiskType:         gcp.DiskType,
			ImageFamily:      gcp.ImageFamily,
			ImageProject:     gcp.ImageProject,
			Network:          gcp.Network,
			Subnet:           gcp.Subnet,
			ServiceAccount:   gcp.ServiceAccount,
			Tags:             sortedUniqueStrings(gcp.Tags),
			ProvisioningMode: provisioningModel,
			Labels:           cloneStringMap(gcp.Labels),
		}
	case "azure":
		az := c.Provider.Azure
		spec = struct {
			Location          string            `json:"location"`
			VMSize            string            `json:"vm_size"`
			DiskSizeGB        int               `json:"disk_size_gb"`
			StorageSKU        string            `json:"storage_sku"`
			Image             string            `json:"image"`
			VNet              string            `json:"vnet"`
			Subnet            string            `json:"subnet"`
			AdminUsername     string            `json:"admin_username"`
			Identity          string            `json:"identity"`
			PublicIP          bool              `json:"public_ip"`
			PublicIPSku       string            `json:"public_ip_sku"`
			SSHPrivateKeyFile string            `json:"ssh_private_key_file"`
			SSHPublicKeyFile  string            `json:"ssh_public_key_file"`
			SSHSources        []string          `json:"ssh_sources"`
			Tags              map[string]string `json:"tags"`
		}{
			Location:          az.Location,
			VMSize:            az.VMSize,
			DiskSizeGB:        az.DiskSizeGB,
			StorageSKU:        az.StorageSKU,
			Image:             az.Image,
			VNet:              az.VNet,
			Subnet:            az.Subnet,
			AdminUsername:     az.AdminUsername,
			Identity:          az.Identity,
			PublicIP:          az.PublicIP,
			PublicIPSku:       az.PublicIPSku,
			SSHPrivateKeyFile: az.SSHPrivateKeyFile,
			SSHPublicKeyFile:  az.SSHPublicKeyFile,
			SSHSources:        sortedUniqueStrings(az.SSHSources),
			Tags:              cloneStringMap(az.Tags),
		}
	default:
		return "", fmt.Errorf("config: unsupported provider type %q", c.Provider.Type)
	}

	data, err := json.Marshal(struct {
		Version  string `json:"version"`
		Provider string `json:"provider"`
		Spec     any    `json:"spec"`
	}{
		Version:  vmConfigFingerprintVersion,
		Provider: c.Provider.Type,
		Spec:     spec,
	})
	if err != nil {
		return "", fmt.Errorf("config: marshal VM configuration: %w", err)
	}
	sum := sha256.Sum256(data)
	return vmConfigFingerprintVersion + "-" + hex.EncodeToString(sum[:16]), nil
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
