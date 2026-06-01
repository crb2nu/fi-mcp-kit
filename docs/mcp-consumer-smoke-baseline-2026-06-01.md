# MCP Consumer Smoke Baseline - 2026-06-01

Slice: `feat/mcp-consumer-smoke-report`

Collector worktree: `/Users/cblevins/workspace/libs/fi-mcp-kit/.worktrees/feat-mcp-consumer-smoke-report`

Go runtime used for smoke commands: `go version go1.26.3 darwin/arm64`

## Scope and Method

This baseline is read-only for service repositories. The smoke command used was:

```bash
GOFLAGS=-mod=readonly go test ./...
```

`GOFLAGS=-mod=readonly` was used so package tests could run without allowing `go test` to rewrite consumer `go.mod` or `go.sum` files. No service repo fixes were attempted.

The `fi-mcp-kit` slice worktree started clean at:

- Branch: `feat/mcp-consumer-smoke-report`
- Head: `8488c447202173c2da171d417ef58d3e0603e2d9`
- Status before report creation: `## feat/mcp-consumer-smoke-report...origin/main`
- Local `fi-mcp-kit` pin: `gitlab.flexinfer.ai/libs/mcp-go v0.2.1-0.20260116221656-df35197c2d46`
- Local `fi-mcp-kit` replace directives: none
- Local `fi-mcp-kit` `fi-accel` dependency: none

## Local Replacement Inputs

These library checkout states matter because some consumers use workspace-local modules through `go.work` or `replace` directives.

| Repo | Branch | Head | Dirty state |
| --- | --- | --- | --- |
| `/Users/cblevins/workspace/libs/mcp-go` | `main` | `be320b72e921e8a307a48aa848062aabe912ffe0` | Dirty; behind origin by 2; modified `ROADMAP.md`, `toon_decode.go`, `toon_decode_test.go`, `toon_encode.go`; untracked `.cache/`, `content_json_test.go`, `docs/`, and many `ROADMAP_RECONCILIATION_*.md` files. |
| `/Users/cblevins/workspace/libs/fi-mcp-kit` | `main` | `9a620e6c2bd3851de68589378dbaa1aa2f04b881` | Dirty; behind origin by 6; modified `go.mod`, `go.sum`, `vendor/modules.txt`, and many vendored dependency files; untracked roadmap docs and vendor additions. |
| `/Users/cblevins/workspace/libs/fi-accel` | `main` | `29f437ec55e4b4cf1197baf88721060469877237` | Dirty; behind origin by 2; untracked roadmap docs and `go/fiaccel/.cache/`. |

## Consumer Matrix

| Consumer | Current branch / head | Dirty state | Pins | Replace / workspace directives | Smoke result |
| --- | --- | --- | --- | --- | --- |
| `/Users/cblevins/workspace/services/loom-core` | `main` at `5eb7b1821419a246e045b7c5a354ea0b714cf797` | Dirty: modified `internal/daemon/daemon_call.go`; untracked `internal/daemon/daemon_call_test.go`. Note: initial read observed `fix/mills-harvester-vm-daemon-config` at `605ad164eca3cddcc9b5770bdc38108ec58c1520`; final read observed `main`, so this checkout changed during collection. | `fi-accel/go/fiaccel v0.0.0-20260318222621-ce3294e404e0`; `fi-mcp-kit v0.2.1-0.20260412164817-1a41094d2496`; `mcp-go v0.2.1-0.20260520023524-aa57e61f11bd`. | `go.work` uses `.`, `../../libs/fi-accel/go/fiaccel`, `../../libs/fi-mcp-kit`, `../../libs/mcp-go`; default `go test` therefore used local dirty library checkouts instead of only the pinned module versions. | Failed, exit 1. Build failures: `internal/daemon` undefined tests `TestBuildEmbeddedHUDConfig_HarvesterFromEnv`, `TestBuildEmbeddedHUDConfig_HarvesterUnsetLeavesEmpty`, `TestBuildEmbeddedHUDConfig_HarvesterFileConfigWins`; `pkg/agentcontext` undefined `TestSessionReaperTick_PrunesOldEndedSessionsByMaxAge`. Test failure: `pkg/generator TestResolveHubWrapper_PreferenceOrder` resolved `bin/mcp-hub-wrapper` instead of env override. Many linker warnings from local `fi-accel` static archive built for newer macOS 26.2 than linked 26.0. |
| `/Users/cblevins/workspace/services/fi-mcp-gateway` | `main` at `ff460b1f28f8d88350218ecb3b65ae6e1e3311b1` | Dirty: many untracked `docs/roadmap-reconciliation-*.md` files. | `fi-mcp-kit v0.1.0`; `mcp-go v0.2.1-0.20260116221656-df35197c2d46`; no `fi-accel` pin. | `replace gitlab.flexinfer.ai/libs/fi-mcp-kit => ../../libs/fi-mcp-kit`; `replace gitlab.flexinfer.ai/libs/mcp-go => ../../libs/mcp-go`. | Failed before package execution, exit 1: `go: updates to go.mod needed, disabled by -mod=readonly; to update it: go mod tidy`. |
| `/Users/cblevins/workspace/services/mcp-orchestra` | `main` at `dadd589e8ae4122cace420bd4c47c269a5968e94` | Dirty: untracked `docs/` and `orchestra`. | `fi-mcp-kit v0.2.0`; `mcp-go v0.2.0`; no `fi-accel` pin. | No replace directives; no `go.work`. | Passed, exit 0. Packages included `cmd/orchestra`, `internal/executor`, `internal/planner`, `internal/store`, `internal/transport`; remaining packages had no test files. |
| `/Users/cblevins/workspace/services/mcp-sandbox` | `main` at `c250faf5785c4e873f024daa08c08e5e791b1f41` | Dirty: modified `ROADMAP.md`, `cmd/sandbox/main.go`, `internal/server/server.go`; untracked `.cache/`, `docs/`, and `sandbox`. | `mcp-go v0.1.0`; no `fi-mcp-kit` pin; no `fi-accel` pin. | No replace directives; no `go.work`. | Passed, exit 0. All packages compiled; no test files in `cmd/sandbox`, `internal/sandbox`, `internal/server`, or `pkg/types`. |
| `/Users/cblevins/workspace/services/diff-surgeon` | `main` at `5d500c040cb748d14f79cf6c7b636e5709e6effe` | Dirty: modified `ROADMAP.md`; untracked `.cache/` and `docs/`. | `fi-accel/go/fiaccel v0.0.0-20260303164519-48d2ecf11f45`; `mcp-go v0.1.0`; no `fi-mcp-kit` pin. | No replace directives; no `go.work`. | Passed, exit 0. `internal/parser` and `internal/patcher` passed; other packages compiled with no test files. |

## Smoke Summary

| Result | Consumers |
| --- | --- |
| Passed | `mcp-orchestra`, `mcp-sandbox`, `diff-surgeon` |
| Failed / blocked | `loom-core`, `fi-mcp-gateway` |

Pass rate: 3 of 5 consumers.

The failures are baseline blockers in the consumer repos, not fixes made here:

- `fi-mcp-gateway` cannot run read-only smoke until its module graph is tidy while using local replacements.
- `loom-core` is dirty and changed branch/head during collection; its failure includes undefined generated test references plus one generator behavior test failure. The default workspace also pulls in dirty local `fi-accel`, `fi-mcp-kit`, and `mcp-go` checkouts.

## Kill-Test Status

Status: not a clean unblock for proxy backpressure.

The baseline is strong enough to show that three MCP consumers still compile/test with their current pinned module graph, including both older `mcp-go v0.1.0` consumers and `mcp-orchestra` on `mcp-go v0.2.0`. It is not strong enough for an unqualified proxy-backpressure unblock because the gateway consumer did not reach package execution and the largest workspace consumer, `loom-core`, failed from existing dirty/generated-test issues while using dirty local library replacements.

Practical integration note: treat this as a partial pass. Continue integration only if the accepted gate is "at least three non-gateway MCP consumers pass"; require a follow-up gateway smoke after `go mod tidy`/module cleanup for a release-quality gate.
