// Package process manages local MCP server processes.
//
// The process manager handles starting, stopping, and lifecycle management
// of MCP server processes. It integrates with the registry to fetch server
// configurations and starts processes with proper stdio transport.
//
// Example:
//
//	mgr := process.NewManager(reg, "local")
//	defer mgr.StopAll()
//
//	proc, err := mgr.Start(ctx, "mcp-git")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Use proc.Transport to communicate with the server
package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

// Process represents a running MCP server process.
type Process struct {
	Name         string
	Cmd          *exec.Cmd
	Transport    mcp.Transport
	StartedAt    time.Time
	LastActivity time.Time
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	// SSH-specific fields (nil for local processes)
	sshClient  interface{ Close() error } // *ssh.Client
	sshSession interface{ Close() error } // *ssh.Session
}

// ExpandFunc expands variables in strings (e.g., ${repo} -> /path/to/workspace).
type ExpandFunc func(string) string

// Manager manages local MCP server processes.
type Manager struct {
	registry   *registry.Registry
	target     string
	expandFunc ExpandFunc
	mu         sync.Mutex
	procs      map[string]*Process
}

// NewManager creates a new process manager.
func NewManager(reg *registry.Registry, target string) *Manager {
	return &Manager{
		registry:   reg,
		target:     target,
		expandFunc: func(s string) string { return s },
		procs:      make(map[string]*Process),
	}
}

// SetRegistry atomically swaps the registry used for server specs.
func (m *Manager) SetRegistry(reg *registry.Registry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry = reg
}

// SetExpandFunc sets the function used to expand variables in commands.
func (m *Manager) SetExpandFunc(fn ExpandFunc) {
	m.expandFunc = fn
}

// Start starts an MCP server process (local or SSH-based).
func (m *Manager) Start(ctx context.Context, serverName string) (*Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if proc, ok := m.procs[serverName]; ok {
		// For local processes, check if the process is still running
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			proc.LastActivity = time.Now()
			return proc, nil
		}
		// For SSH processes, check if client is set
		if proc.sshClient != nil {
			proc.LastActivity = time.Now()
			return proc, nil
		}
	}

	// Get server spec
	spec, err := m.registry.GetServerSpec(serverName, m.target)
	if err != nil {
		return nil, fmt.Errorf("get server spec: %w", err)
	}

	if spec.Command == "" {
		return nil, fmt.Errorf("server %s has no command defined", serverName)
	}

	// Check for SSH configuration
	if spec.SSH != nil && spec.SSH.Host != "" {
		proc, err := m.startSSH(ctx, serverName, spec)
		if err != nil {
			return nil, err
		}
		m.procs[serverName] = proc
		return proc, nil
	}

	// Expand variables in command
	command := m.expandFunc(spec.Command)

	// Build command with expanded args
	args := make([]string, len(spec.Args))
	for i, arg := range spec.Args {
		args[i] = m.expandFunc(fmt.Sprint(arg))
	}

	cmd := exec.CommandContext(ctx, command, args...)

	// Set environment with expanded values
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		expanded := m.expandFunc(v)
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, expanded))
	}

	// Get pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Start process
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start process: %w", err)
	}

	now := time.Now()
	proc := &Process{
		Name:         serverName,
		Cmd:          cmd,
		Transport:    mcp.NewStdioTransport(stdout, stdin),
		StartedAt:    now,
		LastActivity: now,
		stdin:        stdin,
		stdout:       stdout,
	}

	m.procs[serverName] = proc
	return proc, nil
}

// Stop stops a running MCP server process using a graceful shutdown sequence.
// The sequence follows the MCP spec recommendation: close stdin (EOF signal),
// wait for voluntary exit, then SIGTERM, then SIGKILL as last resort.
func (m *Manager) Stop(serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	proc, ok := m.procs[serverName]
	if !ok {
		return nil
	}

	delete(m.procs, serverName)

	// Close transport and pipes first (MCP spec: close input stream).
	proc.Transport.Close()
	if proc.stdin != nil {
		proc.stdin.Close()
	}
	if proc.stdout != nil {
		proc.stdout.Close()
	}

	// For SSH processes, close session and client.
	if proc.sshSession != nil {
		proc.sshSession.Close()
	}
	if proc.sshClient != nil {
		proc.sshClient.Close()
	}

	// For local processes, graceful shutdown: stdin EOF → wait → SIGTERM → SIGKILL.
	if proc.Cmd != nil && proc.Cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- proc.Cmd.Wait() }()

		select {
		case <-done:
			// Exited cleanly after stdin close.
		case <-time.After(2 * time.Second):
			// SIGTERM for graceful shutdown.
			_ = proc.Cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(1 * time.Second):
				// SIGKILL as last resort.
				_ = proc.Cmd.Process.Kill()
				<-done
			}
		}
	}

	return nil
}

// StopAll stops all running processes.
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.procs))
	for name := range m.procs {
		names = append(names, name)
	}
	m.mu.Unlock()

	for _, name := range names {
		_ = m.Stop(name)
	}
}

// Get returns a running process if it exists.
func (m *Manager) Get(serverName string) (*Process, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	proc, ok := m.procs[serverName]
	return proc, ok
}

// List returns all running process names.
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.procs))
	for name := range m.procs {
		names = append(names, name)
	}
	return names
}

// Dial creates a new connection to an MCP server, starting it if needed.
// This implements the pool.DialFunc interface.
func (m *Manager) Dial(ctx context.Context, serverName string) (mcp.Transport, error) {
	proc, err := m.Start(ctx, serverName)
	if err != nil {
		return nil, err
	}
	return proc.Transport, nil
}

// MarkActivity updates the last activity time for a server.
func (m *Manager) MarkActivity(serverName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if proc, ok := m.procs[serverName]; ok {
		proc.LastActivity = time.Now()
	}
}

// ReapIdle terminates processes that have been idle for longer than timeout.
// Returns the names of servers that were reaped.
func (m *Manager) ReapIdle(timeout time.Duration) []string {
	m.mu.Lock()
	var toReap []string
	now := time.Now()

	for name, proc := range m.procs {
		if now.Sub(proc.LastActivity) > timeout {
			toReap = append(toReap, name)
		}
	}
	m.mu.Unlock()

	for _, name := range toReap {
		_ = m.Stop(name)
	}

	return toReap
}

// Count returns the number of running processes.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.procs)
}

// IdleInfo contains information about process idle times.
type IdleInfo struct {
	Name         string
	IdleDuration time.Duration
	StartedAt    time.Time
}

// GetIdleInfo returns idle information for all running processes.
func (m *Manager) GetIdleInfo() []IdleInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	info := make([]IdleInfo, 0, len(m.procs))
	for name, proc := range m.procs {
		info = append(info, IdleInfo{
			Name:         name,
			IdleDuration: now.Sub(proc.LastActivity),
			StartedAt:    proc.StartedAt,
		})
	}
	return info
}
