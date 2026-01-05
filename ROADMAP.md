# Roadmap: fi-mcp-kit

## Vision

To be the enterprise-grade orchestration toolkit for the Model Context Protocol (MCP) in the FlexInfer ecosystem, seamless functionality across local development and cloud-native Kubernetes environments.

## Current Status

- **CLI (`fi-mcp`)**: Registry validation, client config generation, and basic manifest generation.
- **Registry**: Schema validation and policy enforcement (local vs hub).
- **Secrets**: Abstractions for Env/File/Vault injection.
- **Proxy**: Local multiplexer with basic tool discovery.

## Immediate Priorities (Q1 2026)

### Gateway Parity & Hub Transport

- [ ] **Gateway Feature Parity**: Bring `services/fi-mcp-gateway` to parity with TypeScript implementation (WS support). (Started: Scaffolding created in `cmd/fi-mcp-gateway`)
    - [ ] **JSON-RPC Multiplexing**: Implement session-based routing to allow multiple clients to share a single host connection.
- [ ] **Auth Hooks**: Add authentication/authorization hooks and audit logging.
- [ ] **Hub Transport**: Extend `fi-mcp proxy` to route requests to remote hub servers via the gateway.

### Connection & Reliability

- [ ] **Connection Pooling**: Implement pooling for gateway backends and proxy processes with idle timeouts.
- [ ] **Backpressure**: Robust backpressure handling in the proxy layer.

## Future Milestones

### Kubernetes & Enterprise

- [ ] **Helm Charts**: develop official Helm charts for easier enterprise installation of the MCP Hub.
- [ ] **Kustomize Overlays**: Generate production-ready Kustomize overlays.

### Security

- [ ] **Hardening**: mTLS options, request redaction, and SSO integration.
- [ ] **Secret Management**: Native integration with external secret stores beyond basic injection.

## Maintenance

- [ ] Keep `mcp-go` dependency up to date.
- [ ] Regular security audits of the proxy component.
