package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Override stateDir for tests by setting HOME to a temp dir.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := os.Getenv("HOME")
	// On Windows USERPROFILE is used; set both
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	_ = old
}

func TestSaveLoad(t *testing.T) {
	withTempHome(t)

	s := &State{
		SchemaVersion: SchemaVersion,
		Name:          "myapp",
		ProviderType:  "gcp",
		Instance: InstanceState{
			ID:                "projects/p/zones/z/instances/mutapod-myapp",
			Name:              "mutapod-myapp",
			TargetScope:       "gcp|projects/p/zones/z/instances/mutapod-myapp",
			ConfigFingerprint: "v1-abc",
		},
		SSH: SSHState{
			Host: "mutapod-myapp.us-central1-a.my-project",
			Port: 22,
			User: "root",
		},
		Sync: SyncState{
			Backend:         "mutagen",
			SessionName:     "mutapod-myapp",
			LocalPath:       "/home/user/projects/myapp",
			RemotePath:      "/workspace/myapp",
			SessionConfig:   "sig456",
			IgnoreSignature: "abc123",
		},
	}

	if err := Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load("myapp")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Name != "myapp" {
		t.Errorf("Name: got %q", loaded.Name)
	}
	if loaded.ProviderType != "gcp" {
		t.Errorf("ProviderType: got %q", loaded.ProviderType)
	}
	if loaded.Instance.ID != s.Instance.ID {
		t.Errorf("Instance.ID: got %q", loaded.Instance.ID)
	}
	if loaded.Instance.ConfigFingerprint != "v1-abc" {
		t.Errorf("Instance.ConfigFingerprint: got %q", loaded.Instance.ConfigFingerprint)
	}
	if loaded.Instance.TargetScope != s.Instance.TargetScope {
		t.Errorf("Instance.TargetScope: got %q", loaded.Instance.TargetScope)
	}
	if loaded.SSH.Host != s.SSH.Host {
		t.Errorf("SSH.Host: got %q", loaded.SSH.Host)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set after Save")
	}
	if loaded.Sync.IgnoreSignature != "abc123" {
		t.Errorf("Sync.IgnoreSignature: got %q", loaded.Sync.IgnoreSignature)
	}
	if loaded.Sync.SessionConfig != "sig456" {
		t.Errorf("Sync.SessionConfig: got %q", loaded.Sync.SessionConfig)
	}
}

func TestLoad_NotFound(t *testing.T) {
	withTempHome(t)

	s, err := Load("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "nonexistent" {
		t.Errorf("Name: got %q", s.Name)
	}
}

func TestDelete(t *testing.T) {
	withTempHome(t)

	s := &State{Name: "todelete", SchemaVersion: SchemaVersion}
	_ = Save(s)

	if err := Delete("todelete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Should return fresh state after deletion
	loaded, err := Load("todelete")
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if !loaded.UpdatedAt.IsZero() {
		t.Error("expected fresh state after deletion")
	}
}

func TestSave_UpdatesTimestamp(t *testing.T) {
	withTempHome(t)

	before := time.Now()
	s := &State{Name: "ts", SchemaVersion: SchemaVersion}
	_ = Save(s)
	after := time.Now()

	loaded, _ := Load("ts")
	if loaded.UpdatedAt.Before(before) || loaded.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt %v not in expected range [%v, %v]", loaded.UpdatedAt, before, after)
	}
}

func TestSave_CreatesDir(t *testing.T) {
	withTempHome(t)

	home, _ := os.UserHomeDir()
	stateDir := filepath.Join(home, ".mutapod", "state")

	// Should not exist yet
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Skip("state dir already exists")
	}

	s := &State{Name: "dirtest", SchemaVersion: SchemaVersion}
	if err := Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
}
