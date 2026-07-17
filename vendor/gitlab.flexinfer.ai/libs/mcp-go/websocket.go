package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/singleflight"
)

// Defaults for transport durability behaviour (see .loom/157).
const (
	defaultKeepAliveInterval  = 22 * time.Second // under typical 60-100s proxy idle windows
	defaultIdleProbeThreshold = 30 * time.Second // sync liveness probe on hand-out beyond this idle age
)

// WebSocketTransport implements MCP transport over WebSocket.
type WebSocketTransport struct {
	conn        *websocket.Conn
	serverName  string
	profile     string
	clientInfo  ClientInfo
	initialized atomic.Bool
	mu          sync.Mutex
	readMu      sync.Mutex

	// Durability state (atomic; safe to read/write from the keepalive goroutine
	// concurrently with the connection pool).
	lastTraffic   atomic.Int64 // unix-nano of the last successful app Send/Recv
	dead          atomic.Bool  // set when a keepalive/liveness ping fails
	stopKeepalive chan struct{}
	closeOnce     sync.Once
}

// WebSocketConfig configures a WebSocket connection.
type WebSocketConfig struct {
	URL                  string
	Profile              string
	CFAccessClientID     string            // Cloudflare Access client ID (optional)
	CFAccessClientSecret string            // Cloudflare Access client secret (optional)
	Headers              map[string]string // Custom HTTP headers (optional)
	ConnectTimeout       time.Duration     // Default: 10s
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	ClientInfo           ClientInfo // Client info for initialization

	// KeepAliveInterval sets the background WS ping cadence that keeps idle
	// connections from being reaped (close 1006). Default 22s; negative disables.
	KeepAliveInterval time.Duration
	// IdleProbeThreshold makes GetConnection synchronously ping a cached
	// connection that has been app-idle longer than this before handing it out.
	// Default 30s; negative disables.
	IdleProbeThreshold time.Duration
}

func (cfg WebSocketConfig) keepAliveInterval() time.Duration {
	switch {
	case cfg.KeepAliveInterval == 0:
		return defaultKeepAliveInterval
	case cfg.KeepAliveInterval < 0:
		return 0 // disabled
	default:
		return cfg.KeepAliveInterval
	}
}

func (cfg WebSocketConfig) idleProbeThreshold() time.Duration {
	switch {
	case cfg.IdleProbeThreshold == 0:
		return defaultIdleProbeThreshold
	case cfg.IdleProbeThreshold < 0:
		return 0 // disabled
	default:
		return cfg.IdleProbeThreshold
	}
}

// NewWebSocketTransport creates a WebSocket transport.
func NewWebSocketTransport(ctx context.Context, cfg WebSocketConfig, serverName string) (*WebSocketTransport, error) {
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.ClientInfo.Name == "" {
		cfg.ClientInfo.Name = "mcp-go-client"
		cfg.ClientInfo.Version = "1.0.0"
	}

	// Build URL with server query param
	url := cfg.URL
	if serverName != "" {
		if strings.Contains(url, "?") {
			url = fmt.Sprintf("%s&server=%s", url, serverName)
		} else {
			url = fmt.Sprintf("%s?server=%s", url, serverName)
		}
	}
	if cfg.Profile != "" {
		if strings.Contains(url, "?") {
			url = fmt.Sprintf("%s&profile=%s", url, cfg.Profile)
		} else {
			url = fmt.Sprintf("%s?profile=%s", url, cfg.Profile)
		}
	}

	// Build headers
	header := http.Header{}
	if cfg.CFAccessClientID != "" && cfg.CFAccessClientSecret != "" {
		header.Set("CF-Access-Client-Id", cfg.CFAccessClientID)
		header.Set("CF-Access-Client-Secret", cfg.CFAccessClientSecret)
	}
	for k, v := range cfg.Headers {
		header.Set(k, v)
	}

	// Create dialer with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: cfg.ConnectTimeout,
	}

	conn, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	t := &WebSocketTransport{
		conn:       conn,
		serverName: serverName,
		profile:    cfg.Profile,
		clientInfo: cfg.ClientInfo,
	}
	t.markTraffic()
	if iv := cfg.keepAliveInterval(); iv > 0 {
		t.startKeepalive(iv)
	}
	return t, nil
}

// Send sends a message over WebSocket.
func (t *WebSocketTransport) Send(ctx context.Context, msg *Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	if err := t.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("write message: %w", err)
	}

	t.markTraffic()
	return nil
}

// Recv receives a message from WebSocket.
//
// Recv honors ctx: gorilla's ReadMessage blocks indefinitely on a silent peer
// (connection open, no frame arriving) and never consults the context, so
// without a read deadline every ctx timeout ABOVE Recv is cosmetic — a stalled
// hub wedges the caller forever (the Mills spin-wedge / "Backend unavailable"
// hang, where an author/tool call hung past its own 45s budget and the outer
// 10m budget alike). gorilla honors SetReadDeadline, so we derive one from the
// context's deadline; a ctx that carries no deadline clears any prior one so an
// intentionally long-lived read (a notification listener) still blocks as
// before.
func (t *WebSocketTransport) Recv(ctx context.Context) (*Message, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	if dl, ok := ctx.Deadline(); ok {
		_ = t.conn.SetReadDeadline(dl)
	} else {
		_ = t.conn.SetReadDeadline(time.Time{})
	}

	_, data, err := t.conn.ReadMessage()
	if err != nil {
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			// Our ctx-derived read deadline fired: gorilla's read stream is now
			// unusable, so mark the connection dead for the pool / mcphub to redial
			// rather than reuse it. The raw net i/o-timeout error still surfaces so
			// mcphub's transport-error path invalidates + retries. (Detecting the
			// timeout on the error itself avoids a race between the net poller's
			// read deadline and the context timer both firing at the same instant.)
			t.markDead()
		}
		return nil, fmt.Errorf("read message: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	t.markTraffic()
	return &msg, nil
}

// Close closes the WebSocket connection and stops its keepalive loop.
func (t *WebSocketTransport) Close() error {
	t.initialized.Store(false)
	t.dead.Store(true)
	if t.stopKeepalive != nil {
		t.closeOnce.Do(func() { close(t.stopKeepalive) })
	}
	return t.conn.Close()
}

// markTraffic records the time of the last successful application Send/Recv.
func (t *WebSocketTransport) markTraffic() {
	t.lastTraffic.Store(time.Now().UnixNano())
}

// idleAge reports how long it has been since the last successful Send/Recv.
func (t *WebSocketTransport) idleAge() time.Duration {
	last := t.lastTraffic.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// isDead reports whether a keepalive/liveness ping has failed on this connection.
func (t *WebSocketTransport) isDead() bool { return t.dead.Load() }

// markDead marks the connection unusable so the pool will not hand it out again.
func (t *WebSocketTransport) markDead() { t.dead.Store(true) }

// startKeepalive runs a background ping loop that keeps an otherwise-idle
// connection from being reaped by an upstream proxy (the close-1006 storm in
// .loom/149). A failed ping marks the connection dead and stops the loop so the
// pool reconnects on the next GetConnection instead of failing at call time.
func (t *WebSocketTransport) startKeepalive(interval time.Duration) {
	t.stopKeepalive = make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-t.stopKeepalive:
				return
			case <-ticker.C:
				// Recent application traffic already kept the link warm.
				if t.idleAge() < interval {
					continue
				}
				if err := t.Ping(context.Background()); err != nil {
					t.markDead()
					return
				}
			}
		}
	}()
}

// alive reports whether a cached connection is safe to hand out. It trusts the
// keepalive-maintained dead flag and, for connections idle beyond
// idleProbeThreshold, performs one synchronous liveness ping.
func (t *WebSocketTransport) alive(idleProbeThreshold time.Duration) bool {
	if t.isDead() {
		return false
	}
	if idleProbeThreshold > 0 && t.idleAge() > idleProbeThreshold {
		if err := t.Ping(context.Background()); err != nil {
			t.markDead()
			return false
		}
	}
	return true
}

// Initialize performs the MCP initialization handshake.
func (t *WebSocketTransport) Initialize(ctx context.Context) error {
	if t.initialized.Load() {
		return nil
	}

	// Send initialize request
	initReq, err := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    Capabilities{},
		ClientInfo:      t.clientInfo,
	})
	if err != nil {
		return fmt.Errorf("create init request: %w", err)
	}

	if err := t.Send(ctx, initReq); err != nil {
		return fmt.Errorf("send init: %w", err)
	}

	// Receive init response
	resp, err := t.Recv(ctx)
	if err != nil {
		return fmt.Errorf("recv init: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("init error: %s", resp.Error.Message)
	}

	// Send initialized notification
	initNotif := &Message{JSONRPC: JSONRPCVersion, Method: "notifications/initialized"}
	if err := t.Send(ctx, initNotif); err != nil {
		return fmt.Errorf("send initialized: %w", err)
	}

	t.initialized.Store(true)
	return nil
}

// IsInitialized returns whether the transport has been initialized.
func (t *WebSocketTransport) IsInitialized() bool {
	return t.initialized.Load()
}

// Ping sends a ping message to check connection health.
func (t *WebSocketTransport) Ping(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second))
}

// WebSocketClient manages multiple WebSocket connections to MCP servers.
type WebSocketClient struct {
	cfg                WebSocketConfig
	conns              map[string]*WebSocketTransport
	mu                 sync.Mutex
	maxRetries         int
	idleProbeThreshold time.Duration
	dialGroup          singleflight.Group // collapses concurrent dials per server name
}

// NewWebSocketClient creates a new WebSocket client.
func NewWebSocketClient(cfg WebSocketConfig) *WebSocketClient {
	return &WebSocketClient{
		cfg:                cfg,
		conns:              make(map[string]*WebSocketTransport),
		maxRetries:         3,
		idleProbeThreshold: cfg.idleProbeThreshold(),
	}
}

// GetConnection returns a connection for a server, creating and initializing one if needed.
//
// A live cached connection is returned without redialing. A dead/stale one is
// evicted and a fresh dial is performed; concurrent callers that all find the
// connection dead share a single redial (singleflight) so one upstream close
// fails at most one in-flight call rather than the whole pool (.loom/149/157).
func (c *WebSocketClient) GetConnection(ctx context.Context, serverName string) (*WebSocketTransport, error) {
	// Fast path: reuse a live cached connection. The liveness probe runs outside
	// c.mu so a network round-trip never blocks other servers' lookups.
	c.mu.Lock()
	cached := c.conns[serverName]
	c.mu.Unlock()
	if cached != nil {
		if cached.IsInitialized() && cached.alive(c.idleProbeThreshold) {
			return cached, nil
		}
		c.evictIf(serverName, cached)
	}

	// Slow path: collapse concurrent dials for the same server into one.
	v, err, _ := c.dialGroup.Do(serverName, func() (interface{}, error) {
		// Another flight may have already established a live connection.
		c.mu.Lock()
		if conn, ok := c.conns[serverName]; ok && conn.IsInitialized() && !conn.isDead() {
			c.mu.Unlock()
			return conn, nil
		}
		c.mu.Unlock()
		return c.dialAndStore(ctx, serverName)
	})
	if err != nil {
		return nil, err
	}
	return v.(*WebSocketTransport), nil
}

// dialAndStore dials, initializes, and caches a fresh connection with retries.
func (c *WebSocketClient) dialAndStore(ctx context.Context, serverName string) (*WebSocketTransport, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff between attempts.
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		conn, err := NewWebSocketTransport(ctx, c.cfg, serverName)
		if err != nil {
			lastErr = fmt.Errorf("connect attempt %d: %w", attempt+1, err)
			continue
		}

		// Initialize MCP protocol
		if err := conn.Initialize(ctx); err != nil {
			_ = conn.Close()
			lastErr = fmt.Errorf("init attempt %d: %w", attempt+1, err)
			continue
		}

		c.mu.Lock()
		if old, ok := c.conns[serverName]; ok && old != conn {
			_ = old.Close()
		}
		c.conns[serverName] = conn
		c.mu.Unlock()
		return conn, nil
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, lastErr)
}

// evictIf removes conn from the cache only if it is still the cached entry for
// serverName, then closes it. The identity check avoids dropping a connection a
// concurrent dial may have just installed. Closing happens outside c.mu.
func (c *WebSocketClient) evictIf(serverName string, conn *WebSocketTransport) {
	c.mu.Lock()
	if cur, ok := c.conns[serverName]; ok && cur == conn {
		delete(c.conns, serverName)
	}
	c.mu.Unlock()
	_ = conn.Close()
}

// Reconnect forces a reconnection for a specific server. It marks the current
// connection dead (so GetConnection will not hand it back) and routes through
// the singleflight dial, so a herd of concurrent reconnects shares one redial.
func (c *WebSocketClient) Reconnect(ctx context.Context, serverName string) (*WebSocketTransport, error) {
	c.mu.Lock()
	if conn, ok := c.conns[serverName]; ok {
		conn.markDead()
	}
	c.mu.Unlock()

	return c.GetConnection(ctx, serverName)
}

// CloseConnection closes a specific server connection.
func (c *WebSocketClient) CloseConnection(serverName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if conn, ok := c.conns[serverName]; ok {
		_ = conn.Close()
		delete(c.conns, serverName)
	}
}

// Close closes all connections.
func (c *WebSocketClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for name, conn := range c.conns {
		_ = conn.Close()
		delete(c.conns, name)
	}
	return nil
}

// Dial implements an interface for connection pooling. It always builds
// a fresh, initialized WebSocketTransport — pool callers maintain their
// own bucket of independent *Conn entries and must NOT share an underlying
// websocket.Conn. Returning the c.conns-cached transport (as GetConnection
// does) caused pool entries to alias the same socket, so one caller's
// "mark unhealthy + Close" left every other caller's Send hitting
// "use of closed network connection". Pool semantics require independent
// transports — Close()'ing one must not affect any other.
//
// Each transport still gets its own keepalive loop (started in
// NewWebSocketTransport), so every pooled connection is kept warm
// independently — the close-1006 fix in .loom/149 applies to the pool path
// here, while GetConnection's liveness gating + singleflight covers the
// cached path.
func (c *WebSocketClient) Dial(ctx context.Context, serverName string) (Transport, error) {
	transport, err := NewWebSocketTransport(ctx, c.cfg, serverName)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := transport.Initialize(ctx); err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return transport, nil
}
