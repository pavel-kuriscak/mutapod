// Package sshforward manages compressed OpenSSH local port forwarding sessions.
package sshforward

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
)

type Manager struct {
	cfg    *config.Config
	sshCfg *provider.SSHConfig
}

type record struct {
	Name      string    `json:"name"`
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Args      []string  `json:"args"`
	StartedAt time.Time `json:"started_at"`
}

func New(cfg *config.Config, sshCfg *provider.SSHConfig) *Manager {
	return &Manager{cfg: cfg, sshCfg: sshCfg}
}

func (m *Manager) SessionName(port int) string {
	return fmt.Sprintf("mutapod-%s-ssh-%d", m.cfg.Name, port)
}

func (m *Manager) Ensure(port int) error {
	_ = m.Stop(port)

	args := m.CommandArgs(port)
	cmd := exec.Command("ssh", args...)
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd.Stdin = nil
	cmd.Stdout = devNull
	cmd.Stderr = devNull

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ssh forward %d: start: %w", port, err)
	}
	if err := writeRecord(m.recordPath(port), record{
		Name:      m.SessionName(port),
		PID:       cmd.Process.Pid,
		Port:      port,
		Args:      args,
		StartedAt: time.Now(),
	}); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	if err := waitForLocalListener(port, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(m.recordPath(port))
		return err
	}
	return cmd.Process.Release()
}

func (m *Manager) Stop(port int) error {
	rec, err := readRecord(m.recordPath(port))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if proc, err := os.FindProcess(rec.PID); err == nil {
		_ = proc.Kill()
	}
	return os.Remove(m.recordPath(port))
}

func (m *Manager) StopAll(ports []int) {
	for _, port := range ports {
		_ = m.Stop(port)
	}
}

func (m *Manager) CommandArgs(port int) []string {
	args := []string{
		"-N",
		"-C",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	if m.sshCfg.Port > 0 && m.sshCfg.Port != 22 {
		args = append(args, "-p", strconv.Itoa(m.sshCfg.Port))
	}
	args = append(args,
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port),
		fmt.Sprintf("%s@%s", m.sshCfg.User, m.sshCfg.Host),
	)
	return args
}

func (m *Manager) recordPath(port int) string {
	return filepath.Join(recordDir(), sanitize(m.cfg.Name)+"-"+strconv.Itoa(port)+".json")
}

func recordDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mutapod", "forwards")
	}
	return filepath.Join(home, ".mutapod", "forwards")
}

func writeRecord(path string, rec record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("ssh forward: create record dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("ssh forward: marshal record: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func readRecord(path string) (record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return record{}, fmt.Errorf("ssh forward: parse record %s: %w", path, err)
	}
	return rec, nil
}

func waitForLocalListener(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	address := fmt.Sprintf("127.0.0.1:%d", port)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("ssh forward: local listener %s not ready: %w", address, lastErr)
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "workspace"
	}
	return b.String()
}
