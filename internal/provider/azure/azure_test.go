package azure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
	"github.com/mutapod/mutapod/internal/shell"
	"github.com/mutapod/mutapod/internal/sshrun"
)

func testConfig() *config.Config {
	return &config.Config{
		Name:          "myapp",
		InstanceOwner: "tester",
		Provider: config.ProviderConfig{
			Type: "azure",
			Azure: config.AzureConfig{
				Subscription:  "sub-123",
				ResourceGroup: "rg-dev",
				Location:      "westeurope",
				VMSize:        "Standard_D4s_v5",
				DiskSizeGB:    64,
				StorageSKU:    "StandardSSD_LRS",
				Image:         "Ubuntu2204",
				VNet:          "dev-vnet",
				Subnet:        "dev-subnet",
				AdminUsername: "azureuser",
				PublicIPSku:   "Standard",
				Tags:          map[string]string{"managed-by": "mutapod"},
			},
		},
	}
}

func TestState_Running(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub("VM running\n", "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--show-details",
		"--query", "powerState",
		"--output", "tsv",
		"--subscription", "sub-123",
	)

	p := New(testConfig(), f)
	state, err := p.State(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != provider.StateRunning {
		t.Errorf("state: got %q, want %q", state, provider.StateRunning)
	}
}

func TestState_NotFound(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.StubErr(errors.New("(ResourceNotFound) The Resource was not found"), "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--show-details",
		"--query", "powerState",
		"--output", "tsv",
		"--subscription", "sub-123",
	)

	p := New(testConfig(), f)
	state, err := p.State(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != provider.StateNotFound {
		t.Errorf("state: got %q, want %q", state, provider.StateNotFound)
	}
}

func TestEnsureInstance_CreateNew(t *testing.T) {
	home := tempHome(t)
	privateKey := filepath.Join(home, ".ssh", "id_rsa")
	publicKey := privateKey + ".pub"
	if err := os.MkdirAll(filepath.Dir(privateKey), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 test"), 0600); err != nil {
		t.Fatal(err)
	}

	f := shell.NewFakeCommander()
	cfg := testConfig()
	instanceName := cfg.InstanceName()
	fingerprint, err := cfg.VMConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f.StubErr(errors.New("not found"), "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--show-details",
		"--query", "powerState",
		"--output", "tsv",
		"--subscription", "sub-123",
	)

	p := New(cfg, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = p.EnsureInstance(ctx)

	if !f.CalledWith("az", "vm", "create",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--size", "Standard_D4s_v5",
		"--image", "Ubuntu2204",
		"--admin-username", "azureuser",
		"--authentication-type", "ssh",
		"--os-disk-size-gb", "64",
		"--os-disk-delete-option", "Delete",
		"--nic-delete-option", "Delete",
		"--storage-sku", "StandardSSD_LRS",
		"--public-ip-address", "",
		"--nsg-rule", "NONE",
		"--location", "westeurope",
		"--ssh-key-values", "@"+publicKey,
		"--vnet-name", "dev-vnet",
		"--subnet", "dev-subnet",
		"--tags", "managed-by=mutapod", "mutapod-config="+fingerprint,
		"--subscription", "sub-123",
		"--output", "json",
	) {
		t.Error("expected az vm create to be called with correct args")
		for _, c := range f.Calls {
			t.Logf("  call: %s %v", c.Name, c.Args)
		}
	}
}

func TestEnsureInstance_CreateNewWithPublicIP(t *testing.T) {
	home := tempHome(t)
	privateKey := filepath.Join(home, ".ssh", "id_rsa")
	publicKey := privateKey + ".pub"
	if err := os.MkdirAll(filepath.Dir(privateKey), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 test"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig()
	cfg.Provider.Azure.PublicIP = true
	fingerprint, err := cfg.VMConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}

	f := shell.NewFakeCommander()
	instanceName := cfg.InstanceName()
	f.StubErr(errors.New("not found"), "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--show-details",
		"--query", "powerState",
		"--output", "tsv",
		"--subscription", "sub-123",
	)

	p := New(cfg, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = p.EnsureInstance(ctx)

	if !f.CalledWith("az", "vm", "create",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--size", "Standard_D4s_v5",
		"--image", "Ubuntu2204",
		"--admin-username", "azureuser",
		"--authentication-type", "ssh",
		"--os-disk-size-gb", "64",
		"--os-disk-delete-option", "Delete",
		"--nic-delete-option", "Delete",
		"--storage-sku", "StandardSSD_LRS",
		"--public-ip-sku", "Standard",
		"--nsg-rule", "SSH",
		"--location", "westeurope",
		"--ssh-key-values", "@"+publicKey,
		"--vnet-name", "dev-vnet",
		"--subnet", "dev-subnet",
		"--tags", "managed-by=mutapod", "mutapod-config="+fingerprint,
		"--subscription", "sub-123",
		"--output", "json",
	) {
		t.Error("expected az vm create to be called with public IP args")
		for _, c := range f.Calls {
			t.Logf("  call: %s %v", c.Name, c.Args)
		}
	}
}

func TestSSHConfig(t *testing.T) {
	oldTrustHost := trustHostFunc
	t.Cleanup(func() {
		trustHostFunc = oldTrustHost
	})
	trustHostFunc = func(client *sshrun.Client, knownHostsFile, hostKeyAlias string) error { return nil }

	home := tempHome(t)
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub("10.1.2.3\n", "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--show-details",
		"--query", "privateIps",
		"--output", "tsv",
		"--subscription", "sub-123",
	)

	p := New(testConfig(), f)
	sshCfg, err := p.SSHConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := instanceName + ".azure"; sshCfg.Host != want {
		t.Errorf("Host: got %q, want %q", sshCfg.Host, want)
	}
	if sshCfg.IP != "10.1.2.3" {
		t.Errorf("IP: got %q, want %q", sshCfg.IP, "10.1.2.3")
	}
	if sshCfg.User != "azureuser" {
		t.Errorf("User: got %q, want azureuser", sshCfg.User)
	}
	if sshCfg.IdentityFile != filepath.Join(home, ".ssh", "id_rsa") {
		t.Errorf("IdentityFile: got %q", sshCfg.IdentityFile)
	}

	configData, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		t.Fatalf("read ssh config: %v", err)
	}
	configText := string(configData)
	for _, want := range []string{
		"Host " + instanceName + ".azure",
		"HostName 10.1.2.3",
		"User azureuser",
		"IdentityFile " + filepath.ToSlash(filepath.Join(home, ".ssh", "id_rsa")),
		"UserKnownHostsFile " + filepath.ToSlash(filepath.Join(home, ".ssh", "known_hosts")),
		"HostKeyAlias " + instanceName + ".azure",
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("ssh config missing %q:\n%s", want, configText)
		}
	}
}

func TestSSHConfigPreferPrivateIP(t *testing.T) {
	oldTrustHost := trustHostFunc
	t.Cleanup(func() {
		trustHostFunc = oldTrustHost
	})
	trustHostFunc = func(client *sshrun.Client, knownHostsFile, hostKeyAlias string) error { return nil }

	tempHome(t)
	cfg := testConfig()
	cfg.Provider.Azure.PreferPrivateIP = true

	f := shell.NewFakeCommander()
	instanceName := cfg.InstanceName()
	f.Stub("10.1.2.3\n", "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", instanceName,
		"--show-details",
		"--query", "privateIps",
		"--output", "tsv",
		"--subscription", "sub-123",
	)

	p := New(cfg, f)
	sshCfg, err := p.SSHConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sshCfg.IP != "10.1.2.3" {
		t.Fatalf("IP: got %q, want 10.1.2.3", sshCfg.IP)
	}
}

func TestExecTtyUsesAzureSSH(t *testing.T) {
	tempHome(t)
	f := shell.NewFakeCommander()
	p := New(testConfig(), f)

	if err := p.Exec(context.Background(), nil, provider.ExecOptions{Tty: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !f.CalledWith("az", "ssh", "vm",
		"--resource-group", "rg-dev",
		"--name", testConfig().InstanceName(),
		"--local-user", "azureuser",
		"--private-key-file", filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa"),
		"--subscription", "sub-123",
	) {
		t.Error("expected az ssh vm to be called")
		for _, c := range f.Calls {
			t.Logf("  call: %s %v", c.Name, c.Args)
		}
	}
}

func TestCopyFile_RequiresSSHConfig(t *testing.T) {
	p := New(testConfig(), shell.NewFakeCommander())
	err := p.CopyFile(context.Background(), "/tmp/bootstrap.sh", "/tmp/bootstrap.sh")
	if err == nil {
		t.Fatal("expected error when SSHConfig not called, got nil")
	}
}

func TestEnsureSSHNSGRuleCreatesRuleWhenMissing(t *testing.T) {
	cfg := testConfig()
	cfg.Provider.Azure.SSHSources = []string{"10.130.1.0/27"}
	f := shell.NewFakeCommander()
	instanceName := cfg.InstanceName()
	f.StubErr(errors.New("not found"), "az",
		"network", "nsg", "rule", "show",
		"--resource-group", "rg-dev",
		"--nsg-name", instanceName+"NSG",
		"--name", "mutapod-ssh",
		"--output", "none",
		"--subscription", "sub-123",
	)

	p := New(cfg, f)
	if err := p.ensureSSHNSGRule(context.Background()); err != nil {
		t.Fatalf("ensureSSHNSGRule: %v", err)
	}

	if !f.CalledWith("az",
		"network", "nsg", "rule", "create",
		"--resource-group", "rg-dev",
		"--nsg-name", instanceName+"NSG",
		"--name", "mutapod-ssh",
		"--priority", "1000",
		"--direction", "Inbound",
		"--access", "Allow",
		"--protocol", "Tcp",
		"--source-address-prefixes", "10.130.1.0/27",
		"--source-port-ranges", "*",
		"--destination-address-prefixes", "*",
		"--destination-port-ranges", "22",
		"--description", "Allow mutapod SSH from configured private sources",
		"--subscription", "sub-123",
	) {
		t.Error("expected az network nsg rule create to be called")
		for _, c := range f.Calls {
			t.Logf("  call: %s %v", c.Name, c.Args)
		}
	}
}

func TestEnsureSSHNSGRuleUpdatesExistingRule(t *testing.T) {
	cfg := testConfig()
	cfg.Provider.Azure.SSHSources = []string{"10.130.1.0/27", "10.130.2.0/27"}
	f := shell.NewFakeCommander()
	instanceName := cfg.InstanceName()

	p := New(cfg, f)
	if err := p.ensureSSHNSGRule(context.Background()); err != nil {
		t.Fatalf("ensureSSHNSGRule: %v", err)
	}

	if !f.CalledWith("az",
		"network", "nsg", "rule", "update",
		"--resource-group", "rg-dev",
		"--nsg-name", instanceName+"NSG",
		"--name", "mutapod-ssh",
		"--priority", "1000",
		"--direction", "Inbound",
		"--access", "Allow",
		"--protocol", "Tcp",
		"--source-address-prefixes", "10.130.1.0/27", "10.130.2.0/27",
		"--source-port-ranges", "*",
		"--destination-address-prefixes", "*",
		"--destination-port-ranges", "22",
		"--description", "Allow mutapod SSH from configured private sources",
		"--subscription", "sub-123",
	) {
		t.Fatalf("expected NSG rule update, got %#v", f.Calls)
	}
}

func TestEnsureSSHNSGRuleRemovesManagedRuleWhenSourcesEmpty(t *testing.T) {
	cfg := testConfig()
	f := shell.NewFakeCommander()
	instanceName := cfg.InstanceName()

	p := New(cfg, f)
	if err := p.ensureSSHNSGRule(context.Background()); err != nil {
		t.Fatalf("ensureSSHNSGRule: %v", err)
	}

	if !f.CalledWith("az",
		"network", "nsg", "rule", "delete",
		"--resource-group", "rg-dev",
		"--nsg-name", instanceName+"NSG",
		"--name", "mutapod-ssh",
		"--subscription", "sub-123",
	) {
		t.Fatalf("expected managed NSG rule delete, got %#v", f.Calls)
	}
}

func TestIsSSHStartupErrorTreatsWindowsConnectTimeoutAsTransient(t *testing.T) {
	err := errors.New("sshrun: connect to capture host key: dial tcp 10.150.170.36:22: connectex: A connection attempt failed because the connected party did not properly respond after a period of time")
	if !isSSHStartupError(err) {
		t.Fatalf("expected Windows connect timeout to be transient")
	}
}

func TestEnsureSSHConfigEntryReplacesExistingBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("Host old\n    HostName old.example\n\nHost vm.azure\n    HostName stale\n"), 0600); err != nil {
		t.Fatal(err)
	}

	err := ensureSSHConfigEntry(path, sshConfigEntry{
		Alias:          "vm.azure",
		HostName:       "1.2.3.4",
		User:           "azureuser",
		Port:           22,
		IdentityFile:   filepath.Join(dir, "id_rsa"),
		KnownHostsFile: filepath.Join(dir, "known_hosts"),
		HostKeyAlias:   "vm.azure",
	})
	if err != nil {
		t.Fatalf("ensureSSHConfigEntry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "HostName stale") {
		t.Fatalf("stale host block was not replaced:\n%s", text)
	}
	if strings.Count(text, "Host vm.azure") != 1 {
		t.Fatalf("expected one vm.azure block:\n%s", text)
	}
	if !strings.Contains(text, "Host old") {
		t.Fatalf("unrelated host block was removed:\n%s", text)
	}
}

func TestInstanceID(t *testing.T) {
	p := New(testConfig(), shell.NewFakeCommander())
	want := "/subscriptions/sub-123/resourceGroups/rg-dev/providers/Microsoft.Compute/virtualMachines/" + testConfig().InstanceName()
	got, err := p.InstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("InstanceID: got %q, want %q", got, want)
	}
}

func TestInstanceIDUsesActiveSubscription(t *testing.T) {
	cfg := testConfig()
	cfg.Provider.Azure.Subscription = ""
	f := shell.NewFakeCommander()
	f.Stub("active-sub\n", "az", "account", "show", "--query", "id", "--output", "tsv")

	got, err := New(cfg, f).InstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "/subscriptions/active-sub/resourceGroups/rg-dev/providers/Microsoft.Compute/virtualMachines/" + cfg.InstanceName()
	if got != want {
		t.Fatalf("InstanceID: got %q, want %q", got, want)
	}
}

func TestInstanceMetadata(t *testing.T) {
	cfg := testConfig()
	f := shell.NewFakeCommander()
	resourceID := "/subscriptions/sub-123/resourceGroups/rg-dev/providers/Microsoft.Compute/virtualMachines/" + cfg.InstanceName()
	f.Stub(`{"id":"`+resourceID+`","tags":{"managed-by":"mutapod","MUTAPOD-CONFIG":"v1-abc"}}`, "az",
		"vm", "show",
		"--resource-group", "rg-dev",
		"--name", cfg.InstanceName(),
		"--query", "{id:id,tags:tags}",
		"--output", "json",
		"--subscription", "sub-123",
	)

	metadata, err := New(cfg, f).InstanceMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ID != resourceID || metadata.ConfigFingerprint != "v1-abc" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestAdoptInstance(t *testing.T) {
	cfg := testConfig()
	f := shell.NewFakeCommander()
	resourceID := "/subscriptions/sub-123/resourceGroups/rg-dev/providers/Microsoft.Compute/virtualMachines/" + cfg.InstanceName()

	if err := New(cfg, f).AdoptInstance(context.Background(), "v1-abc"); err != nil {
		t.Fatal(err)
	}
	if !f.CalledWith("az", "tag", "update",
		"--resource-id", resourceID,
		"--operation", "Merge",
		"--tags", "mutapod-config=v1-abc",
		"--subscription", "sub-123",
	) {
		t.Fatalf("unexpected calls: %#v", f.Calls)
	}
}

func tempHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}
