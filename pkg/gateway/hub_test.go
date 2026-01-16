package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if err := c.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
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
