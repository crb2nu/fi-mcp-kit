# fi-mcp-kit Roadmap

> Last Updated: 2026-07-02
> Tier: 2 (see workspace AGENTS.md "Portfolio Tiers")
> Tracking Issue: https://gitlab.flexinfer.ai/libs/fi-mcp-kit/-/issues/4

## Current Status

Active (steady). fi-mcp-kit is the Go MCP orchestration toolkit: the `fi-mcp`
CLI (registry validation, client config generation), registry schema/policy
enforcement, secrets injection (Env/File/Vault), a local proxy multiplexer,
connection pooling (`pkg/pool`), and process management (`pkg/process`). It
also ships the `fi-mcp-gateway` image consumed by `services/fi-mcp-gateway`,
deployed on k3s via Flux image automation. Last meaningful activity
2026-06-02: fixed a concurrent-write panic in gateway keepalive pings
(`57c6c794`); 2026-06-01 landed mcp-core compatibility, proxy backpressure
knobs, and an MCP consumer smoke harness (`ce8f89f9`, `8e4228bd`). Q1-2026
gateway parity, auth hooks, and pooling goals from the previous roadmap are
done; backpressure landed 2026-06.

- **Plan store**: plan-workspace-portfolio-refresh-2026-h2-roadmaps-quality-baselin-f3db23
- **Deployed**: gateway image deployed on k3s via Flux image automation (`fi-mcp-gateway`); library itself local/CI-only
- **CI**: go template family (platform/gitops `/ci/templates/go.yml`) + tech-radar scan

## Now

Nothing actively in flight (last landed work 2026-06-02).

## Next

- [ ] Hub transport for proxy — route `fi-mcp proxy` requests to remote hub servers via the gateway (#1)

## Later

- Kubernetes packaging: Helm charts / Kustomize overlays for MCP Hub installs
- Security hardening: mTLS options, request redaction, external secret stores
- Keep the `mcp-go` dependency current

## Backlog

Full backlog: [P1 issues](https://gitlab.flexinfer.ai/libs/fi-mcp-kit/-/issues/?label_name[]=P1) ·
[P2](https://gitlab.flexinfer.ai/libs/fi-mcp-kit/-/issues/?label_name[]=P2) ·
[P3](https://gitlab.flexinfer.ai/libs/fi-mcp-kit/-/issues/?label_name[]=P3) ·
[Milestones](https://gitlab.flexinfer.ai/libs/fi-mcp-kit/-/milestones)
