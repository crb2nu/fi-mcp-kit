# Inventory (existing code to reuse)

This is a short map of what’s already in `~/workspace/services` and `~/workspace/libs` that we can extract/reuse for the enterprise `fi-mcp-kit` + `fi-mcp-gateway` track.

## Primary sources

- `services/loom-core`
  - `internal/pool/pool.go`: per-server connection pooling for `mcp.Transport` (idle reaping, warmup)
  - `internal/process/manager.go`: stdio process lifecycle for local servers (start/stop/reap idle, env/arg expansion)
  - `internal/router/router.go`: local vs hub routing + simple circuit breaker (failure threshold + recovery time)
  - `internal/daemon/daemon.go`: end-to-end “local orchestrator” (registry load, secrets expansion, pool + hub pool, tool cache)
  - `pkg/generator`, `pkg/registry`, `pkg/secrets`: already extracted into `libs/fi-mcp-kit`
- `services/mcp-gateway` (TypeScript)
  - `src/session.ts`: per-session backend WS caching + tool-name routing (`tools/call` → route by `params.name`)
  - `src/router.ts`: route tables derived from registry + allow/deny controls
- `libs/mcp-go`
  - `websocket.go`: `WebSocketTransport` + `WebSocketClient` (multiplexed dials; useful for hub/gateway clients)

## Secondary sources

- `libs/py-resilience`
  - Python implementations of circuit breaker + rate limiting patterns; useful only as a reference, not a direct dependency.

## Likely extraction targets (next)

1) `fi-mcp-kit/pkg/pool`
   - port `services/loom-core/internal/pool` (keep API `Get/Put/WarmUp/Stats`)
2) `fi-mcp-kit/pkg/process`
   - port `services/loom-core/internal/process` (process lifecycle + idle reaping)
3) `fi-mcp-gateway`: parity with TS gateway
   - implement tool-name routing + per-session backend connection cache (Go)
   - move shared routing logic into `fi-mcp-kit/pkg/router` (or a new `pkg/gateway/router`)

