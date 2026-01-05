package pool_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/pool"
	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

// mockTransport is a simple mock implementation for testing.
type mockTransport struct {
	closed bool
	mu     sync.Mutex
}

func (m *mockTransport) Send(ctx context.Context, msg *mcp.Message) error {
	return nil
}

func (m *mockTransport) Recv(ctx context.Context) (*mcp.Message, error) {
	return nil, nil
}

func (m *mockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockTransport) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func TestPoolGetAndPut(t *testing.T) {
	dialCount := 0
	p := pool.New(pool.Config{
		MaxIdle: 2,
		MaxOpen: 5,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			dialCount++
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	// First get should dial
	conn, err := p.Get(context.Background(), "server1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if dialCount != 1 {
		t.Errorf("expected 1 dial, got %d", dialCount)
	}

	// Put it back
	p.Put(conn)

	// Second get should reuse
	conn2, err := p.Get(context.Background(), "server1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if dialCount != 1 {
		t.Errorf("expected 1 dial (reused), got %d", dialCount)
	}

	p.Put(conn2)
}

func TestPoolMaxOpen(t *testing.T) {
	p := pool.New(pool.Config{
		MaxIdle: 1,
		MaxOpen: 2,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	// Get two connections
	conn1, _ := p.Get(context.Background(), "server1")
	conn2, _ := p.Get(context.Background(), "server1")

	// Third should fail
	_, err := p.Get(context.Background(), "server1")
	if err == nil {
		t.Error("expected error when max connections reached")
	}

	// Return one and try again
	p.Put(conn1)
	conn3, err := p.Get(context.Background(), "server1")
	if err != nil {
		t.Errorf("expected to get connection after Put: %v", err)
	}

	p.Put(conn2)
	p.Put(conn3)
}

func TestPoolStats(t *testing.T) {
	p := pool.New(pool.Config{
		MaxIdle: 5,
		MaxOpen: 10,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	conn, _ := p.Get(context.Background(), "server1")
	stats := p.Stats()
	if stats.ActiveConns != 1 {
		t.Errorf("expected 1 active conn, got %d", stats.ActiveConns)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}

	p.Put(conn)
	stats = p.Stats()
	if stats.IdleConns != 1 {
		t.Errorf("expected 1 idle conn, got %d", stats.IdleConns)
	}

	// Get again should be a hit
	conn2, _ := p.Get(context.Background(), "server1")
	stats = p.Stats()
	if stats.Hits != 1 {
		t.Errorf("expected 1 hit, got %d", stats.Hits)
	}
	p.Put(conn2)
}

func TestPoolConcurrent(t *testing.T) {
	var dialCount int32
	p := pool.New(pool.Config{
		MaxIdle: 5,
		MaxOpen: 10,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			atomic.AddInt32(&dialCount, 1)
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := p.Get(context.Background(), "server1")
			if err != nil {
				return // Expected for some goroutines
			}
			time.Sleep(10 * time.Millisecond)
			p.Put(conn)
		}()
	}
	wg.Wait()

	stats := p.Stats()
	if stats.TotalConns > 10 {
		t.Errorf("expected at most 10 total conns, got %d", stats.TotalConns)
	}
}

func TestPoolDialError(t *testing.T) {
	dialErr := errors.New("dial failed")
	p := pool.New(pool.Config{
		MaxOpen: 5,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			return nil, dialErr
		},
	})
	defer p.Close()

	_, err := p.Get(context.Background(), "server1")
	if err == nil {
		t.Error("expected error from dial")
	}

	// Active count should be decremented on failure
	stats := p.Stats()
	if stats.ActiveConns != 0 {
		t.Errorf("expected 0 active conns after dial failure, got %d", stats.ActiveConns)
	}
}

func TestPoolClose(t *testing.T) {
	transport := &mockTransport{}
	p := pool.New(pool.Config{
		MaxIdle: 5,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			return transport, nil
		},
	})

	conn, _ := p.Get(context.Background(), "server1")
	p.Put(conn)

	p.Close()

	if !transport.IsClosed() {
		t.Error("expected transport to be closed")
	}

	_, err := p.Get(context.Background(), "server1")
	if err == nil {
		t.Error("expected error after pool closed")
	}
}

func TestPoolWarmUp(t *testing.T) {
	var dialCount int32
	p := pool.New(pool.Config{
		MaxIdle: 5,
		MaxOpen: 10,
		DialFunc: func(ctx context.Context, name string) (mcp.Transport, error) {
			atomic.AddInt32(&dialCount, 1)
			return &mockTransport{}, nil
		},
	})
	defer p.Close()

	err := p.WarmUp(context.Background(), []string{"s1", "s2", "s3"})
	if err != nil {
		t.Errorf("WarmUp failed: %v", err)
	}

	if atomic.LoadInt32(&dialCount) != 3 {
		t.Errorf("expected 3 dials, got %d", dialCount)
	}

	stats := p.Stats()
	if stats.IdleConns != 3 {
		t.Errorf("expected 3 idle conns, got %d", stats.IdleConns)
	}
}
