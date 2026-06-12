// Package sshrun runs commands and uploads files on a remote host using
// pure-Go SSH — no system SSH client or PuTTY required.
package sshrun

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mutapod/mutapod/internal/shell"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var (
	dialRetryTimeout  = 90 * time.Second
	dialRetryInterval = 2 * time.Second
)

// Client is a lightweight SSH client.
type Client struct {
	ip           string
	port         int
	user         string
	identityFile string
}

// New creates a Client that connects to ip:port with the given identity file.
func New(ip string, port int, user, identityFile string) *Client {
	return &Client{ip: ip, port: port, user: user, identityFile: identityFile}
}

func (c *Client) dial() (*gossh.Client, error) {
	key, err := os.ReadFile(c.identityFile)
	if err != nil {
		return nil, fmt.Errorf("sshrun: read identity file %q: %w", c.identityFile, err)
	}
	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("sshrun: parse private key: %w", err)
	}
	cfg := &gossh.ClientConfig{
		User:            c.user,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec — VM is ours
		Timeout:         30 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", c.ip, c.port)
	return gossh.Dial("tcp", addr, cfg)
}

// Run executes a shell command on the remote host.
func (c *Client) Run(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	conn, err := c.dialWithRetry(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepaliveDone:
				return
			case <-ticker.C:
				_, _, _ = conn.SendRequest("keepalive@openssh.com", true, nil)
			}
		}
	}()

	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("sshrun: new session: %w", err)
	}
	defer session.Close()

	session.Stdin = stdin
	session.Stdout = stdout
	session.Stderr = stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(gossh.SIGTERM)
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *Client) dialWithRetry(ctx context.Context) (*gossh.Client, error) {
	deadline := time.Now().Add(dialRetryTimeout)
	for {
		conn, err := c.dial()
		if err == nil {
			return conn, nil
		}
		if !isTransientDialError(err) || time.Now().After(deadline) {
			return nil, err
		}

		shell.Debugf("sshrun: SSH dial failed, retrying: %v", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(dialRetryInterval):
		}
	}
}

func isTransientDialError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection attempt failed") ||
		strings.Contains(msg, "failed to respond") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "actively refused") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "operation timed out") ||
		strings.Contains(msg, "permission denied (publickey)") ||
		strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "eof")
}

// TrustHost scans the remote server's host key and stores it in knownHostsFile
// under hostAlias. Existing entries for the alias are replaced when a recreated
// VM presents a different host key; unrelated entries are preserved.
//
// The SSH host key is exchanged before user authentication. On a fresh GCP VM
// the daemon may already be reachable while the injected SSH key is still
// propagating, so we treat "captured host key, auth failed" as success.
func (c *Client) TrustHost(knownHostsFile, hostAlias string) error {
	keyData, err := os.ReadFile(c.identityFile)
	if err != nil {
		return fmt.Errorf("sshrun: read identity file: %w", err)
	}
	signer, err := gossh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("sshrun: parse private key: %w", err)
	}

	var captured gossh.PublicKey
	cfg := &gossh.ClientConfig{
		User: c.user,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			captured = key
			return nil
		},
		Timeout: 30 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", c.ip, c.port)
	conn, err := net.DialTimeout("tcp", addr, cfg.Timeout)
	if err != nil {
		return fmt.Errorf("sshrun: connect to capture host key: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		return fmt.Errorf("sshrun: set deadline while capturing host key: %w", err)
	}
	sshConn, _, _, err := gossh.NewClientConn(conn, addr, cfg)
	if err != nil {
		if captured == nil {
			return fmt.Errorf("sshrun: connect to capture host key: %w", err)
		}
	} else {
		_ = sshConn.Close()
	}
	if captured == nil {
		return fmt.Errorf("sshrun: connect to capture host key: host key was not received")
	}

	if err := os.MkdirAll(filepath.Dir(knownHostsFile), 0700); err != nil {
		return fmt.Errorf("sshrun: create known_hosts dir: %w", err)
	}

	var existing []byte
	if data, err := os.ReadFile(knownHostsFile); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("sshrun: read known_hosts %s: %w", knownHostsFile, err)
	}

	entry := knownhosts.Line([]string{hostAlias}, captured)
	updated := replaceKnownHostAlias(existing, hostAlias, entry)
	if string(existing) == updated {
		return nil
	}
	if err := os.WriteFile(knownHostsFile, []byte(updated), 0600); err != nil {
		return fmt.Errorf("sshrun: write known_hosts %s: %w", knownHostsFile, err)
	}
	return nil
}

func replaceKnownHostAlias(existing []byte, hostAlias, entry string) string {
	text := strings.ReplaceAll(string(existing), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		if line == "" {
			continue
		}
		if !knownHostsLineContainsAlias(line, hostAlias) {
			filtered = append(filtered, line)
		}
	}
	filtered = append(filtered, entry)
	return strings.Join(filtered, "\n") + "\n"
}

func knownHostsLineContainsAlias(line, hostAlias string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	hostField := fields[0]
	if strings.HasPrefix(hostField, "@") {
		if len(fields) < 3 {
			return false
		}
		hostField = fields[1]
	}
	for _, host := range strings.Split(hostField, ",") {
		if host == hostAlias {
			return true
		}
	}
	return false
}

// Upload copies a local file to remotePath on the remote host by piping
// its content through `cat`. No SFTP or SCP binary required.
func (c *Client) Upload(ctx context.Context, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("sshrun: open %q: %w", localPath, err)
	}
	defer f.Close()

	return c.Run(ctx, "cat > "+shellQuote(remotePath), f, io.Discard, io.Discard)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
