package proxy

import (
	"context"
)

func (p *Proxy) startHubBackend(ctx context.Context, serverName string) (*backend, error) {
	conn, err := p.hubPool.Get(ctx, serverName)
	if err != nil {
		return nil, err
	}

	b := &backend{
		name:      serverName,
		transport: conn.Transport,
		reqID:     &p.reqID,
		pooled:    conn,
	}

	// Double check or re-init if needed. pool.Get uses dialHub which initializes.
	// But backend.initialize sends MCP-level init.
	// Note: We might want to skip b.initialize if transport.Initialize already did it (it did).
	// However, backend.initialize is harmless if idempotent or if we need to sync state.
	// Actually mcp.WebSocketTransport.Initialize ALREADY does the MCP handshake.
	// So we can skip it here to avoid "already initialized" errors.

	p.backendsMu.Lock()
	p.backends[serverName] = b
	p.backendsMu.Unlock()

	return b, nil
}
