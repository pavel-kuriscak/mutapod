package config

import (
	"encoding/hex"
	"hash/fnv"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
)

var (
	invalidInstanceToken = regexp.MustCompile(`[^a-z0-9]+`)
	gcloudAccountLookup  = lookupGCloudAccount
	azureAccountLookup   = lookupAzureAccount
	localUsernameLookup  = lookupLocalUsername
)

// InstanceName returns the cloud instance name derived from the active account
// and workspace name.
func (c *Config) InstanceName() string {
	owner := c.InstanceOwner
	if owner == "" {
		owner = detectInstanceOwner(c.Provider.Type)
		c.InstanceOwner = owner
	}
	return buildInstanceName(owner, c.Name)
}

func detectInstanceOwner(providerType string) string {
	if providerType == "gcp" {
		if account := gcloudAccountLookup(); account != "" {
			return sanitizeAccountToken(account)
		}
	}
	if providerType == "azure" {
		if account := azureAccountLookup(); account != "" {
			return sanitizeAccountToken(account)
		}
	}

	if username := localUsernameLookup(); username != "" {
		return sanitizeInstanceToken(username, "user")
	}
	return "user"
}

func sanitizeAccountToken(account string) string {
	localPart := account
	if at := strings.Index(localPart, "@"); at >= 0 {
		localPart = localPart[:at]
	}
	return sanitizeInstanceToken(localPart, "user")
}

func lookupGCloudAccount() string {
	for _, binary := range []string{"gcloud.cmd", "gcloud"} {
		path, err := exec.LookPath(binary)
		if err != nil {
			continue
		}
		out, err := exec.Command(path, "config", "get-value", "account").Output()
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(out))
		if value == "" || strings.EqualFold(value, "(unset)") {
			continue
		}
		return value
	}
	return ""
}

func lookupAzureAccount() string {
	for _, binary := range []string{"az.cmd", "az"} {
		path, err := exec.LookPath(binary)
		if err != nil {
			continue
		}
		out, err := exec.Command(path, "account", "show", "--query", "user.name", "--output", "tsv").Output()
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(out))
		if value == "" || strings.EqualFold(value, "null") {
			continue
		}
		return value
	}
	return ""
}

func lookupLocalUsername() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Username
	if idx := strings.LastIndex(name, `\`); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

func buildInstanceName(owner, workspace string) string {
	name := strings.Join([]string{
		"mutapod",
		sanitizeInstanceToken(owner, "user"),
		sanitizeInstanceToken(workspace, "workspace"),
	}, "-")
	if len(name) <= 63 {
		return name
	}

	sum := fnv.New32a()
	_, _ = sum.Write([]byte(name))
	hashBytes := make([]byte, 4)
	value := sum.Sum32()
	hashBytes[0] = byte(value >> 24)
	hashBytes[1] = byte(value >> 16)
	hashBytes[2] = byte(value >> 8)
	hashBytes[3] = byte(value)
	hash := strings.ToLower(hex.EncodeToString(hashBytes))

	prefixLimit := 63 - 1 - len(hash)
	prefix := strings.Trim(name[:prefixLimit], "-")
	if prefix == "" {
		prefix = "mutapod"
	}
	return prefix + "-" + hash
}

func sanitizeInstanceToken(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = invalidInstanceToken.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return fallback
	}
	return value
}
