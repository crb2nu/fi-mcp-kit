package process

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	mcp "gitlab.flexinfer.ai/libs/mcp-go"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHProcess represents an MCP server running via SSH.
type SSHProcess struct {
	Name         string
	Transport    mcp.Transport
	StartedAt    time.Time
	LastActivity time.Time
	client       *ssh.Client
	session      *ssh.Session
	stdin        io.WriteCloser
	stdout       io.ReadCloser
}

// startSSH starts an MCP server process via SSH.
func (m *Manager) startSSH(ctx context.Context, serverName string, spec *registry.TargetSpec) (*Process, error) {
	sshSpec := spec.SSH
	if sshSpec == nil {
		return nil, fmt.Errorf("no SSH configuration for server %s", serverName)
	}

	// Build auth methods
	authMethods, err := m.buildSSHAuthMethods(sshSpec)
	if err != nil {
		return nil, fmt.Errorf("build ssh auth methods: %w", err)
	}

	// Build host key callback
	hostKeyCallback, err := m.buildHostKeyCallback(sshSpec)
	if err != nil {
		return nil, fmt.Errorf("build host key callback: %w", err)
	}

	// Determine user
	user := sshSpec.User
	if user == "" {
		user = os.Getenv("USER")
	}

	// Determine timeout
	timeout := 30 * time.Second
	if sshSpec.ConnectTimeout > 0 {
		timeout = time.Duration(sshSpec.ConnectTimeout) * time.Second
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	// Connect to SSH server
	host := sshSpec.Host
	if !strings.Contains(host, ":") {
		host = host + ":22"
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("dial ssh %s: %w", host, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, host, config)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)

	// Create session
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create ssh session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutReader, err := session.StdoutPipe()
	if err != nil {
		stdin.Close()
		session.Close()
		client.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Wrap reader as ReadCloser (session.Close handles cleanup)
	stdout := io.NopCloser(stdoutReader)

	// Build command with expanded variables
	command := m.expandFunc(spec.Command)
	args := make([]string, len(spec.Args))
	for i, arg := range spec.Args {
		args[i] = m.expandFunc(fmt.Sprint(arg))
	}
	fullCommand := command
	if len(args) > 0 {
		fullCommand = command + " " + strings.Join(args, " ")
	}

	// Build env prefix
	var envPrefix string
	for k, v := range spec.Env {
		expanded := m.expandFunc(v)
		envPrefix += fmt.Sprintf("%s=%q ", k, expanded)
	}
	if envPrefix != "" {
		fullCommand = envPrefix + fullCommand
	}

	// Start remote command
	if err := session.Start(fullCommand); err != nil {
		stdin.Close()
		session.Close()
		client.Close()
		return nil, fmt.Errorf("start remote command: %w", err)
	}

	now := time.Now()
	proc := &Process{
		Name:         serverName,
		Cmd:          nil, // No local command
		Transport:    mcp.NewStdioTransport(stdout, stdin),
		StartedAt:    now,
		LastActivity: now,
		stdin:        stdin,
		stdout:       stdout,
		// SSH-specific fields stored in sshProcess
		sshClient:  client,
		sshSession: session,
	}

	return proc, nil
}

// buildSSHAuthMethods builds authentication methods for SSH connection.
func (m *Manager) buildSSHAuthMethods(spec *registry.SSHSpec) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// Use SSH agent if enabled (default: true)
	useAgent := true
	if spec.UseAgent != nil {
		useAgent = *spec.UseAgent
	}

	if useAgent {
		if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
			agentClient := agent.NewClient(agentConn)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	// Try key file if specified
	if spec.KeyFile != "" {
		keyPath := m.expandFunc(spec.KeyFile)
		keyPath = expandPath(keyPath)
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key file: %w", err)
		}

		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	// Try default key locations if no methods yet
	if len(methods) == 0 {
		for _, keyName := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			keyPath := filepath.Join(os.Getenv("HOME"), ".ssh", keyName)
			if keyData, err := os.ReadFile(keyPath); err == nil {
				if signer, err := ssh.ParsePrivateKey(keyData); err == nil {
					methods = append(methods, ssh.PublicKeys(signer))
					break
				}
			}
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no authentication methods available")
	}

	return methods, nil
}

// buildHostKeyCallback builds the host key callback for SSH connection.
func (m *Manager) buildHostKeyCallback(spec *registry.SSHSpec) (ssh.HostKeyCallback, error) {
	// Check strict host key checking (default: true)
	strictChecking := true
	if spec.StrictHostKeyChecking != nil {
		strictChecking = *spec.StrictHostKeyChecking
	}

	if !strictChecking {
		return ssh.InsecureIgnoreHostKey(), nil
	}

	knownHostsPath := spec.KnownHostsFile
	if knownHostsPath == "" {
		knownHostsPath = filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts")
	}
	knownHostsPath = expandPath(knownHostsPath)

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		// If known_hosts doesn't exist, use insecure callback
		if os.IsNotExist(err) {
			return ssh.InsecureIgnoreHostKey(), nil
		}
		return nil, fmt.Errorf("parse known_hosts: %w", err)
	}

	return callback, nil
}

// expandPath expands ~ to home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home := os.Getenv("HOME")
		return filepath.Join(home, path[2:])
	}
	return path
}
