# Resilience in fi-mcp-kit

## Recommendation

Prefer small, composable building blocks over a “big” bespoke `go-resilience` package:

- retries/backoff: `github.com/cenkalti/backoff/v4`
- circuit breaker: `github.com/sony/gobreaker`
- rate limiting: `golang.org/x/time/rate`
- timeouts/cancellation: `context.Context`

Wrap these behind minimal `pkg/resilience` helpers only when we need:

- consistent defaults (timeouts, jitter, max attempts)
- standardized metrics labels
- structured logs across gateway/proxy

## Alternatives

If we want a batteries-included pattern library:

- `github.com/slok/goresilience`: higher-level resilience composition (bulkheads, breakers, timeouts, retries)

Notes:

- Keep dependencies lean in `fi-mcp-kit` core; prefer optional modules (or a separate `fi-mcp-kit/extra` tree) for heavier integrations.
- For Kubernetes-facing services (gateway), prioritize: bounded concurrency, backpressure, and observability (Prometheus metrics + structured logs) over aggressive retries.

