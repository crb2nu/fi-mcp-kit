// Package pool provides connection pooling for MCP servers.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

// Conn represents a pooled connection to an MCP server.
type Conn struct {
	ServerName string
	Transport  mcp.Transport
	CreatedAt  time.Time
	LastUsed   time.Time
	Healthy    bool
}

// Stats provides pool statistics.
type Stats struct {
	TotalConns  int
	ActiveConns int
	IdleConns   int
	Hits        int64
	Misses      int64
}

// Pool manages a pool of MCP server connections.
type Pool struct {
	maxIdle     int
	maxOpen     int
	idleTimeout time.Duration
	waitTimeout time.Duration
	dialFunc    DialFunc
	mu          sync.Mutex
	cond        *sync.Cond
	conns       map[string][]*Conn
	activeCount map[string]int
	stats       Stats
	closed      bool
}

// ErrExhausted identifies errors returned when a server pool is saturated.
var ErrExhausted = errors.New("pool exhausted")

// ExhaustedError describes a saturated per-server pool.
type ExhaustedError struct {
	ServerName  string
	MaxOpen     int
	WaitTimeout time.Duration
}

func (e *ExhaustedError) Error() string {
	if e.WaitTimeout > 0 {
		return fmt.Sprintf("pool exhausted for %s: max open connections reached (max_open=%d, waited %s)", e.ServerName, e.MaxOpen, e.WaitTimeout)
	}
	return fmt.Sprintf("pool exhausted for %s: max open connections reached (max_open=%d)", e.ServerName, e.MaxOpen)
}

func (e *ExhaustedError) Unwrap() error {
	return ErrExhausted
}

// IsExhausted reports whether err is a pool saturation error.
func IsExhausted(err error) bool {
	return errors.Is(err, ErrExhausted)
}

// DialFunc is a function that creates a new connection to a server.
type DialFunc func(ctx context.Context, serverName string) (mcp.Transport, error)

// Config configures the connection pool.
type Config struct {
	MaxIdle     int           // Maximum idle connections per server (default: 2)
	MaxOpen     int           // Maximum open connections per server (default: 10)
	IdleTimeout time.Duration // Idle connection timeout (default: 5m)
	WaitTimeout time.Duration // Wait timeout when pool exhausted; 0 = immediate error (default: 0)
	DialFunc    DialFunc      // Function to dial new connections
}

// New creates a new connection pool.
func New(cfg Config) *Pool {
	if cfg.MaxIdle <= 0 {
		cfg.MaxIdle = 2
	}
	if cfg.MaxOpen <= 0 {
		cfg.MaxOpen = 10
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}

	p := &Pool{
		maxIdle:     cfg.MaxIdle,
		maxOpen:     cfg.MaxOpen,
		idleTimeout: cfg.IdleTimeout,
		waitTimeout: cfg.WaitTimeout,
		dialFunc:    cfg.DialFunc,
		conns:       make(map[string][]*Conn),
		activeCount: make(map[string]int),
	}
	p.cond = sync.NewCond(&p.mu)

	// Start idle connection reaper.
	go p.reapLoop()

	return p
}

// MaxOpen returns the configured maximum open connections per server.
func (p *Pool) MaxOpen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxOpen
}

// WaitTimeout returns the configured timeout for waiting on a saturated pool.
func (p *Pool) WaitTimeout() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitTimeout
}

// Get retrieves a connection from the pool, or creates a new one.
func (p *Pool) Get(ctx context.Context, serverName string) (*Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("pool is closed")
	}

	// Check for idle connection.
	if conns := p.conns[serverName]; len(conns) > 0 {
		for len(conns) > 0 {
			conn := conns[len(conns)-1]
			conns = conns[:len(conns)-1]
			p.stats.IdleConns--

			// Drop unhealthy connections and continue looking.
			if !conn.Healthy {
				_ = conn.Transport.Close()
				continue
			}

			p.conns[serverName] = conns
			p.activeCount[serverName]++
			p.stats.Hits++
			p.stats.ActiveConns++
			p.mu.Unlock()

			conn.LastUsed = time.Now()
			return conn, nil
		}

		// No usable conns left.
		p.conns[serverName] = conns
	}

	// Check if we can create a new connection.
	if p.activeCount[serverName] >= p.maxOpen {
		if p.waitTimeout <= 0 {
			p.mu.Unlock()
			return nil, &ExhaustedError{ServerName: serverName, MaxOpen: p.maxOpen}
		}
		// Wait for a connection to be returned.
		deadline := time.Now().Add(p.waitTimeout)
		for p.activeCount[serverName] >= p.maxOpen {
			if err := ctx.Err(); err != nil {
				p.mu.Unlock()
				return nil, err
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				p.mu.Unlock()
				return nil, &ExhaustedError{ServerName: serverName, MaxOpen: p.maxOpen, WaitTimeout: p.waitTimeout}
			}
			// Wake cond after remaining time so we don't block forever.
			timer := time.AfterFunc(remaining, p.broadcast)
			stopContextWake := context.AfterFunc(ctx, p.broadcast)
			p.cond.Wait() // releases mu, re-acquires on wake
			timer.Stop()
			stopContextWake()
			if p.closed {
				p.mu.Unlock()
				return nil, fmt.Errorf("pool is closed")
			}
			// Re-check idle pool — another waiter may have returned a conn.
			if conns := p.conns[serverName]; len(conns) > 0 {
				conn := conns[len(conns)-1]
				p.conns[serverName] = conns[:len(conns)-1]
				p.stats.IdleConns--
				if conn.Healthy {
					p.activeCount[serverName]++
					p.stats.Hits++
					p.stats.ActiveConns++
					p.mu.Unlock()
					conn.LastUsed = time.Now()
					return conn, nil
				}
				_ = conn.Transport.Close()
			}
		}
	}

	p.activeCount[serverName]++
	p.stats.Misses++
	p.stats.TotalConns++
	p.stats.ActiveConns++
	p.mu.Unlock()

	// Create new connection.
	transport, err := p.dialFunc(ctx, serverName)
	if err != nil {
		p.mu.Lock()
		p.activeCount[serverName]--
		p.stats.ActiveConns--
		p.cond.Broadcast()
		p.mu.Unlock()
		return nil, fmt.Errorf("dial %s: %w", serverName, err)
	}

	now := time.Now()
	return &Conn{
		ServerName: serverName,
		Transport:  transport,
		CreatedAt:  now,
		LastUsed:   now,
		Healthy:    true,
	}, nil
}

// Put returns a connection to the pool.
func (p *Pool) Put(conn *Conn) {
	p.mu.Lock()

	if p.closed || !conn.Healthy {
		p.activeCount[conn.ServerName]--
		p.stats.ActiveConns--
		_ = conn.Transport.Close()
		p.cond.Broadcast()
		p.mu.Unlock()
		return
	}

	// Check if we have room in idle pool.
	if len(p.conns[conn.ServerName]) >= p.maxIdle {
		p.activeCount[conn.ServerName]--
		p.stats.ActiveConns--
		_ = conn.Transport.Close()
		p.cond.Broadcast()
		p.mu.Unlock()
		return
	}

	conn.LastUsed = time.Now()
	p.conns[conn.ServerName] = append(p.conns[conn.ServerName], conn)
	p.activeCount[conn.ServerName]--
	p.stats.ActiveConns--
	p.stats.IdleConns++
	p.cond.Broadcast()
	p.mu.Unlock()
}

// ClearServer closes and removes all idle connections for a specific server.
func (p *Pool) ClearServer(serverName string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	conns := p.conns[serverName]
	for _, conn := range conns {
		_ = conn.Transport.Close()
		p.stats.IdleConns--
	}
	delete(p.conns, serverName)
}

// Close closes the pool and all connections.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	for _, conns := range p.conns {
		for _, conn := range conns {
			_ = conn.Transport.Close()
		}
	}
	p.conns = nil
	p.cond.Broadcast()
	return nil
}

func (p *Pool) broadcast() {
	p.mu.Lock()
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Stats returns pool statistics.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// reapLoop periodically closes idle connections.
func (p *Pool) reapLoop() {
	reapEvery := time.Minute
	if p.idleTimeout > 0 && p.idleTimeout < time.Minute {
		reapEvery = p.idleTimeout / 2
		if reapEvery < 25*time.Millisecond {
			reapEvery = 25 * time.Millisecond
		}
	}

	ticker := time.NewTicker(reapEvery)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}

		now := time.Now()
		for serverName, conns := range p.conns {
			var keep []*Conn
			for _, conn := range conns {
				if now.Sub(conn.LastUsed) > p.idleTimeout {
					_ = conn.Transport.Close()
					p.stats.IdleConns--
				} else {
					keep = append(keep, conn)
				}
			}
			p.conns[serverName] = keep
		}
		p.mu.Unlock()
	}
}

// WarmUp pre-establishes connections to the specified servers.
func (p *Pool) WarmUp(ctx context.Context, servers []string) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(servers))

	for _, server := range servers {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			conn, err := p.Get(ctx, s)
			if err != nil {
				errCh <- fmt.Errorf("warm up %s: %w", s, err)
				return
			}
			p.Put(conn)
		}(server)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("warm up failed: %v", errs)
	}
	return nil
}
