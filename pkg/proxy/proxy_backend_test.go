package proxy

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

type scriptedTransport struct {
	mu   sync.Mutex
	sent []*mcp.Message
	recv chan *mcp.Message
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{
		recv: make(chan *mcp.Message, 8),
	}
}

func (t *scriptedTransport) Send(_ context.Context, msg *mcp.Message) error {
	t.mu.Lock()
	t.sent = append(t.sent, msg)
	t.mu.Unlock()

	if msg.Method == "initialize" {
		resp, err := mcp.NewResponse(msg.ID, mcp.InitializeResult{
			ProtocolVersion: mcp.ProtocolVersion,
			Capabilities:    mcp.Capabilities{},
			ServerInfo:      mcp.ServerInfo{Name: "backend", Version: "1.0"},
		})
		if err != nil {
			return err
		}
		t.recv <- resp
	}

	return nil
}

func (t *scriptedTransport) Recv(ctx context.Context) (*mcp.Message, error) {
	select {
	case msg := <-t.recv:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *scriptedTransport) Close() error {
	return nil
}

func TestBackendInitialize_AdvertisesSamplingAndResourceSubscriptions(t *testing.T) {
	tr := newScriptedTransport()
	var reqID atomic.Int64
	b := &backend{
		transport: tr,
		reqID:     &reqID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := b.initialize(ctx); err != nil {
		t.Fatalf("initialize() error: %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.sent) != 2 {
		t.Fatalf("expected 2 outbound messages, got %d", len(tr.sent))
	}

	initReq := tr.sent[0]
	if initReq.Method != "initialize" {
		t.Fatalf("expected first message to be initialize, got %q", initReq.Method)
	}

	var params mcp.InitializeParams
	if err := json.Unmarshal(initReq.Params, &params); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}

	if params.Capabilities.Sampling == nil {
		t.Fatal("expected sampling capability to be advertised")
	}
	if params.Capabilities.Resources == nil {
		t.Fatal("expected resources capability to be advertised")
	}
	if !params.Capabilities.Resources.Subscribe {
		t.Fatal("expected resources.subscribe=true")
	}
	if !params.Capabilities.Resources.ListChanged {
		t.Fatal("expected resources.listChanged=true")
	}

	if tr.sent[1].Method != "notifications/initialized" {
		t.Fatalf("expected initialized notification, got %q", tr.sent[1].Method)
	}
}
