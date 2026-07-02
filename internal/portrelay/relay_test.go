package portrelay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptIncludesRequestedPortsAndRelayProgram(t *testing.T) {
	script := Script([]int{9000, 8000, 8000})
	for _, needle := range []string{
		"ports='8000 9000'",
		"relay_dir=/tmp/mutapod-port-relays",
		"MUTAPOD_RELAY_PORT=\"$port\"",
		"server.bind((bind_ip, listen_port))",
		"wait_for_target()",
		"upstream.settimeout(None)",
		"127.0.0.1:{target_port}",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q:\n%s", needle, script)
		}
	}
}

func TestPythonRelayProgramCompiles(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		py, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skip("python not available")
	}

	path := filepath.Join(t.TempDir(), "relay.py")
	if err := os.WriteFile(path, []byte(pythonRelayProgram), 0644); err != nil {
		t.Fatalf("write relay: %v", err)
	}
	cmd := exec.Command(py, "-m", "py_compile", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("py_compile failed: %v\n%s", err, out)
	}
}

func TestScriptFallsBackToNoopForNoPorts(t *testing.T) {
	if got := Script(nil); got != ":" {
		t.Fatalf("Script(nil): got %q, want :", got)
	}
}

func TestNormalizePorts(t *testing.T) {
	got := normalizePorts([]int{0, 9000, 8000, 9000, 65536, -1})
	want := []int{8000, 9000}
	if len(got) != len(want) {
		t.Fatalf("normalizePorts: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizePorts: got %v, want %v", got, want)
		}
	}
}
