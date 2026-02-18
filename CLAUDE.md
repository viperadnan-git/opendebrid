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
make migrate-up         # Apply database migrations
make migrate-down       # Rollback migrations
```

The binary has two subcommands: `opendebrid controller` and `opendebrid worker`. The controller requires `DATABASE_URL`, the worker requires `CONTROLLER_URL` and `CONTROLLER_TOKEN`.

## Architecture

### Controller-Worker Model

- **Controller** (`internal/controller/`): Hosts REST API (Echo), gRPC server, web UI, scheduler, and can run local engines. Owns the PostgreSQL database.
- **Worker** (`internal/worker/`): Lightweight node that runs engines locally and communicates with the controller via gRPC (register, heartbeat, job dispatch).
- **Shared core** (`internal/core/`): Engine interfaces, event bus, job manager, download service, file server, storage providers, and node client abstraction.

### Key Abstractions

- **Engine interface** (`internal/core/engine/engine.go`): All download backends implement `Engine` (Add, Status, ListFiles, Cancel, etc.). Registered via `Registry`.
- **NodeClient interface** (`internal/core/node/client.go`): `LocalNodeClient` calls engines directly; `RemoteNodeClient` wraps gRPC. The service layer is transport-agnostic.
- **DownloadService** (`internal/core/service/download.go`): Central business logic entry point. Handles validation, cache lookup, scheduling, dispatch, persistence, and event publishing.
- **EventBus** (`internal/core/event/`): In-process pub/sub decoupling components (job lifecycle events, cache hits).
- **Scheduler** (`internal/controller/scheduler/`): Pre-filters nodes by capability/health, then delegates to a LoadBalancer adapter (currently round-robin).

### HTTP/gRPC Multiplexing

`internal/mux/handler.go` uses h2c to serve both Echo HTTP routes and gRPC on a single port. Requests with `Content-Type: application/grpc` go to the gRPC server.

### Database Layer

- PostgreSQL with **pgx/v5** driver
- **sqlc** for type-safe query generation: SQL in `internal/database/queries/`, generated Go in `internal/database/gen/`
- Migrations in `internal/database/migrations/`, config in `internal/database/sqlc.yaml`
- Tables: users, nodes, jobs, cache_entries, download_links, settings

### Configuration

Three-layer system (`internal/config/config.go`): hard-coded defaults → TOML file (`--config`) → `OD_*` environment variables. Env mapping joins TOML tags with `_` (e.g., `OD_ENGINES_ARIA2_RPC_SECRET`). Some secrets (JWT, worker token, admin password) are auto-generated on first boot and stored in the `settings` table.

### Web UI

Server-rendered with Go `html/template`, Alpine.js, and Pico CSS. No frontend build step. Templates in `internal/controller/web/templates/`, static assets in `internal/controller/web/static/`. All data is fetched client side using API.

### Proto / gRPC

Proto definitions in `proto/opendebrid/node.proto`, generated Go code in `internal/proto/gen/`. The `NodeService` defines RPCs for registration, heartbeat (bidirectional streaming), job dispatch, status reporting, and cache resolution. Proto messages use `bytes` fields for extensible JSON payloads.

## Code Generation

After modifying SQL queries in `internal/database/queries/`, run `make sqlc`. Do not edit files in `internal/database/gen/` or `internal/proto/gen/` by hand.
