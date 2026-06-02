package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

func TestHandler_ReverseProxy_WithRegistryAllowlist(t *testing.T) {
	t.Parallel()

	backendMux := http.NewServeMux()
	backendMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, msg)
		}
	})
	backendSrv := httptest.NewServer(backendMux)
	t.Cleanup(backendSrv.Close)

	backendWS := "ws" + strings.TrimPrefix(backendSrv.URL, "http") + "/ws"

	hub := NewHub()
	hub.Registry = &registry.Registry{
		Version: 1,
		Servers: []*registry.Server{{Name: "tavily"}},
	}
	hub.BackendURLTemplate = backendWS

	gwMux := http.NewServeMux()
	gwMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		Handler(hub, w, r)
	})
	gwSrv := httptest.NewServer(gwMux)
	t.Cleanup(gwSrv.Close)

	gwWS := "ws" + strings.TrimPrefix(gwSrv.URL, "http") + "/ws?server=tavily"

	c, _, err := websocket.DefaultDialer.Dial(gwWS, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer c.Close()

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if writeErr := c.WriteMessage(websocket.TextMessage, payload); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}

	_, got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("unexpected payload: got %q want %q", string(got), string(payload))
	}
}

func TestHandler_RejectsUnknownServerWhenRegistrySet(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	hub.Registry = &registry.Registry{
		Version: 1,
		Servers: []*registry.Server{{Name: "tavily"}},
	}

	gwMux := http.NewServeMux()
	gwMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		Handler(hub, w, r)
	})
	gwSrv := httptest.NewServer(gwMux)
	t.Cleanup(gwSrv.Close)

	gwWS := "ws" + strings.TrimPrefix(gwSrv.URL, "http") + "/ws?server=unknown"
	c, _, err := websocket.DefaultDialer.Dial(gwWS, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer c.Close()

	// The gateway accepts the WS upgrade, then closes the connection immediately.
	_, _, err = c.ReadMessage()
	if err == nil {
		t.Fatalf("expected connection close")
	}
}

// TestHandler_ConcurrentPingAndRelay_NoPanic is a regression test for a
// "panic: concurrent write to websocket connection" crash in the hub relay.
// The relay spawned a keepalive-ping goroutine that called
// conn.WriteMessage(PingMessage) concurrently with the relay goroutine's
// conn.WriteMessage(payload). gorilla/websocket forbids concurrent writers, so
// the panic crashed the entire gateway process, dropping every client's
// connection (observed as repeated "muxstdio: transport closed" on daemons).
// Pings now use WriteControl, which is documented safe to call concurrently
// with other writers. Drive frequent pings + heavy bidirectional relay traffic;
// under the old code this panics (crashing the test binary), under the fix it
// survives. Run with -race for extra signal.
func TestHandler_ConcurrentPingAndRelay_NoPanic(t *testing.T) {
	// Not parallel: mutates the package-level pingInterval. Non-parallel tests
	// complete (including cleanup) before parallel tests resume, so the restore
	// happens-before any concurrent read of pingInterval.
	old := pingInterval
	pingInterval = time.Millisecond
	t.Cleanup(func() { pingInterval = old })

	backendMux := http.NewServeMux()
	backendMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})
	backendSrv := httptest.NewServer(backendMux)
	t.Cleanup(backendSrv.Close)
	backendWS := "ws" + strings.TrimPrefix(backendSrv.URL, "http") + "/ws"

	hub := NewHub()
	hub.Registry = &registry.Registry{
		Version: 1,
		Servers: []*registry.Server{{Name: "tavily"}},
	}
	hub.BackendURLTemplate = backendWS

	gwMux := http.NewServeMux()
	gwMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		Handler(hub, w, r)
	})
	gwSrv := httptest.NewServer(gwMux)
	t.Cleanup(gwSrv.Close)
	gwWS := "ws" + strings.TrimPrefix(gwSrv.URL, "http") + "/ws?server=tavily"

	c, _, err := websocket.DefaultDialer.Dial(gwWS, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer c.Close()

	// Continuously drain the client conn so the backend->client relay keeps
	// writing to it — that is the conn the ping goroutine also writes to.
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				readErr <- err
				return
			}
		}
	}()

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case <-deadline:
			return // survived sustained concurrent ping/relay writes without panic
		case err := <-readErr:
			t.Fatalf("relay connection closed during concurrent ping/relay traffic: %v", err)
		default:
			if err := c.WriteMessage(websocket.TextMessage, payload); err != nil {
				t.Fatalf("client write: %v", err)
			}
		}
	}
}
