package sshrun

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestTrustHostSucceedsWhenHostKeyCapturedBeforeAuthReady(t *testing.T) {
	hostSigner := mustGenerateSigner(t)

	serverConfig := &gossh.ServerConfig{
		PublicKeyCallback: func(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, errors.New("auth not ready")
		},
	}
	serverConfig.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _, _ = gossh.NewServerConn(conn, serverConfig)
	}()

	identityFile := filepath.Join(t.TempDir(), "id_test")
	writePrivateKeyFile(t, identityFile)
	knownHostsFile := filepath.Join(t.TempDir(), "known_hosts")

	client := New("127.0.0.1", listener.Addr().(*net.TCPAddr).Port, "tester", identityFile)
	if err := client.TrustHost(knownHostsFile, "vm-alias"); err != nil {
		t.Fatalf("TrustHost: %v", err)
	}

	data, err := os.ReadFile(knownHostsFile)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if !strings.Contains(string(data), "vm-alias ") {
		t.Fatalf("known_hosts missing alias entry: %q", string(data))
	}

	<-done
}

func TestTrustHostReplacesStaleAliasAndPreservesOtherHosts(t *testing.T) {
	hostSigner := mustGenerateSigner(t)
	serverConfig := &gossh.ServerConfig{
		PublicKeyCallback: func(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, errors.New("auth not ready")
		},
	}
	serverConfig.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _, _ = gossh.NewServerConn(conn, serverConfig)
	}()

	identityFile := filepath.Join(t.TempDir(), "id_test")
	writePrivateKeyFile(t, identityFile)
	knownHostsFile := filepath.Join(t.TempDir(), "known_hosts")
	staleSigner := mustGenerateSigner(t)
	staleLine := knownhosts.Line([]string{"vm-alias"}, staleSigner.PublicKey())
	otherLine := knownhosts.Line([]string{"other-host"}, staleSigner.PublicKey())
	if err := os.WriteFile(knownHostsFile, []byte(otherLine+"\n"+staleLine+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	client := New("127.0.0.1", listener.Addr().(*net.TCPAddr).Port, "tester", identityFile)
	if err := client.TrustHost(knownHostsFile, "vm-alias"); err != nil {
		t.Fatalf("TrustHost: %v", err)
	}

	data, err := os.ReadFile(knownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	currentLine := knownhosts.Line([]string{"vm-alias"}, hostSigner.PublicKey())
	if strings.Contains(text, staleLine) {
		t.Fatalf("stale alias entry was preserved:\n%s", text)
	}
	if !strings.Contains(text, currentLine) {
		t.Fatalf("current alias entry missing:\n%s", text)
	}
	if !strings.Contains(text, otherLine) {
		t.Fatalf("unrelated host entry was removed:\n%s", text)
	}
	if strings.Count(text, "vm-alias ") != 1 {
		t.Fatalf("expected exactly one alias entry:\n%s", text)
	}

	<-done
}

func TestReplaceKnownHostAliasRecognizesMarkerAndHostLists(t *testing.T) {
	existing := strings.Join([]string{
		"@cert-authority vm-alias,other-host ssh-ed25519 old",
		"unrelated ssh-ed25519 keep",
		"",
	}, "\n")

	got := replaceKnownHostAlias([]byte(existing), "vm-alias", "vm-alias ssh-ed25519 new")
	if strings.Contains(got, "old") {
		t.Fatalf("stale marked entry remains:\n%s", got)
	}
	if !strings.Contains(got, "unrelated ssh-ed25519 keep") {
		t.Fatalf("unrelated entry removed:\n%s", got)
	}
	if !strings.Contains(got, "vm-alias ssh-ed25519 new") {
		t.Fatalf("replacement entry missing:\n%s", got)
	}
}

func TestIsTransientDialError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "windows connect timeout",
			err:  errors.New("dial tcp 34.90.155.38:22: connectex: A connection attempt failed because the connected party did not properly respond after a period of time"),
			want: true,
		},
		{
			name: "connection refused",
			err:  errors.New("dial tcp: connection refused"),
			want: true,
		},
		{
			name: "auth propagation",
			err:  errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"),
			want: true,
		},
		{
			name: "permanent",
			err:  errors.New("sshrun: parse private key: invalid format"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientDialError(tt.err); got != tt.want {
				t.Fatalf("isTransientDialError(%q): got %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func mustGenerateSigner(t *testing.T) gossh.Signer {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}

func writePrivateKeyFile(t *testing.T, path string) {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
}
