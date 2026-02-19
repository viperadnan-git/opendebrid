# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

OpenDebrid is a distributed download management system written in Go. It uses a **controller-worker architecture** where a single controller orchestrates multiple worker nodes. Downloads are handled by pluggable engines (aria2, yt-dlp). HTTP and gRPC are multiplexed on a single port via h2c.

## Build & Development Commands

```bash
make build              # Static binary (CGO_ENABLED=0)
make dev                # Run controller locally
make dev-worker         # Run worker locally
make test               # go test ./...
make lint               # golangci-lint run ./...
make fmt                # gofmt + goimports
make sqlc               # Regenerate sqlc code (internal/database/gen/)
make proto              # Regenerate gRPC code (internal/proto/gen/)
make migrate-up         # Apply database migrations
make migrate-down       # Rollback migrations
```

The binary has two subcommands: `opendebrid controller` and `opendebrid worker`. The controller requires `DATABASE_URL`, the worker requires `CONTROLLER_URL` and `CONTROLLER_TOKEN`.

## Architecture

### Controller-Worker Model

- **Controller** (`internal/controller/`): Hosts REST API (Echo), gRPC server, web UI, scheduler, and can run local engines. Owns the PostgreSQL database.
- **Worker** (`internal/worker/`): Lightweight node that runs engines locally and communicates with the controller via gRPC (register, heartbeat, job dispatch, status push).
- **Shared core** (`internal/core/`): Engine interfaces, event bus, job manager, download service, file server, storage providers, node client abstraction, status loop, and process manager.

### Key Abstractions

- **Engine interface** (`internal/core/engine/engine.go`): All download backends implement `Engine` (Add, BatchStatus, ListFiles, Cancel, ResolveCacheKey, etc.). Engines that require external processes also implement `DaemonEngine`. Registered via `Registry`.
- **NodeClient interface** (`internal/core/node/client.go`): `LocalNodeClient` calls engines directly; `RemoteNodeClient` wraps gRPC. The service layer is transport-agnostic. `internal/core/node/store.go` provides thread-safe NodeClient storage.
- **DownloadService** (`internal/core/service/download.go`): Central business logic entry point. Handles validation, cache lookup (via completed jobs), scheduling, dispatch, persistence, and event publishing.
- **EventBus** (`internal/core/event/`): In-process pub/sub decoupling components (job lifecycle events).
- **Scheduler** (`internal/controller/scheduler/`): Pre-filters nodes by capability/health, then delegates to a LoadBalancer adapter (currently round-robin).
- **StatusLoop** (`internal/core/statusloop/`): Batch polls engine status and delivers updates via the `Sink` interface. Workers run a status loop that periodically pushes batched job status to the controller via `PushJobStatuses` RPC.
- **Process Manager** (`internal/core/process/`): Manages external daemon processes (e.g., aria2c) with startup, stop, restart, and health checks.

### HTTP/gRPC Multiplexing

`internal/mux/handler.go` uses h2c to serve both Echo HTTP routes and gRPC on a single port. Requests with `Content-Type: application/grpc` go to the gRPC server.

### Database Layer

- PostgreSQL with **pgx/v5** driver
- **sqlc** for type-safe query generation: SQL in `internal/database/queries/`, generated Go in `internal/database/gen/`
- Migrations in `internal/database/migrations/`, config in `internal/database/sqlc.yaml`
- Tables: users, nodes, jobs, downloads, download_links, settings
  - `jobs` + `downloads` are normalized (formerly a single table; `cache_entries` was removed)
  - Cache lookups query `jobs` directly via `FindCompletedJobByCacheKey`
  - `downloads` tracks per-download metadata and deduplication via `StorageKey`

### Download Deduplication

Downloads use a deterministic `StorageKey` derived from the cache key. Multiple jobs referencing the same content share a single storage directory, preventing redundant disk writes.

### Configuration

Three-layer system (`internal/config/config.go`): hard-coded defaults → TOML file (`--config`) → `OPENDEBRID_*` environment variables. Env mapping joins TOML tags with `_` (e.g., `OPENDEBRID_ENGINES_ARIA2_RPC_SECRET`). Some secrets (JWT, worker token, admin password) are auto-generated on first boot and stored in the `settings` table.

### Web UI

Hybrid server-rendered + client-side pattern. No frontend build step; templates are embedded at compile time via `go:embed`.

- **Rendering**: Each navigation is a full server-side render using Go `html/template`. Alpine.js v3 then takes over for interactivity and data fetching within the page.
- **Styling**: Pico CSS v2 and Google Fonts loaded from CDN; custom mobile-first CSS embedded inline in `base.html`. The `static/` directories are empty — no local asset files.
- **Templates** (`internal/controller/web/templates/`): `layouts/base.html` (master layout with utility JS), `partials/nav.html` (role-aware nav), and pages: `login`, `signup`, `dashboard`, `downloads`, `files`, `admin/nodes`, `admin/users`, `admin/settings`.
- **Handler** (`internal/controller/web/handler.go`): Registers Echo routes. Auth is session-based (JWT in `od_session` HTTP-only cookie). Protected routes use `requireAuth` middleware; admin routes additionally check `role == "admin"`.
- **Client-side data**: All data is fetched after page load via a global `api(path, opts)` helper that calls `/api/v1/` endpoints. Pages auto-poll every 5–10 seconds using `setInterval` in Alpine `x-init`.
- **Alpine.js patterns**: Per-page `x-data` objects hold reactive state (`jobs`, `loading`, etc.) and async methods. HTML5 `<dialog>` elements are used for modals; form submissions are intercepted with `@submit.prevent` and handled via `api()`.

### Proto / gRPC

Proto definitions in `proto/opendebrid/node.proto`, generated Go code in `internal/proto/gen/`. The `NodeService` RPCs:

- `Register` / `Deregister` — node lifecycle
- `Heartbeat` — bidirectional streaming keepalive
- `DispatchJob` — controller assigns a job to a worker
- `PushJobStatuses` — worker pushes batched status updates (push-based, not polled)
- `ResolveCacheKey` — cache resolution
- `ListNodeStorageKeys` — used for orphan cleanup on worker reconnect

Proto messages use `bytes` fields for extensible JSON payloads.

## Code Generation

After modifying SQL queries in `internal/database/queries/`, run `make sqlc`. After modifying `proto/opendebrid/node.proto`, run `make proto`. Do not edit files in `internal/database/gen/` or `internal/proto/gen/` by hand.
