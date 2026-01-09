package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.flexinfer.ai/libs/mcp-go"
)

type mockTransport struct {
	closed bool
}

func (t *mockTransport) Send(ctx context.Context, msg *mcp.Message) error { return nil }
func (t *mockTransport) Recv(ctx context.Context) (*mcp.Message, error)   { return nil, nil }
func (t *mockTransport) Close() error {
	t.closed = true
	return nil
}

func TestPool_Concurrency(t *testing.T) {
	var dials int32
	p := New(Config{
		MaxIdle:     5,
		MaxOpen:     10,
		IdleTimeout: 1 * time.Second,
		DialFunc: func(ctx context.Context, serverName string) (mcp.Transport, error) {
			atomic.AddInt32(&dials, 1)
			time.Sleep(10 * time.Millisecond) // Simulate network latency
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	// Launch parallel requests
	var wg sync.WaitGroup
	workers := 10 // Match MaxOpen to avoid errors
	
	// Server 1
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := p.Get(context.Background(), "server1")
			if err != nil {
				t.Errorf("Get error: %v", err)
				return
			}
			time.Sleep(5 * time.Millisecond) // Simulate usage
			p.Put(conn)
		}()
	}

	wg.Wait()

	// With 20 requests and MaxOpen=10, we expect roughly 10 dials if they overlap perfectly,
	// or up to 20 if sequential (but they are parallel).
	// Actually, since MaxOpen=10, requests 11-20 should block or fail?
	// Wait, Pool.Get returns error if max reached?
	// The implementation says: `if p.activeCount[serverName] >= p.maxOpen { return nil, fmt.Errorf("max connections reached") }`
	
	// So we expect some failures if we hit the limit hard.
	// But `Pool` doesn't block/wait for a connection? It returns error immediately.
	// That's a "fail-fast" pool.
}

func TestPool_MaxOpenEnforcement(t *testing.T) {
	p := New(Config{
		MaxIdle: 1,
		MaxOpen: 2,
		DialFunc: func(ctx context.Context, serverName string) (mcp.Transport, error) {
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	// Get 1
	c1, err := p.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}

	// Get 2
	c2, err := p.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}

	// Get 3 (should fail)
	_, err = p.Get(context.Background(), "s1")
	if err == nil {
		t.Error("expected error getting 3rd connection with MaxOpen=2")
	}

	// Return 1
	p.Put(c1)

	// Get 3 (should succeed now)
	c3, err := p.Get(context.Background(), "s1")
	if err != nil {
		t.Errorf("expected success after Put, got: %v", err)
	}

	p.Put(c2)
	p.Put(c3)
}