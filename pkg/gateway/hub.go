package gateway

import (
	"log"
	"net/http"
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

// Hub maintains the set of active host connections.
type Hub struct {
	mu    sync.RWMutex
	hosts map[string]*Host
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
	log.Printf("Host registered: %s", serverName)
	return host
}

func (h *Hub) UnregisterHost(serverName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.hosts[serverName]; ok {
		delete(h.hosts, serverName)
		log.Printf("Host unregistered: %s", serverName)
	}
}

func (h *Hub) GetHost(serverName string) *Host {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hosts[serverName]
}

// Handler handles WebSocket connections for the gateway.
func Handler(hub *Hub, w http.ResponseWriter, r *http.Request) {
	serverName := r.URL.Query().Get("server")
	role := r.URL.Query().Get("role")

	if serverName == "" {
		http.Error(w, "missing server query param", http.StatusBadRequest)
		return
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
			host.Broadcast(mt, message)
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
		defer func() {
			host.RemoveClient(client)
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
			if err := host.Send(mt, message); err != nil {
				log.Printf("Error relaying to host %s: %v", serverName, err)
				break
			}
		}
	}
}
