# MCP Core Compatibility Matrix

This matrix is the compatibility kill-test baseline for services that consume the MCP core libraries. It is intentionally read-only: the smoke harness inspects manifests by default and only runs consumer test suites when `--run` is explicit.

## Harness

Run from `libs/fi-mcp-kit`:

```bash
scripts/mcp_consumer_smoke.sh --list
scripts/mcp_consumer_smoke.sh --dry-run
scripts/mcp_consumer_smoke.sh --run
```

The harness defaults to `/Users/cblevins/workspace/services` and accepts `--services-root PATH` for another checkout. Use `--consumer NAME` one or more times to narrow the target set.

In run mode it executes `go test ./...` inside each selected service with `-mod=readonly` appended to `GOFLAGS`, then compares `git status --porcelain` before and after each test. Any service repo status change is treated as a smoke failure.

## Matrix

| Consumer | Go | `mcp-go` pin | `fi-mcp-kit` pin | `fi-accel` pin | Local overrides | Smoke command | Current status |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `loom-core` | `1.25.10` | `v0.2.1-0.20260520023524-aa57e61f11bd` | `v0.2.1-0.20260412164817-1a41094d2496` | `v0.0.0-20260318222621-ce3294e404e0` | `go.work` uses `../../libs/fi-accel/go/fiaccel`, `../../libs/fi-mcp-kit`, `../../libs/mcp-go` | `scripts/mcp_consumer_smoke.sh --run --consumer loom-core` | Listed by harness; full smoke not run in this slice |
| `fi-mcp-gateway` | `1.25.5` | `v0.2.1-0.20260116221656-df35197c2d46` | `v0.1.0` | `-` | `replace gitlab.flexinfer.ai/libs/fi-mcp-kit => ../../libs/fi-mcp-kit`; `replace gitlab.flexinfer.ai/libs/mcp-go => ../../libs/mcp-go` | `scripts/mcp_consumer_smoke.sh --run --consumer fi-mcp-gateway` | Listed by harness; full smoke not run in this slice |
| `mcp-orchestra` | `1.25.5` | `v0.2.0` | `v0.2.0` | `-` | `-` | `scripts/mcp_consumer_smoke.sh --run --consumer mcp-orchestra` | Listed by harness; full smoke not run in this slice |
| `mcp-sandbox` | `1.25.5` | `v0.1.0` | `-` | `-` | `-` | `scripts/mcp_consumer_smoke.sh --run --consumer mcp-sandbox` | Listed by harness; full smoke not run in this slice |
| `diff-surgeon` | `1.22.0` | `v0.1.0` | `-` | `v0.0.0-20260303164519-48d2ecf11f45` | `-` | `scripts/mcp_consumer_smoke.sh --run --consumer diff-surgeon` | Listed by harness; full smoke not run in this slice |

## Riskiest Assumption

The riskiest load-bearing assumption is that `go test ./...` is a sufficient cross-consumer smoke for MCP core compatibility. That may miss runtime-only compatibility issues in registry loading, process lifecycle, gateway routing, and websocket transport behavior.

Kill-test outcome/status: this slice establishes the repeatable baseline harness and manifest matrix, but the full cross-consumer smoke has not been run here because this slice changed docs and scripts only. The next integration gate should run:

```bash
scripts/mcp_consumer_smoke.sh --run
```

Pass criteria: every selected consumer exits `go test ./...` successfully and no service repo status changes after the run. Failure in either condition blocks MCP core compatibility sign-off.
