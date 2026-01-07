package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/gateway"
)

func TestGatewayMultiplexing(t *testing.T) {
	hub := gateway.NewHub()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.Handler(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// 1. Connect Host
	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test-server&role=host", nil)
	if err != nil {
		t.Fatalf("Failed to connect host: %v", err)
	}
	defer hostConn.Close()

	// 2. Connect Client 1
	client1Conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test-server&role=client", nil)
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer client1Conn.Close()

	// 3. Connect Client 2
	client2Conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test-server&role=client", nil)
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer client2Conn.Close()

	// Give them a moment to register
	time.Sleep(100 * time.Millisecond)

	// 4. Test Client 1 -> Host
	testMsg := "hello from client 1"
	if err := client1Conn.WriteMessage(websocket.TextMessage, []byte(testMsg)); err != nil {
		t.Fatalf("Client 1 failed to send message: %v", err)
	}

	_, p, err := hostConn.ReadMessage()
	if err != nil {
		t.Fatalf("Host failed to read message: %v", err)
	}
	if string(p) != testMsg {
		t.Errorf("Expected host to receive %q, got %q", testMsg, string(p))
	}

	// 5. Test Host -> Multiple Clients (Broadcast)
	broadcastMsg := "ping from host"
	if err := hostConn.WriteMessage(websocket.TextMessage, []byte(broadcastMsg)); err != nil {
		t.Fatalf("Host failed to broadcast: %v", err)
	}

	// Verify Client 1 receives it
	_, p1, err := client1Conn.ReadMessage()
	if err != nil {
		t.Fatalf("Client 1 failed to read broadcast: %v", err)
	}
	if string(p1) != broadcastMsg {
		t.Errorf("Client 1: expected %q, got %q", broadcastMsg, string(p1))
	}

	// Verify Client 2 receives it
	_, p2, err := client2Conn.ReadMessage()
	if err != nil {
		t.Fatalf("Client 2 failed to read broadcast: %v", err)
	}
	if string(p2) != broadcastMsg {
		t.Errorf("Client 2: expected %q, got %q", broadcastMsg, string(p2))
	}
}

func TestHostNotFound(t *testing.T) {
	hub := gateway.NewHub()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.Handler(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// Connect Client to non-existent host
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=ghost-server&role=client", nil)
	if err != nil {
		t.Fatalf("Dial should succeed because upgrade happens before host check: %v", err)
	}
	defer conn.Close()

	// The server should close the connection immediately
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("Expected error reading from connection that should be closed, got nil")
	}
}

func TestGatewayAuthentication(t *testing.T) {
	token := "secret-token"
	hub := gateway.NewHub()
	hub.Authenticator = &gateway.TokenAuthenticator{Token: token}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.Handler(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// 1. Try to connect without token - should fail
	_, resp, err := websocket.DefaultDialer.Dial(wsURL+"?server=test&role=host", nil)
	if err == nil {
		t.Fatal("Expected error connecting without token, got nil")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got: %v (resp: %v)", err, resp)
	}

	// 2. Try with invalid token - should fail
	_, resp2, err := websocket.DefaultDialer.Dial(wsURL+"?server=test&role=host&token=wrong", nil)
	if err == nil {
		t.Fatal("Expected error connecting with wrong token, got nil")
	}
	if resp2 == nil || resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for wrong token, got: %v (resp: %v)", err, resp2)
	}

	// 3. Try with valid token in query param
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test&role=host&token="+token, nil)
	if err != nil {
		t.Fatalf("Failed to connect with valid query token: %v", err)
	}
	conn.Close()

	// 4. Try with valid token in Authorization header
	header := http.Header{}
	header.Add("Authorization", "Bearer "+token)
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test&role=host", header)
	if err != nil {
		t.Fatalf("Failed to connect with valid auth header: %v", err)
	}
	conn2.Close()
}
func TestGatewayRedaction(t *testing.T) {
	hub := gateway.NewHub()
	hub.Redactor = gateway.NewRedactor()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.Handler(hub, w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// Connect Host
	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test-server&role=host", nil)
	if err != nil {
		t.Fatalf("Failed to connect host: %v", err)
	}
	defer hostConn.Close()

	// Connect Client
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL+"?server=test-server&role=client", nil)
	if err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}
	defer clientConn.Close()

	// Test message with a secret
	secretKey := "sk-1234567890abcdef12345678"
	msgWithSecret := "User requested key: " + secretKey
	if err := clientConn.WriteMessage(websocket.TextMessage, []byte(msgWithSecret)); err != nil {
		t.Fatalf("Client failed to send: %v", err)
	}

	_, p, err := hostConn.ReadMessage()
	if err != nil {
		t.Fatalf("Host failed to read: %v", err)
	}

	receivedMsg := string(p)
	if strings.Contains(receivedMsg, secretKey) {
		t.Errorf("Secret key was not redacted! Received: %q", receivedMsg)
	}
	if !strings.Contains(receivedMsg, "[REDACTED]") {
		t.Errorf("Message does not contain [REDACTED] placeholder: %q", receivedMsg)
	}
}

func init() {
	// Suppress log noise during tests
	log.SetOutput(io.Discard)
}
