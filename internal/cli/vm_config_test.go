package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
)

type declarativeTestProvider struct {
	state       provider.InstanceState
	metadata    provider.InstanceMetadata
	id          string
	adopted     string
	deleteCount int
}

func (p *declarativeTestProvider) Name() string { return "gcp" }
func (p *declarativeTestProvider) EnsureInstance(context.Context) (provider.InstanceState, error) {
	return p.state, nil
}
func (p *declarativeTestProvider) State(context.Context) (provider.InstanceState, error) {
	return p.state, nil
}
func (p *declarativeTestProvider) InstanceMetadata(context.Context) (provider.InstanceMetadata, error) {
	return p.metadata, nil
}
func (p *declarativeTestProvider) AdoptInstance(_ context.Context, fingerprint string) error {
	p.adopted = fingerprint
	return nil
}
func (p *declarativeTestProvider) InstanceID(context.Context) (string, error) {
	return p.id, nil
}
func (p *declarativeTestProvider) SSHConfig(context.Context) (*provider.SSHConfig, error) {
	return &provider.SSHConfig{}, nil
}
func (p *declarativeTestProvider) Exec(context.Context, []string, provider.ExecOptions) error {
	return nil
}
func (p *declarativeTestProvider) CopyFile(context.Context, string, string) error {
	return nil
}
func (p *declarativeTestProvider) PreferredSyncBackend() provider.SyncBackend {
	return provider.SyncMutagen
}
func (p *declarativeTestProvider) StopInstance(context.Context) error { return nil }
func (p *declarativeTestProvider) DeleteInstance(context.Context) error {
	p.deleteCount++
	p.state = provider.StateNotFound
	return nil
}
func (p *declarativeTestProvider) ForwardedWorkspacePath() string { return "/workspace/test" }

func TestPrepareDeclarativeVMMatchingFingerprintReusesVM(t *testing.T) {
	cfg := declarativeTestConfig()
	fingerprint, _ := cfg.VMConfigFingerprint()
	prov := declarativeProvider(fingerprint)

	got, err := prepareDeclarativeVM(context.Background(), cfg, prov, freshState(cfg.Name), testVMOptions(""))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || prov.deleteCount != 0 || prov.adopted != "" {
		t.Fatalf("unexpected result: state=%#v provider=%#v", got, prov)
	}
}

func TestPrepareDeclarativeVMLegacyAdoption(t *testing.T) {
	cfg := declarativeTestConfig()
	prov := declarativeProvider("")
	opts := testVMOptions("")
	opts.Adopt = true

	if _, err := prepareDeclarativeVM(context.Background(), cfg, prov, freshState(cfg.Name), opts); err != nil {
		t.Fatal(err)
	}
	want, _ := cfg.VMConfigFingerprint()
	if prov.adopted != want || prov.deleteCount != 0 {
		t.Fatalf("adopted=%q deletes=%d", prov.adopted, prov.deleteCount)
	}
}

func TestPrepareDeclarativeVMChangedFingerprintRejectsAdoption(t *testing.T) {
	cfg := declarativeTestConfig()
	prov := declarativeProvider("v1-old")
	opts := testVMOptions("")
	opts.Adopt = true

	_, err := prepareDeclarativeVM(context.Background(), cfg, prov, freshState(cfg.Name), opts)
	if err == nil || !strings.Contains(err.Error(), "use --replace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareDeclarativeVMNoninteractiveRequiresReplaceFlag(t *testing.T) {
	cfg := declarativeTestConfig()
	prov := declarativeProvider("v1-old")

	_, err := prepareDeclarativeVM(context.Background(), cfg, prov, freshState(cfg.Name), testVMOptions(""))
	if err == nil || !strings.Contains(err.Error(), "--replace") || prov.deleteCount != 0 {
		t.Fatalf("error=%v deletes=%d", err, prov.deleteCount)
	}
}

func TestPrepareDeclarativeVMDeclinedReplacementLeavesVM(t *testing.T) {
	cfg := declarativeTestConfig()
	prov := declarativeProvider("v1-old")
	opts := testVMOptions("no\n")
	opts.Interactive = true

	_, err := prepareDeclarativeVM(context.Background(), cfg, prov, freshState(cfg.Name), opts)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || prov.deleteCount != 0 {
		t.Fatalf("error=%v deletes=%d", err, prov.deleteCount)
	}
}

func TestPrepareDeclarativeVMReplaceFlagDeletesVM(t *testing.T) {
	oldCleanup := cleanupLocalWorkspaceForVM
	oldInspect := inspectReplacementLeasesForVM
	t.Cleanup(func() {
		cleanupLocalWorkspaceForVM = oldCleanup
		inspectReplacementLeasesForVM = oldInspect
	})
	cleanupLocalWorkspaceForVM = func(context.Context, *config.Config) error { return nil }
	inspectReplacementLeasesForVM = func(context.Context, provider.Provider, provider.InstanceState, *config.Config, io.Writer) {}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	cfg := declarativeTestConfig()
	prov := declarativeProvider("v1-old")
	opts := testVMOptions("")
	opts.Replace = true

	got, err := prepareDeclarativeVM(context.Background(), cfg, prov, freshState(cfg.Name), opts)
	if err != nil {
		t.Fatal(err)
	}
	if prov.deleteCount != 1 || got.Instance.ID != "" {
		t.Fatalf("deletes=%d state=%#v", prov.deleteCount, got)
	}
}

func TestTargetChanged(t *testing.T) {
	cfg := declarativeTestConfig()
	st := freshState(cfg.Name)
	st.ProviderType = "gcp"
	st.Instance.ID = "projects/old/zones/a/instances/test"
	if !targetChanged(cfg, st, "projects/new/zones/a/instances/test") {
		t.Fatal("expected changed resource ID to change target")
	}
	st.Instance.TargetScope = "gcp|" + st.Instance.ID
	if !targetChanged(cfg, st, "projects/new/zones/a/instances/test") {
		t.Fatal("expected changed stored target scope to change target")
	}
	st.ProviderType = "azure"
	if !targetChanged(cfg, st, st.Instance.ID) {
		t.Fatal("expected provider change to change target")
	}
}

func TestTargetChangedIncludesAzureTenant(t *testing.T) {
	cfg := declarativeTestConfig()
	cfg.Provider.Type = "azure"
	cfg.Provider.Azure.Tenant = "tenant-new"
	id := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm"
	st := freshState(cfg.Name)
	st.ProviderType = "azure"
	st.Instance.ID = id
	st.Instance.TargetScope = "azure|tenant-old|" + id

	if !targetChanged(cfg, st, id) {
		t.Fatal("expected Azure tenant change to change target scope")
	}
}

func declarativeTestConfig() *config.Config {
	return &config.Config{
		Name:          "test",
		InstanceOwner: "tester",
		Provider: config.ProviderConfig{
			Type: "gcp",
			GCP: config.GCPConfig{
				Project:      "project",
				Zone:         "zone",
				MachineType:  "e2-standard-4",
				DiskSizeGB:   30,
				DiskType:     "pd-balanced",
				ImageFamily:  "ubuntu-2204-lts",
				ImageProject: "ubuntu-os-cloud",
				Labels:       map[string]string{"managed-by": "mutapod"},
			},
		},
	}
}

func declarativeProvider(fingerprint string) *declarativeTestProvider {
	id := "projects/project/zones/zone/instances/mutapod-tester-test"
	return &declarativeTestProvider{
		state: provider.StateRunning,
		metadata: provider.InstanceMetadata{
			ID:                id,
			ConfigFingerprint: fingerprint,
		},
		id: id,
	}
}

func testVMOptions(input string) vmUpOptions {
	return vmUpOptions{
		In:  strings.NewReader(input),
		Out: &strings.Builder{},
	}
}
