package sshforward

import (
	"reflect"
	"testing"

	"github.com/mutapod/mutapod/internal/config"
	"github.com/mutapod/mutapod/internal/provider"
)

func TestCommandArgs_DefaultPort(t *testing.T) {
	manager := New(&config.Config{Name: "app"}, &provider.SSHConfig{
		User: "alice",
		Host: "example-host",
	})

	got := manager.CommandArgs(8000)
	want := []string{
		"-N",
		"-C",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-L", "127.0.0.1:8000:127.0.0.1:8000",
		"alice@example-host",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandArgs: got %#v, want %#v", got, want)
	}
}

func TestCommandArgs_CustomSSHPort(t *testing.T) {
	manager := New(&config.Config{Name: "app"}, &provider.SSHConfig{
		User: "alice",
		Host: "example-host",
		Port: 2222,
	})

	got := manager.CommandArgs(5000)
	want := []string{
		"-N",
		"-C",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-p", "2222",
		"-L", "127.0.0.1:5000:127.0.0.1:5000",
		"alice@example-host",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandArgs: got %#v, want %#v", got, want)
	}
}
