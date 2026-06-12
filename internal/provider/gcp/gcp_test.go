package gcp

import (
	"context"
	"errors"
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
			Type: "gcp",
			GCP: config.GCPConfig{
				Project:      "my-project",
				Zone:         "us-central1-a",
				MachineType:  "n2-standard-4",
				DiskSizeGB:   50,
				DiskType:     "pd-balanced",
				ImageFamily:  "ubuntu-2204-lts",
				ImageProject: "ubuntu-os-cloud",
				Labels:       map[string]string{"managed-by": "mutapod"},
			},
		},
	}
}

func TestState_Running(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(`{"status":"RUNNING"}`, "gcloud",
		"compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
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
	f.StubErr(errors.New("The resource 'projects/p/zones/z/instances/"+instanceName+"' was not found"),
		"gcloud", "compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
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

func TestState_Stopped(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(`{"status":"TERMINATED"}`, "gcloud",
		"compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
	)

	p := New(testConfig(), f)
	state, _ := p.State(context.Background())
	if state != provider.StateStopped {
		t.Errorf("state: got %q, want %q", state, provider.StateStopped)
	}
}

func TestEnsureInstance_AlreadyRunning(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(`{"status":"RUNNING"}`, "gcloud",
		"compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
	)

	p := New(testConfig(), f)
	state, err := p.EnsureInstance(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != provider.StateRunning {
		t.Errorf("state: got %q", state)
	}
	// Should NOT have called create or start
	if f.CalledWith("gcloud", "compute", "instances", "create") {
		t.Error("should not have called create when already running")
	}
}

func TestEnsureInstance_CreateNew(t *testing.T) {
	f := shell.NewFakeCommander()
	cfg := testConfig()
	instanceName := cfg.InstanceName()
	fingerprint, err := cfg.VMConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// First call returns not found
	f.StubErr(errors.New("was not found"),
		"gcloud", "compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
	)

	p := New(cfg, f)

	// After create, State will be called again in waitForRunning.
	// The second describe call has the same key, so let's patch f to return RUNNING on second call.
	// We'll do this by replacing the fake after the first describe call triggers create.
	// Simpler: test that create was called, then separately test waitForRunning.
	// For this test, let's set up the commander so create succeeds and the poll returns RUNNING.
	// Since FakeCommander keys are exact, we need to clear the error and add a success stub.
	// In practice, let's just verify that create is attempted:
	ctx, cancel := context.WithTimeout(context.Background(), 100)
	defer cancel()
	_, _ = p.EnsureInstance(ctx) // Will timeout in waitForRunning — that's fine

	// Verify gcloud instances create was called with correct args
	if !f.CalledWith("gcloud", "compute", "instances", "create", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--machine-type", "n2-standard-4",
		"--boot-disk-size", "50GB",
		"--boot-disk-type", "pd-balanced",
		"--image-family", "ubuntu-2204-lts",
		"--image-project", "ubuntu-os-cloud",
		"--labels", "managed-by=mutapod,mutapod-config="+fingerprint,
		"--format", "json",
	) {
		t.Error("expected gcloud instances create to be called with correct args")
		for _, c := range f.Calls {
			t.Logf("  call: %s %v", c.Name, c.Args)
		}
	}
}

func TestEnsureInstance_StartStopped(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(`{"status":"TERMINATED"}`, "gcloud",
		"compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
	)

	p := New(testConfig(), f)
	ctx, cancel := context.WithTimeout(context.Background(), 100)
	defer cancel()
	_, _ = p.EnsureInstance(ctx)

	if !f.CalledWith("gcloud", "compute", "instances", "start", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
	) {
		t.Error("expected gcloud instances start to be called")
	}
}

func TestSSHConfig(t *testing.T) {
	oldTrustHost := trustHostFunc
	t.Cleanup(func() {
		trustHostFunc = oldTrustHost
	})
	trustHostFunc = func(client *sshrun.Client, knownHostsFile, hostKeyAlias string) error { return nil }

	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(
		`{"networkInterfaces":[{"accessConfigs":[{"natIP":"1.2.3.4"}]}]}`,
		"gcloud", "compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(networkInterfaces[0].accessConfigs[0].natIP)",
	)
	f.Stub(
		`ssh -i /tmp/google_compute_engine -o UserKnownHostsFile=/tmp/google_compute_known_hosts -o HostKeyAlias=compute.123 alice@1.2.3.4`,
		"gcloud", "compute", "ssh", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--dry-run",
	)
	p := New(testConfig(), f)

	sshCfg, err := p.SSHConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := instanceName + ".us-central1-a.my-project"; sshCfg.Host != want {
		t.Errorf("Host: got %q, want %q", sshCfg.Host, want)
	}
	if sshCfg.IP != "1.2.3.4" {
		t.Errorf("IP: got %q, want %q", sshCfg.IP, "1.2.3.4")
	}
	if sshCfg.Port != 22 {
		t.Errorf("Port: got %d, want 22", sshCfg.Port)
	}
	if sshCfg.User == "" {
		t.Error("User should not be empty")
	}
	if sshCfg.User != "alice" {
		t.Errorf("User: got %q, want %q", sshCfg.User, "alice")
	}
	if !f.CalledWith("gcloud", "compute", "config-ssh", "--project", "my-project") {
		t.Error("expected gcloud compute config-ssh to be called")
	}
}

func TestSSHConfigPrefersOpenSSHKeyWhenDryRunUsesPuttyPPK(t *testing.T) {
	oldTrustHost := trustHostFunc
	t.Cleanup(func() {
		trustHostFunc = oldTrustHost
	})
	trustHostFunc = func(client *sshrun.Client, knownHostsFile, hostKeyAlias string) error { return nil }

	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(
		`{"networkInterfaces":[{"accessConfigs":[{"natIP":"1.2.3.4"}]}]}`,
		"gcloud", "compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(networkInterfaces[0].accessConfigs[0].natIP)",
	)
	f.Stub(
		`"C:\Program Files (x86)\Google\Cloud SDK\google-cloud-sdk\bin\sdk\putty.exe" -t -i "C:\Users\Rezavec\.ssh\google_compute_engine.ppk" Rezavec@1.2.3.4`,
		"gcloud", "compute", "ssh", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--dry-run",
	)

	p := New(testConfig(), f)
	sshCfg, err := p.SSHConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sshCfg.User != "Rezavec" {
		t.Fatalf("User: got %q, want %q", sshCfg.User, "Rezavec")
	}
	if strings.HasSuffix(strings.ToLower(sshCfg.IdentityFile), ".ppk") {
		t.Fatalf("IdentityFile should not use .ppk: %q", sshCfg.IdentityFile)
	}
}

func TestParseSSHInvocationParsesUserAtHost(t *testing.T) {
	info, err := parseSSHInvocation(`ssh -i "C:\Users\Rezavec\.ssh\google_compute_engine" -o UserKnownHostsFile="C:\Users\Rezavec\.ssh\google_compute_known_hosts" -o HostKeyAlias=compute.220242 rezavec_gcp@34.90.170.209`)
	if err != nil {
		t.Fatalf("parseSSHInvocation: %v", err)
	}
	if info.User != "rezavec_gcp" {
		t.Fatalf("User: got %q, want %q", info.User, "rezavec_gcp")
	}
	if info.IdentityFile != `C:\Users\Rezavec\.ssh\google_compute_engine` {
		t.Fatalf("IdentityFile: got %q", info.IdentityFile)
	}
	if info.KnownHostsFile != `C:\Users\Rezavec\.ssh\google_compute_known_hosts` {
		t.Fatalf("KnownHostsFile: got %q", info.KnownHostsFile)
	}
	if info.HostKeyAlias != "compute.220242" {
		t.Fatalf("HostKeyAlias: got %q", info.HostKeyAlias)
	}
}

func TestParseSSHInvocationParsesDashLUser(t *testing.T) {
	info, err := parseSSHInvocation(`ssh -l oslogin-user -i /tmp/google_compute_engine -o UserKnownHostsFile=/tmp/known_hosts 34.90.170.209`)
	if err != nil {
		t.Fatalf("parseSSHInvocation: %v", err)
	}
	if info.User != "oslogin-user" {
		t.Fatalf("User: got %q, want %q", info.User, "oslogin-user")
	}
}

func TestStopInstance_WhenRunning(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(`{"status":"RUNNING"}`, "gcloud",
		"compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
	)

	p := New(testConfig(), f)
	if err := p.StopInstance(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !f.CalledWith("gcloud", "compute", "instances", "stop", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
	) {
		t.Error("expected gcloud instances stop to be called")
	}
}

func TestStopInstance_AlreadyStopped(t *testing.T) {
	f := shell.NewFakeCommander()
	instanceName := testConfig().InstanceName()
	f.Stub(`{"status":"TERMINATED"}`, "gcloud",
		"compute", "instances", "describe", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(status)",
	)

	p := New(testConfig(), f)
	if err := p.StopInstance(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// stop should NOT be called when already stopped
	if f.CalledWith("gcloud", "compute", "instances", "stop", instanceName,
		"--project", "my-project",
		"--zone", "us-central1-a",
	) {
		t.Error("should not call stop when already stopped")
	}
}

func TestDeleteInstance(t *testing.T) {
	f := shell.NewFakeCommander()
	p := New(testConfig(), f)

	if err := p.DeleteInstance(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.CalledWith("gcloud", "compute", "instances", "delete", testConfig().InstanceName(),
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--quiet",
	) {
		t.Error("expected gcloud instances delete to be called")
	}
}

func TestCopyFile_RequiresSSHConfig(t *testing.T) {
	// CopyFile uses pure-Go SSH; it requires SSHConfig to be called first.
	p := New(testConfig(), shell.NewFakeCommander())
	err := p.CopyFile(context.Background(), "/tmp/bootstrap.sh", "/tmp/bootstrap.sh")
	if err == nil {
		t.Fatal("expected error when SSHConfig not called, got nil")
	}
}

func TestInstanceID(t *testing.T) {
	p := New(testConfig(), shell.NewFakeCommander())
	want := "projects/my-project/zones/us-central1-a/instances/" + testConfig().InstanceName()
	got, err := p.InstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("InstanceID: got %q, want %q", got, want)
	}
}

func TestInstanceMetadata(t *testing.T) {
	cfg := testConfig()
	f := shell.NewFakeCommander()
	f.Stub(`{"labels":{"managed-by":"mutapod","mutapod-config":"v1-abc"}}`, "gcloud",
		"compute", "instances", "describe", cfg.InstanceName(),
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--format", "json(labels)",
	)

	metadata, err := New(cfg, f).InstanceMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ConfigFingerprint != "v1-abc" {
		t.Fatalf("fingerprint: got %q", metadata.ConfigFingerprint)
	}
	if metadata.ID != "projects/my-project/zones/us-central1-a/instances/"+cfg.InstanceName() {
		t.Fatalf("ID: got %q", metadata.ID)
	}
}

func TestAdoptInstance(t *testing.T) {
	cfg := testConfig()
	f := shell.NewFakeCommander()

	if err := New(cfg, f).AdoptInstance(context.Background(), "v1-abc"); err != nil {
		t.Fatal(err)
	}
	if !f.CalledWith("gcloud", "compute", "instances", "add-labels", cfg.InstanceName(),
		"--project", "my-project",
		"--zone", "us-central1-a",
		"--labels", "mutapod-config=v1-abc",
	) {
		t.Fatalf("unexpected calls: %#v", f.Calls)
	}
}

func TestFormatLabels(t *testing.T) {
	labels := map[string]string{"managed-by": "mutapod"}
	got := formatLabels(labels)
	if got != "managed-by=mutapod" {
		t.Errorf("formatLabels: got %q", got)
	}
}

func TestJoinShellArgs(t *testing.T) {
	got := joinShellArgs([]string{"bash", "-c", "cd '/workspace/testproject' && sudo docker compose -f 'compose.yaml' up -d --build"})
	want := "'bash' '-c' 'cd '\\''/workspace/testproject'\\'' && sudo docker compose -f '\\''compose.yaml'\\'' up -d --build'"
	if got != want {
		t.Errorf("joinShellArgs: got %q, want %q", got, want)
	}
}

func TestRetrySSHReadyRetriesTransientErrors(t *testing.T) {
	oldTimeout := sshReadyTimeout
	oldPeriod := sshReadyRetryPeriod
	sshReadyTimeout = 50 * time.Millisecond
	sshReadyRetryPeriod = time.Millisecond
	t.Cleanup(func() {
		sshReadyTimeout = oldTimeout
		sshReadyRetryPeriod = oldPeriod
	})

	attempts := 0
	err := retrySSHReady(context.Background(), "trust host key", func() error {
		attempts++
		if attempts < 3 {
			return errors.New("dial tcp: connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retrySSHReady: unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("retrySSHReady attempts: got %d, want 3", attempts)
	}
}

func TestIsSSHStartupErrorTreatsWindowsConnectTimeoutAsTransient(t *testing.T) {
	err := errors.New("sshrun: connect to capture host key: dial tcp 34.90.155.38:22: connectex: A connection attempt failed because the connected party did not properly respond after a period of time")
	if !isSSHStartupError(err) {
		t.Fatalf("expected Windows connect timeout to be transient")
	}
}

func TestRetrySSHReadyStopsOnNonTransientError(t *testing.T) {
	oldTimeout := sshReadyTimeout
	oldPeriod := sshReadyRetryPeriod
	sshReadyTimeout = 50 * time.Millisecond
	sshReadyRetryPeriod = time.Millisecond
	t.Cleanup(func() {
		sshReadyTimeout = oldTimeout
		sshReadyRetryPeriod = oldPeriod
	})

	attempts := 0
	err := retrySSHReady(context.Background(), "trust host key", func() error {
		attempts++
		return errors.New("some permanent ssh configuration error")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("retrySSHReady attempts: got %d, want 1", attempts)
	}
}

func TestRetrySSHReadyRetriesAuthDelayErrors(t *testing.T) {
	oldTimeout := sshReadyTimeout
	oldPeriod := sshReadyRetryPeriod
	sshReadyTimeout = 50 * time.Millisecond
	sshReadyRetryPeriod = time.Millisecond
	t.Cleanup(func() {
		sshReadyTimeout = oldTimeout
		sshReadyRetryPeriod = oldPeriod
	})

	attempts := 0
	err := retrySSHReady(context.Background(), "trust host key", func() error {
		attempts++
		if attempts < 3 {
			return errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retrySSHReady: unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("retrySSHReady attempts: got %d, want 3", attempts)
	}
}
