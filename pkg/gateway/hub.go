package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var Upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now
	},
}

// Client represents a connected MCP client.
type Client struct {
	conn *websocket.Conn
	mu   sync.Mutex
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
}

func NewHost(name string, conn *websocket.Conn) *Host {
	return &Host{
		name:    name,
		conn:    conn,
		clients: make(map[*Client]bool),
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
	Redactor      *Redactor
}

func NewHub() *Hub {
	return &Hub{
		hosts: make(map[string]*Host),
	}
}

func (h *Hub) RegisterHost(serverName string, conn *websocket.Conn) *Host {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.hosts[serverName]; ok {
		log.Printf("Replacing existing host for %s", serverName)
		old.conn.Close()
	}
	host := NewHost(serverName, conn)
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

func (h *Hub) ListHosts() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	hosts := make([]string, 0, len(h.hosts))
	for name := range h.hosts {
		hosts = append(hosts, name)
	}
	return hosts
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

	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}

	if role == "host" {
		host := hub.RegisterHost(serverName, conn)
		defer func() {
			hub.UnregisterHost(serverName)
			conn.Close()
		}()

		// Set pong handler to reset read deadline
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Start pinger
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
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
			if hub.Redactor != nil {
				message = hub.Redactor.Redact(message)
			}
			MessagesRelayed.WithLabelValues("host_to_client", serverName).Inc()
			host.Broadcast(mt, message)
			lSpan.End()
		}
	} else {
		// Client role (default)
		host := hub.GetHost(serverName)
		if host == nil {
			log.Printf("Client requested unknown host: %s", serverName)
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Host not registered"))
			conn.Close()
			return
		}

		client := &Client{conn: conn}
		host.AddClient(client)
		ClientsConnected.Inc()
		defer func() {
			host.RemoveClient(client)
			ClientsConnected.Dec()
			conn.Close()
		}()

		// Set pong handler
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Start pinger
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
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
	json.NewEncoder(w).Encode(hub.ListHosts())
}
