package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/gateway/auth"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

var Upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now
	},
}

// pingInterval is the keepalive ping period for hub websocket connections.
// Exposed as a package var (not a literal) so tests can shorten it to exercise
// the ping/relay write concurrency without waiting 30s. See
// TestHandler_ConcurrentPingAndRelay_NoPanic.
var pingInterval = 30 * time.Second

// Client represents a connected MCP client.
type Client struct {
	conn *websocket.Conn
	mu   sync.Mutex
	Auth *auth.AuthContext
}

func (c *Client) Send(mt int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(mt, data)
}

// Host represents a registered MCP server/host.
type Host struct {
	name    string
	conn    *websocket.Conn
	mu      sync.Mutex
	clients map[*Client]bool
	cMu     sync.RWMutex
	Auth    *auth.AuthContext
}

func NewHost(name string, conn *websocket.Conn, auth *auth.AuthContext) *Host {
	return &Host{
		name:    name,
		conn:    conn,
		clients: make(map[*Client]bool),
		Auth:    auth,
	}
}

func (h *Host) Send(mt int, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn.WriteMessage(mt, data)
}

func (h *Host) AddClient(c *Client) {
	h.cMu.Lock()
	defer h.cMu.Unlock()
	h.clients[c] = true
}

func (h *Host) RemoveClient(c *Client) {
	h.cMu.Lock()
	defer h.cMu.Unlock()
	delete(h.clients, c)
}

func (h *Host) Broadcast(mt int, data []byte) {
	h.cMu.RLock()
	defer h.cMu.RUnlock()
	for client := range h.clients {
		if err := client.Send(mt, data); err != nil {
			log.Printf("Error sending to client of %s: %v", h.name, err)
		}
	}
}

// Authenticator defines an interface for authenticating gateway connections.
type Authenticator interface {
	Authenticate(r *http.Request) error
}

// TokenAuthenticator simple static token authenticator.
type TokenAuthenticator struct {
	Token string
}

func (a *TokenAuthenticator) Authenticate(r *http.Request) error {
	token := r.Header.Get("Authorization")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if !strings.HasPrefix(token, "Bearer ") {
		token = "Bearer " + token
	}
	if token != "Bearer "+a.Token {
		return fmt.Errorf("invalid or missing token")
	}
	return nil
}

// CertAuthenticator authenticates based on client certificates (mTLS).
type CertAuthenticator struct {
	AllowedCommonNames []string
}

func (a *CertAuthenticator) Authenticate(r *http.Request) error {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return errors.New("no client certificates provided")
	}

	if len(a.AllowedCommonNames) == 0 {
		// If no list provided, allow any valid certificate
		return nil
	}

	for _, cert := range r.TLS.PeerCertificates {
		for _, cn := range a.AllowedCommonNames {
			if cert.Subject.CommonName == cn {
				return nil
			}
		}
	}

	return errors.New("certificate common name not allowed")
}

// Hub maintains the set of active host connections.
type Hub struct {
	mu            sync.RWMutex
	hosts         map[string]*Host
	Authenticator Authenticator
	AuthHook      auth.Hook
	Redactor      *Redactor

	// Registry optionally restricts which servers can be proxied.
	// When set, only servers in the registry (or connected as hosts) are allowed.
	Registry *registry.Registry

	// BackendURLTemplate is a websocket URL template used when the requested server is not connected
	// as a registered host. It must be a websocket URL (ws:// or wss://) and may contain "{server}".
	// Default: ws://{server}:8080/ws
	BackendURLTemplate string

	// Dialer is used for outbound backend websocket connections (reverse-proxy mode).
	// If nil, websocket.DefaultDialer is used.
	Dialer *websocket.Dialer
}

func NewHub() *Hub {
	return &Hub{
		hosts:    make(map[string]*Host),
		AuthHook: &auth.NoOpHook{},
	}
}

func (h *Hub) RegisterHost(serverName string, conn *websocket.Conn, auth *auth.AuthContext) *Host {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.hosts[serverName]; ok {
		log.Printf("Replacing existing host for %s", serverName)
		old.conn.Close()
	}
	host := NewHost(serverName, conn, auth)
	h.hosts[serverName] = host
	HostsConnected.Inc()
	log.Printf("Host registered: %s", serverName)
	return host
}

func (h *Hub) UnregisterHost(serverName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.hosts[serverName]; ok {
		delete(h.hosts, serverName)
		HostsConnected.Dec()
		log.Printf("Host unregistered: %s", serverName)
	}
}

func (h *Hub) GetHost(serverName string) *Host {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hosts[serverName]
}

func (h *Hub) isAllowedServer(serverName string) bool {
	h.mu.RLock()
	if _, ok := h.hosts[serverName]; ok {
		h.mu.RUnlock()
		return true
	}
	reg := h.Registry
	h.mu.RUnlock()

	if reg == nil {
		return false
	}
	for _, s := range reg.Servers {
		if s != nil && s.Name == serverName {
			return true
		}
	}
	return false
}

func (h *Hub) ListHosts() []string {
	h.mu.RLock()
	hostsMap := make(map[string]struct{}, len(h.hosts))
	for name := range h.hosts {
		hostsMap[name] = struct{}{}
	}
	reg := h.Registry
	h.mu.RUnlock()

	if reg != nil {
		for _, srv := range reg.Servers {
			if srv != nil && strings.TrimSpace(srv.Name) != "" {
				hostsMap[srv.Name] = struct{}{}
			}
		}
	}

	hosts := make([]string, 0, len(hostsMap))
	for name := range hostsMap {
		hosts = append(hosts, name)
	}
	return hosts
}

func (h *Hub) backendURL(serverName string) (string, error) {
	h.mu.RLock()
	tpl := strings.TrimSpace(h.BackendURLTemplate)
	reg := h.Registry
	h.mu.RUnlock()

	if reg != nil {
		if srv := reg.GetServer(serverName); srv != nil {
			if u := strings.TrimSpace(srv.URL); u != "" {
				return validateBackendURL(u)
			}
		}
	}

	if tpl == "" {
		tpl = "ws://{server}:8080/ws"
	}

	u := strings.ReplaceAll(tpl, "{server}", serverName)
	return validateBackendURL(u)
}

func validateBackendURL(u string) (string, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return "", fmt.Errorf("parse backend url: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", fmt.Errorf("invalid backend url scheme: %q", parsed.Scheme)
	}

	return u, nil
}

// Handler handles WebSocket connections for the gateway.
func Handler(hub *Hub, w http.ResponseWriter, r *http.Request) {
	ctx, span := Tracer().Start(r.Context(), "Handler")
	defer span.End()

	serverName := r.URL.Query().Get("server")
	role := r.URL.Query().Get("role")

	if serverName == "" {
		http.Error(w, "missing server query param", http.StatusBadRequest)
		return
	}

	// Perform authentication check before upgrade
	if hub.Authenticator != nil {
		if err := hub.Authenticator.Authenticate(r); err != nil {
			log.Printf("Authentication failed for %s (%s): %v", serverName, role, err)
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}

	var authCtx *auth.AuthContext
	if hub.AuthHook != nil {
		var err error
		authCtx, err = hub.AuthHook.OnConnect(r.Context(), r)
		if err != nil {
			log.Printf("Auth hook rejected %s: %v", serverName, err)
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}

	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}

	if role == "host" {
		host := hub.RegisterHost(serverName, conn, authCtx)
		defer func() {
			hub.UnregisterHost(serverName)
			_ = conn.Close()
		}()

		// Set pong handler to reset read deadline
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Start pinger
		go func() {
			ticker := time.NewTicker(pingInterval)
			defer ticker.Stop()
			for range ticker.C {
				// WriteControl is concurrency-safe with other writers on this
				// conn; WriteMessage(PingMessage) is not and panics under a
				// concurrent relay/registration write.
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			}
		}()

		log.Printf("Host %s connected and waiting for clients", serverName)

		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("Host %s disconnected: %v", serverName, err)
				break
			}
			_, lSpan := Tracer().Start(ctx, "relay_host_to_clients")

			if hub.AuthHook != nil {
				if err := hub.AuthHook.OnMessage(ctx, authCtx, message); err != nil {
					log.Printf("Message rejected by auth hook: %v", err)
					lSpan.End()
					continue
				}
			}

			if hub.Redactor != nil {
				message = hub.Redactor.Redact(message)
			}
			MessagesRelayed.WithLabelValues("host_to_client", serverName).Inc()
			host.Broadcast(mt, message)
			lSpan.End()
		}
	} else {
		// Client role (default): first try a registered host; otherwise reverse-proxy to a backend WS server.
		if host := hub.GetHost(serverName); host != nil {
			client := &Client{conn: conn, Auth: authCtx}
			host.AddClient(client)
			ClientsConnected.Inc()
			defer func() {
				host.RemoveClient(client)
				ClientsConnected.Dec()
				_ = conn.Close()
			}()

			// Set pong handler
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			conn.SetPongHandler(func(string) error {
				_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
				return nil
			})

			// Start pinger
			go func() {
				ticker := time.NewTicker(pingInterval)
				defer ticker.Stop()
				for range ticker.C {
					// WriteControl is concurrency-safe with other writers on
					// this conn; WriteMessage(PingMessage) is not and panics
					// under a concurrent relay write.
					if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
						return
					}
				}
			}()

			log.Printf("Client connected to host %s", serverName)

			for {
				mt, message, err := conn.ReadMessage()
				if err != nil {
					log.Printf("Client of %s disconnected: %v", serverName, err)
					break
				}
				_, lSpan := Tracer().Start(ctx, "relay_client_to_host")

				if hub.AuthHook != nil {
					if err := hub.AuthHook.OnMessage(ctx, authCtx, message); err != nil {
						log.Printf("Message rejected by auth hook: %v", err)
						lSpan.End()
						continue
					}
				}

				if hub.Redactor != nil {
					message = hub.Redactor.Redact(message)
				}
				MessagesRelayed.WithLabelValues("client_to_host", serverName).Inc()
				if err := host.Send(mt, message); err != nil {
					log.Printf("Error relaying to host %s: %v", serverName, err)
					RelayErrors.WithLabelValues(serverName).Inc()
					lSpan.End()
					break
				}
				lSpan.End()
			}
			return
		}

		if !hub.isAllowedServer(serverName) {
			log.Printf("Client requested unknown server: %s", serverName)
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Server not registered"))
			_ = conn.Close()
			return
		}

		backendURL, err := hub.backendURL(serverName)
		if err != nil {
			log.Printf("Backend URL error for %s: %v", serverName, err)
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Backend URL error"))
			_ = conn.Close()
			return
		}

		dialer := hub.Dialer
		if dialer == nil {
			dialer = websocket.DefaultDialer
		}

		backendConn, _, err := dialer.DialContext(ctx, backendURL, nil)
		if err != nil {
			log.Printf("Backend connect failed for %s: %v", serverName, err)
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Backend unavailable"))
			_ = conn.Close()
			return
		}
		defer backendConn.Close()

		ClientsConnected.Inc()
		defer func() {
			ClientsConnected.Dec()
			_ = conn.Close()
		}()

		// Keep-alives for both ends
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})
		_ = backendConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		backendConn.SetPongHandler(func(string) error {
			_ = backendConn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		stop := make(chan struct{})
		stopOnce := sync.Once{}
		closeStop := func() { stopOnce.Do(func() { close(stop) }) }

		ping := func(c *websocket.Conn) {
			ticker := time.NewTicker(pingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// WriteControl is safe to call concurrently with the relay
					// goroutine's WriteMessage; a plain WriteMessage(PingMessage)
					// here races it and panics gorilla/websocket ("concurrent
					// write to websocket connection"), crashing the gateway.
					if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
						closeStop()
						return
					}
				case <-stop:
					return
				}
			}
		}
		go ping(conn)
		go ping(backendConn)

		log.Printf("Client proxy connected: %s -> %s", serverName, backendURL)

		relay := func(src, dst *websocket.Conn, direction string) {
			for {
				mt, message, err := src.ReadMessage()
				if err != nil {
					closeStop()
					return
				}

				_, lSpan := Tracer().Start(ctx, "relay_"+direction)
				if hub.AuthHook != nil {
					if err := hub.AuthHook.OnMessage(ctx, authCtx, message); err != nil {
						log.Printf("Message rejected by auth hook: %v", err)
						lSpan.End()
						continue
					}
				}
				if hub.Redactor != nil {
					message = hub.Redactor.Redact(message)
				}

				if direction == "client_to_backend" {
					MessagesRelayed.WithLabelValues("client_to_host", serverName).Inc()
				} else {
					MessagesRelayed.WithLabelValues("host_to_client", serverName).Inc()
				}

				if err := dst.WriteMessage(mt, message); err != nil {
					RelayErrors.WithLabelValues(serverName).Inc()
					lSpan.End()
					closeStop()
					return
				}
				lSpan.End()
			}
		}

		go relay(conn, backendConn, "client_to_backend")
		go relay(backendConn, conn, "backend_to_client")

		<-stop
	}
}

// HostsHandler returns a JSON list of registered host names.
func HostsHandler(hub *Hub, w http.ResponseWriter, r *http.Request) {
	if hub.Authenticator != nil {
		if err := hub.Authenticator.Authenticate(r); err != nil {
			log.Printf("Discovery authentication failed: %v", err)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(hub.ListHosts()); err != nil {
		http.Error(w, "encode error: "+err.Error(), http.StatusInternalServerError)
	}
}
