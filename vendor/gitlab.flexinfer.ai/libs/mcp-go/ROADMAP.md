# Roadmap: mcp-go

## Vision

To provide a rock-solid, high-performance, and fully compliant Go SDK for the Model Context Protocol (MCP), serving as the foundational layer for all Go-based MCP servers and tools in the FlexInfer ecosystem.

## Current Status

- **Protocol**: Full JSON-RPC 2.0 implementation with MCP types.
- **Transports**: Stdio (JSON-L + Header) and WebSocket.
- **Utils**: Fluent `ToolBuilder` and connection pooling with idle reaping.
- **Concurrency**: Parallel request handling with configurable limits and backpressure.
- **Maturity**: Used in production by 26+ servers.

## Immediate Priorities (Q1 2026)

### Protocol Completeness

- [x] **SSE Transport**: Implement Server-Sent Events transport for HTTP-based interactions.
- [x] **Validation Layer**: Strict schema validation for incoming JSON-RPC messages to fail fast on malformed requests.
- [x] **Structured Logging**: Deep integration with `slog` for protocol-level trace debugging and tool duration tracking.

## Future Milestones (Q2 2026+)

### Advanced Capabilities

- [ ] **Sampling**: Implement the 'sampling' capability allowing servers to request completions from the client (Agentic patterns).
- [ ] **Resource Subscriptions**: Support for client subscriptions to resource updates.
- [ ] **Plugin System**: Experimental support for loading MCP servers as Go plugins.

## Maintenance

- [ ] Maintain compatibility with Go 1.22+.
- [ ] Regular sync with official MCP specification updates.