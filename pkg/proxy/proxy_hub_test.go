package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/gateway"
	"gitlab.flexinfer.ai/libs/mcp-go"
)

func TestHubTransportBridge(t *testing.T) {
	// 1. Setup Gateway with a Host
	token := "proxy-secret"
	hub := gateway.NewHub()
	hub.Authenticator = &gateway.TokenAuthenticator{Token: token}

	gwServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hosts" {
			gateway.HostsHandler(hub, w, r)
		} else {
			gateway.Handler(hub, w, r)
		}
	}))
	defer gwServer.Close()

	wsURL := "ws" + strings.TrimPrefix(gwServer.URL, "http") + "/ws"

	// Connect a Guest Host to the Gateway
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=remote-server&role=host&token="+token, nil)
	if err != nil {
		t.Fatalf("Failed to connect guest host: %v", err)
	}
	defer hostConn.Close()

	// Mock logic for remote host tools
	go func() {
		for {
			_, p, err := hostConn.ReadMessage()
			if err != nil {
				return
			}
			var msg mcp.Message
			json.Unmarshal(p, &msg)

			if msg.Method == "initialize" {
				res := mcp.InitializeResult{
					ProtocolVersion: mcp.ProtocolVersion,
					Capabilities:    mcp.Capabilities{},
					ServerInfo:      mcp.ServerInfo{Name: "remote-host", Version: "1.0"},
				}
				resp, _ := mcp.NewResponse(msg.ID, res)
				b, _ := json.Marshal(resp)
				hostConn.WriteMessage(websocket.TextMessage, b)
			} else if msg.Method == "tools/list" {
				res := mcp.ToolsListResult{
					Tools: []mcp.Tool{
						{Name: "echo", Description: "echos back"},
					},
				}
				resp, _ := mcp.NewResponse(msg.ID, res)
				b, _ := json.Marshal(resp)
				hostConn.WriteMessage(websocket.TextMessage, b)
			}
		}
	}()

	// 2. Setup Proxy with empty registry
	tmpDir, _ := os.MkdirTemp("", "proxy-test")
	defer os.RemoveAll(tmpDir)
	regFile := filepath.Join(tmpDir, "registry.yaml")
	os.WriteFile(regFile, []byte("version: 1\nservers: []"), 0644)

	p, err := New(Config{
		RegistryPath: regFile,
		HubURL:       wsURL,
		HubToken:     token,
	})
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer p.Close()

	// 3. Run Prepare (should discover remote tools)
	if err := p.Prepare(ctx); err != nil {
		t.Fatalf("Proxy prepare failed: %v", err)
	}

	// 4. Verify discovery
	// We need to check if the tool "remote-server__echo" is registered in p.server
	// Currently p.server doesn't have a public ListTools, but it has internal state.
	// We can try to call it.

	// Wait a bit for async tasks
	time.Sleep(200 * time.Millisecond)

	// In a real scenario, the proxy.server.Run(ctx) would be called.
	// Here we just check the tool count via p.server.
	// Since we can't easily access p.server.tools, let's just assert no error in Prepare and check logs if we could.
	// Actually, we can check p.backends
	p.backendsMu.Lock()
	_, exists := p.backends["remote-server"]
	p.backendsMu.Unlock()

	if !exists {
		t.Fatal("remote-server backend should have been discovered and started")
	}
}
