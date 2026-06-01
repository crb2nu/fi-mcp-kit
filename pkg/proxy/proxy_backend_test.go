package proxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestNewAppliesBackpressureConfig(t *testing.T) {
	regFile := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(regFile, []byte("version: 1\nservers: []"), 0644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}

	p, err := New(Config{
		RegistryPath:       regFile,
		LocalMaxOpen:       3,
		HubMaxOpen:         4,
		BackendWaitTimeout: 75 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer p.Close()

	if got := p.localPool.MaxOpen(); got != 3 {
		t.Fatalf("local max open = %d, want 3", got)
	}
	if got := p.hubPool.MaxOpen(); got != 4 {
		t.Fatalf("hub max open = %d, want 4", got)
	}
	if got := p.localPool.WaitTimeout(); got != 75*time.Millisecond {
		t.Fatalf("local wait timeout = %s, want 75ms", got)
	}
	if got := p.hubPool.WaitTimeout(); got != 75*time.Millisecond {
		t.Fatalf("hub wait timeout = %s, want 75ms", got)
	}
}

func TestNewBackpressureDefaultsMatchExistingPoolLimits(t *testing.T) {
	regFile := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(regFile, []byte("version: 1\nservers: []"), 0644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}

	p, err := New(Config{RegistryPath: regFile})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer p.Close()

	if got := p.localPool.MaxOpen(); got != DefaultLocalMaxOpen {
		t.Fatalf("local max open = %d, want %d", got, DefaultLocalMaxOpen)
	}
	if got := p.hubPool.MaxOpen(); got != DefaultHubMaxOpen {
		t.Fatalf("hub max open = %d, want %d", got, DefaultHubMaxOpen)
	}
	if got := p.localPool.WaitTimeout(); got != 0 {
		t.Fatalf("local wait timeout = %s, want 0", got)
	}
	if got := p.hubPool.WaitTimeout(); got != 0 {
		t.Fatalf("hub wait timeout = %s, want 0", got)
	}
}
