# Copilot Instructions

## Build, Test, and Lint

This project uses [mise](https://mise.jdx.dev/) as the task runner. Run `mise trust && mise install` on first setup.

```bash
# Build
mise run build            # or: go build ./cmd/git-sync

# Test
mise run test             # default suite (go test ./...)
mise run test:ci          # with race detection

# Run a single test
go test ./internal/syncer -run TestBootstrap_LiveLinuxSource -v

# Lint (runs golangci-lint, gofmt, gomod, shellcheck)
mise run lint

# Format
mise run fmt
```

Set `GIT_TERMINAL_PROMPT=0` when running tests to avoid credential prompts.

## Architecture

`git-sync` is a remote-to-remote Git mirroring tool and Go library. It transfers refs and objects between remotes over smart HTTP (and SSH) without a local checkout, preferring pack relay over local materialization.

### Package layout

- **`gitsync` (root)** — stable public API. Typed `Probe`, `Plan`, `Sync`, `Replicate` requests/results with auth and HTTP client injection.
- **`unstable`** — non-stable advanced controls (`Bootstrap`, `Fetch`, batching knobs, heap measurement). Used by CLI and benchmarks.
- **`internalbridge`** — adapter between the public API types and internal engine.
- **`internal/syncer`** — top-level orchestration and result shaping.
- **`internal/planner`** — ref planning, desired refs, prune policy, checkpoint planning.
- **`internal/strategy/bootstrap`** — one-shot and batched relay bootstrap.
- **`internal/strategy/incremental`** — narrow incremental relay path.
- **`internal/strategy/materialized`** — local object decode/repack push fallback.
- **`internal/gitproto`** — smart HTTP, pkt-line, capability negotiation, fetch/push framing.
- **`internal/validation`** — input normalization and front-loaded validation.
- **`internal/auth`** — credential lookup, token store, Entire token handling.
- **`internal/syncertest`** — shared in-memory test fixtures.
- **`cmd/git-sync`** — CLI entry point.
- **`cmd/git-sync-bench`** — benchmark runner.

### Transfer modes

1. **Bootstrap relay** — empty-target, streams source pack directly into target `receive-pack`.
2. **Incremental relay** — narrow fast-forward updates, same streaming approach.
3. **Materialized fallback** — objects decoded into in-memory `go-git` store, repacked, pushed. Bounded by `--materialized-max-objects`.
4. **Batched bootstrap** — large initial migrations, splits into bounded batches.

### Operation modes

- `sync` — planning + reconciliation (bootstrap, relay, or materialized fallback).
- `replicate` — source-authoritative overwrite, relay-only, fails rather than materializing.

## Key Conventions

- **API stability split**: `gitsync` (root package) is the stable embedding surface. `unstable` is explicitly non-stable. Additions to the root package API require careful review.
- **Error handling**: all errors handled explicitly. The linter enforces `errcheck` with `check-type-assertions: true` and `check-blank: true`.
- **Linting**: golangci-lint v2 with a comprehensive linter set (see `.golangci.yaml`). `wrapcheck` is enabled for non-test code. `nolintlint` requires explanation and specificity.
- **Testing**: the default suite uses in-process smart HTTP servers with no external dependencies. Optional E2E tests are gated behind env vars (`GITSYNC_E2E_GIT_HTTP_BACKEND`, `GITSYNC_E2E_SSH_DOCKER`, `GITSYNC_E2E_LIVE_LINUX`).
- **`testify`**: used for assertions (`require`, `assert`). The `testifylint` linter is enabled with all checks.
- **JSON struct tags**: use `camelCase` (e.g., `json:"skipTlsVerify"`).
- **No `init()` functions**: enforced by `gochecknoinits` linter.
- **Interfaces**: `ireturn` linter restricts returning interfaces; allowed exceptions are listed in `.golangci.yaml`.
