# mcp-go Roadmap

> Last Updated: 2026-07-02
> Tier: 2 (see workspace AGENTS.md "Portfolio Tiers")
> Tracking Issue: https://gitlab.flexinfer.ai/libs/mcp-go/-/issues/3

## Current Status

Active (steady). mcp-go is the workspace's Go SDK for the Model Context
Protocol: JSON-RPC 2.0 + MCP types, stdio / WebSocket / Streamable HTTP (+SSE)
transports, ToolBuilder, connection pooling, and parallel request handling
with backpressure. Consumed by loom-core's Go MCP servers and by fi-mcp-kit.
Last meaningful activity 2026-06-17: WebSocket keepalive, liveness gating, and
singleflight reconnect (`8c42d354`); 2026-06-06 toon codec cross-version
decode fixes (`fa6daf97`, `aa12cdfb`); 2026-06-01 an MCP SDK compatibility
smoke test (`a079baf5`). Q1-2026 goals from the previous roadmap (SSE
transport, validation layer, structured logging) are done. Backlog is empty
after grooming (the stale 2026-02 backlog-sync issue was closed 2026-07-02).

- **Plan store**: plan-workspace-portfolio-refresh-2026-h2-roadmaps-quality-baselin-f3db23
- **Deployed**: not deployed (library; consumed by downstream Go MCP servers)
- **CI**: go template family (platform/gitops `/ci/templates/go.yml`) + tech-radar scan; lint gate is `go vet`

## Now

Nothing actively in flight (last landed work 2026-06-17).

## Next

Nothing queued — reliability fixes land on demand from downstream consumers. File P-labeled issues to queue work.

## Later

- Sampling capability (server-requested completions / agentic patterns)
- Resource subscriptions
- Track upstream MCP specification revisions; maintain Go 1.22+ compatibility

## Backlog

Full backlog: [P1 issues](https://gitlab.flexinfer.ai/libs/mcp-go/-/issues/?label_name[]=P1) ·
[P2](https://gitlab.flexinfer.ai/libs/mcp-go/-/issues/?label_name[]=P2) ·
[P3](https://gitlab.flexinfer.ai/libs/mcp-go/-/issues/?label_name[]=P3) ·
[Milestones](https://gitlab.flexinfer.ai/libs/mcp-go/-/milestones)
