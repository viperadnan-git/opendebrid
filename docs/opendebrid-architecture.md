# OpenDebrid — Architecture & Implementation Plan

> An open-source, self-hosted, multi-node debrid service with pluggable download engines, smart caching, and horizontal scalability.

---

## 1. Project Overview

OpenDebrid is a self-hosted debrid platform that aggregates multiple download engines (aria2, yt-dlp, and future engines) behind a unified API and minimal web UI. It supports multi-node deployments where a **controller** orchestrates **worker** nodes, each running download engines independently. Downloads are cached globally — if any node already has a file, all users get instant access without re-downloading.

### Core Principles

- **Extensibility over complexity** — every component is interface-driven and pluggable
- **Minimal persistence** — the database stores metadata only; engines and filesystem are the source of truth
- **Horizontal scalability** — add nodes by simply pointing workers at the controller
- **Future-proof storage** — file locations are URI-based (`file://`, `s3://`, `rclone://`) from day one
- **Zero vendor lock-in** — standard protocols, no proprietary dependencies

---

## 2. Tech Stack

### Language: **Go 1.23+**

**Rationale:** Single-binary deployment, excellent concurrency primitives (goroutines for job management), low memory footprint ideal for worker nodes, strong HTTP/gRPC standard library, proven track record in infrastructure tooling. Aligns with existing TorrentFlow experience.

### Full Stack Table

| Layer | Technology | Why |
|---|---|---|
| **Language** | Go 1.23+ | Single binary, concurrency, performance |
| **HTTP Framework** | [Echo v4](https://echo.labstack.com/) | Mature, middleware ecosystem, OpenAPI integration |
| **OpenAPI** | [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) | Generate server stubs + types from OpenAPI 3.1 spec |
| **Node Communication** | [gRPC](https://grpc.io/) + Protobuf | Bidirectional streaming, code generation, future-proof |
| **Database** | PostgreSQL 16+ | JSONB for flexible metadata, proven reliability |
| **DB Driver** | [pgx v5](https://github.com/jackc/pgx) | Pure Go, connection pooling, LISTEN/NOTIFY support |
| **DB Migrations** | [golang-migrate](https://github.com/golang-migrate/migrate) | SQL-based migrations, CLI + library |
| **ORM/Query** | [sqlc](https://sqlc.dev/) | Type-safe SQL → Go code generation, zero runtime overhead |
| **Configuration** | [knadh/koanf](https://github.com/knadh/koanf) + TOML parser | Lightweight, composable providers (file + env + flags), no reflection bloat |
| **CLI** | [urfave/cli/v3](https://github.com/urfave/cli) | Lightweight, subcommands, env binding, no bloat |
| **Logging** | [zerolog](https://github.com/rs/zerolog) | Zero-allocation structured logging, JSON/pretty output, sub-microsecond overhead |
| **Auth** | JWT (access) + UUID API Keys | [golang-jwt/jwt](https://github.com/golang-jwt/jwt), [google/uuid](https://github.com/google/uuid) v7 |
| **Password Hashing** | bcrypt (stdlib `golang.org/x/crypto`) | Battle-tested, adjustable cost |
| **Frontend** | [HTMX](https://htmx.org/) + [Alpine.js](https://alpinejs.dev/) + [Pico CSS](https://picocss.com/) | No build step, server-rendered, progressive enhancement |
| **Templating** | Go `html/template` (stdlib) | Secure by default, zero dependencies |
| **File Server** | Custom (stdlib `net/http`) | Range requests, signed URLs, bandwidth control |
| **Containerization** | Docker + Docker Compose | Multi-stage builds, separate controller/worker images |
| **Process Manager** | Built-in (`os/exec` + goroutines) | Manages aria2 and future daemons as child processes, no external dependencies |
| **Download: Torrents** | [aria2](https://aria2.github.io/) (JSON-RPC) | Mature, battle-tested, built-in RPC |
| **Download: Media** | [yt-dlp](https://github.com/yt-dlp/yt-dlp) | CLI invocation, JSON output, extensive site support |

---

## 3. Architecture

### 3.1 High-Level Topology

```
┌──────────────────────────────────────────────────────────┐
│                       CONTROLLER NODE                         │
│  ┌──────────┐  ┌──────────┐                               │
│  │ REST API │  │ gRPC Hub │                               │
│  │ (Echo)   │  │ (Server) │                               │
│  └────┬─────┘  └────┬─────┘                               │
│       │              │                                    │
│  ┌────▼──────────────▼─────────────────────────────────┐  │
│  │           DownloadService (shared)                   │  │
│  │  Add · Status · ListFiles · Cancel · Remove          │  │
│  └──┬───────────┬──────────────┬───────────────────────┘  │
│     │           │              │                          │
│     ▼           ▼              ▼                          │
│  ┌──────┐  ┌────────┐  ┌────────────┐                    │
│  │Engine│  │Scheduler│  │ NodeClient │                    │
│  │Regis.│  │(filter +│  │ (abstract) │                    │
│  └──┬───┘  │adapter) │  └──┬─────┬──┘                    │
│     │      └────────┘     │     │                        │
│     │           LocalNodeClient RemoteNodeClient         │
│     │               │          │ (gRPC)                  │
│     │      ┌────────┘          │                         │
│     ▼      ▼                   │                         │
│  ┌──────────────┐              │                         │
│  │ Core Engines │              │                         │
│  │ aria2 yt-dlp │              │                         │
│  └──────┬───────┘              │                         │
│         │                      │                         │
│  ┌──────▼──────────────────────│─────────────────────┐   │
│  │              EventBus                              │   │
│  │  job.* · cache.* · node.* events                   │   │
│  │  → SSE Broadcaster, Cache Manager, Metrics, etc.   │   │
│  └────────────────────┬──────────────────────────────┘   │
│                       │                                   │
│  ┌────────────────────▼────────────────────────────────┐  │
│  │             PostgreSQL Database                      │  │
│  └──────────────────────────────────────────────────────┘  │
└───────────────────────┬──────────────────────────────────┘
                        │ gRPC (TLS)
           ┌────────────┼────────────┐
           │            │            │
     ┌─────▼──┐   ┌─────▼──┐   ┌─────▼──┐
     │ WORKER  │   │ WORKER  │   │ WORKER  │
     │ NODE 1 │   │ NODE 2 │   │ NODE N │
     │        │   │        │   │        │
     │EventBus│   │EventBus│   │EventBus│
     │Service │   │Service │   │Service │
     │Engines │   │Engines │   │Engines │
     │ Files  │   │ Files  │   │ Files  │
     └────────┘   └────────┘   └────────┘
```

### 3.2 Controller vs Worker Responsibilities

| Capability | Controller | Worker |
|---|:---:|:---:|
| REST API (external) | ✅ | ❌ |
| Web UI | ✅ | ❌ |
| gRPC Server (node registry) | ✅ | ❌ |
| gRPC Client (register + heartbeat) | ❌ | ✅ |
| Download Engines (aria2, yt-dlp) | ✅ | ✅ |
| File Serving (signed URLs) | ✅ | ✅ |
| Job Execution | ✅ | ✅ |
| Database (PostgreSQL) | ✅ (hosts) | ❌ (queries via gRPC) |
| Node Health Reporting | ✅ (self) | ✅ → controller |
| Cache Lookup | ✅ (global) | ❌ (delegated) |
| Job Scheduling / Load Balancing | ✅ | ❌ |
| Admin Management | ✅ | ❌ |

### 3.3 Single Binary, Two Modes

```bash
# Run as controller (full features + is also a node)
opendebrid controller --config /etc/opendebrid/controller.toml

# Run as worker (core engine only, registers with controller)
opendebrid worker --config /etc/opendebrid/worker.toml
```

Both modes share the same binary. The `worker` mode excludes: REST API, Web UI, database hosting, admin endpoints. The worker communicates exclusively with the controller via gRPC.

---

## 4. Project Structure

```
opendebrid/
├── cmd/
│   ├── root.go                     # urfave/cli app setup, global flags
│   ├── controller.go                   # Controller subcommand
│   └── worker.go                    # Worker subcommand
├── main.go                         # func main() → cmd.Execute()
│
├── internal/
│   ├── config/
│   │   ├── config.go               # Config structs (koanf binding)
│   │   └── defaults.go             # Default values
│   │
│   ├── core/                       # === SHARED CORE (controller + worker) ===
│   │   ├── engine/                  # Download engine interfaces + registry
│   │   │   ├── engine.go            # Engine interface definition
│   │   │   ├── registry.go          # Engine registry (register/lookup by name)
│   │   │   ├── aria2/
│   │   │   │   ├── client.go        # aria2 JSON-RPC client
│   │   │   │   ├── engine.go        # Engine interface implementation
│   │   │   │   ├── types.go         # aria2-specific types
│   │   │   │   └── watcher.go       # Status polling → publishes events via EventBus
│   │   │   └── ytdlp/
│   │   │       ├── engine.go        # Engine interface implementation
│   │   │       ├── runner.go        # yt-dlp CLI process manager
│   │   │       └── types.go         # yt-dlp-specific types
│   │   │
│   │   ├── event/                   # Event bus (decoupled communication)
│   │   │   ├── bus.go               # Bus interface + in-process implementation
│   │   │   ├── types.go             # Event types + payload structs
│   │   │   └── middleware.go        # Event middleware (logging, error recovery)
│   │   │
│   │   ├── process/                 # External daemon process manager
│   │   │   ├── manager.go           # Manager interface + DaemonManager implementation
│   │   │   └── health.go            # Health check probing
│   │   │
│   │   ├── node/                    # Node client abstraction
│   │   │   ├── client.go            # NodeClient interface
│   │   │   ├── local.go             # LocalNodeClient (direct engine calls)
│   │   │   └── remote.go            # RemoteNodeClient (wraps gRPC)
│   │   │
│   │   ├── job/
│   │   │   ├── manager.go           # Job lifecycle management
│   │   │   ├── queue.go             # Per-node job queue with limits
│   │   │   └── types.go             # Job states, events
│   │   │
│   │   ├── service/
│   │   │   ├── download.go          # Unified download workflow (add/status/list/cancel)
│   │   │   ├── cache.go             # Cache check + resolution logic
│   │   │   └── files.go             # File browsing + signed URL generation
│   │   │
│   │   ├── fileserver/
│   │   │   ├── server.go            # HTTP file server (range requests)
│   │   │   ├── signer.go            # URL signing / expiry
│   │   │   └── middleware.go        # Bandwidth, auth validation
│   │   │
│   │   └── storage/
│   │       ├── provider.go          # StorageProvider interface
│   │       ├── local.go             # Local filesystem provider
│   │       └── rclone.go            # Rclone provider (future, stubbed)
│   │
│   ├── controller/                      # === CONTROLLER-ONLY ===
│   │   ├── server.go                # Controller bootstrap (API + gRPC + core)
│   │   │
│   │   ├── api/                     # REST API (Echo)
│   │   │   ├── router.go            # Route registration
│   │   │   ├── middleware/
│   │   │   │   ├── auth.go          # JWT + API key middleware
│   │   │   │   ├── ratelimit.go     # Per-user rate limiting
│   │   │   │   └── admin.go         # Admin-only middleware
│   │   │   ├── handlers/
│   │   │   │   ├── auth.go          # Login, register, API key regeneration
│   │   │   │   ├── aria2.go         # /api/v1/aria2/* (thin: parses req → service)
│   │   │   │   ├── ytdlp.go         # /api/v1/ytdlp/* (thin: parses req → service)
│   │   │   │   ├── downloads.go     # /api/v1/downloads/* (thin: → service)
│   │   │   │   ├── files.go         # /api/v1/files/* (thin: → service)
│   │   │   │   ├── nodes.go         # /api/v1/admin/nodes/* (admin)
│   │   │   │   ├── users.go         # /api/v1/admin/users/* (admin)
│   │   │   │   └── settings.go      # /api/v1/admin/settings/*
│   │   │   └── response/
│   │   │       └── response.go      # Standardized JSON response helpers
│   │   │
│   │   ├── web/                     # Web UI
│   │   │   ├── templates/           # Go html/template files
│   │   │   │   ├── layouts/
│   │   │   │   │   └── base.html
│   │   │   │   ├── pages/
│   │   │   │   │   ├── dashboard.html
│   │   │   │   │   ├── downloads.html
│   │   │   │   │   ├── files.html
│   │   │   │   │   └── admin/
│   │   │   │   │       ├── nodes.html
│   │   │   │   │       ├── users.html
│   │   │   │   │       └── settings.html
│   │   │   │   └── partials/        # HTMX partial responses
│   │   │   │       ├── download_row.html
│   │   │   │       ├── file_list.html
│   │   │   │       └── node_card.html
│   │   │   └── static/
│   │   │       ├── css/
│   │   │       │   └── app.css      # Pico CSS overrides
│   │   │       └── js/
│   │   │           ├── htmx.min.js
│   │   │           ├── alpine.min.js
│   │   │           └── app.js       # Minimal custom JS
│   │   │
│   │   ├── grpc/                    # gRPC server (node management)
│   │   │   ├── server.go            # gRPC server setup
│   │   │   └── handlers.go          # Node registration, heartbeat, job dispatch
│   │   │
│   │   ├── scheduler/
│   │   │   ├── scheduler.go         # Job → Node assignment
│   │   │   ├── adapter.go           # LoadBalancer interface
│   │   │   └── roundrobin.go        # Default round-robin adapter
│   │   │
│   │   └── cache/
│   │       └── manager.go           # Global cache lookup (hash/URL → node + path)
│   │
│   ├── worker/                       # === WORKER-ONLY ===
│   │   ├── server.go                # Worker bootstrap (core + gRPC client)
│   │   ├── grpc/
│   │   │   └── client.go            # Register with controller, heartbeat, receive jobs
│   │   └── reporter.go              # Disk usage, engine status, health reporting
│   │
│   └── database/
│       ├── db.go                    # Connection pool setup (pgx)
│       ├── migrations/
│       │   ├── 001_init.up.sql
│       │   ├── 001_init.down.sql
│       │   └── ...
│       ├── queries/                 # sqlc query files
│       │   ├── users.sql
│       │   ├── jobs.sql
│       │   ├── nodes.sql
│       │   ├── cache.sql
│       │   └── settings.sql
│       ├── sqlc.yaml                # sqlc configuration
│       └── gen/                     # sqlc generated code (DO NOT EDIT)
│           ├── db.go
│           ├── models.go
│           └── queries.sql.go
│
├── proto/                           # Protobuf definitions
│   └── opendebrid/
│       ├── node.proto               # Node registration, heartbeat
│       └── job.proto                # Job dispatch, status updates
│
├── api/                             # OpenAPI specification
│   └── openapi.yaml                 # OpenAPI 3.1 spec (source of truth)
│
├── deployments/
│   ├── Dockerfile.controller
│   ├── Dockerfile.worker
│   ├── docker-compose.yml           # Full stack (controller + postgres + worker)
│   └── docker-compose.worker.yml     # Worker-only
│
├── configs/
│   ├── controller.example.toml
│   └── worker.example.toml
│
├── scripts/
│   ├── generate.sh                  # Run all code generators (sqlc, oapi, proto)
│   └── seed.sh                      # Seed admin account
│
├── go.mod
├── go.sum
├── Makefile
├── LICENSE                          # MIT / AGPLv3 (TBD)
└── README.md
```

---

## 5. Interface Contracts

### 5.1 Engine Interface

The most critical abstraction. Every download engine implements this:

```go
package engine

import (
    "context"
    "time"
)

// Engine is the core download engine interface.
// Implement this to add a new download backend (e.g., qBittorrent, Transmission, gallery-dl).
type Engine interface {
    // === Identity ===

    // Name returns the unique engine identifier (e.g., "aria2", "ytdlp")
    Name() string

    // Capabilities returns what this engine supports
    Capabilities() Capabilities

    // === Lifecycle ===
    // Engines have different runtime models: aria2 runs as a persistent daemon,
    // yt-dlp spawns per-request. The lifecycle methods abstract this away.

    // Init performs one-time setup (validate binaries exist, create dirs, etc.)
    Init(ctx context.Context, cfg EngineConfig) error

    // Start launches the engine backend if needed (e.g., aria2 daemon).
    // For on-demand engines (yt-dlp), this is a no-op that returns nil.
    Start(ctx context.Context) error

    // Stop gracefully shuts down the engine backend.
    // Must drain active jobs or mark them for recovery.
    Stop(ctx context.Context) error

    // Health checks if the engine backend is reachable and operational.
    Health(ctx context.Context) HealthStatus

    // === Download Operations ===

    // Add submits a new download job. Returns an engine-specific job ID.
    Add(ctx context.Context, req AddRequest) (AddResponse, error)

    // Status returns the current state of a job by engine-specific ID.
    Status(ctx context.Context, engineJobID string) (JobStatus, error)

    // ListFiles returns files associated with a completed/in-progress job.
    ListFiles(ctx context.Context, engineJobID string) ([]FileInfo, error)

    // Cancel stops and removes a job.
    Cancel(ctx context.Context, engineJobID string) error

    // Remove removes a job and its files. Always cleans up — no orphaned files.
    Remove(ctx context.Context, engineJobID string) error

    // === Cache ===

    // ResolveCacheKey computes the dedup key for a URL BEFORE downloading.
    // This may involve I/O (e.g., yt-dlp --print id, parsing magnet URI).
    // Returns empty string if cache key cannot be determined pre-download.
    ResolveCacheKey(ctx context.Context, url string) (CacheKey, error)
}

// EngineConfig is passed during Init. Engines extract what they need.
type EngineConfig struct {
    DownloadDir string
    MaxConcurrent int
    Extra       map[string]string // Engine-specific config (rpc_url, binary path, etc.)
}

type Capabilities struct {
    // Input types this engine accepts
    AcceptsSchemes  []string  // ["magnet", "http", "https", "ftp"]
    AcceptsMIME     []string  // ["application/x-bittorrent"] (optional, for file uploads)

    // Feature flags
    SupportsPlaylist  bool
    SupportsStreaming bool
    SupportsInfo      bool  // Can extract metadata without downloading (yt-dlp --dump-json)

    // Custom capabilities for future engines (e.g., "supports_seeding": true)
    Custom map[string]bool
}

// CacheKey wraps the dedup key with its type for proper namespacing.
type CacheKey struct {
    Type  CacheKeyType
    Value string       // The actual key (info_hash, extractor:id, sha256(url), etc.)
}

// Full returns the namespaced key: "aria2:hash:abc123" or "ytdlp:custom_id:dQw4..."
func (c CacheKey) Full() string {
    return string(c.Type) + ":" + c.Value
}

type CacheKeyType string
const (
    CacheKeyHash     CacheKeyType = "hash"
    CacheKeyURL      CacheKeyType = "url"
    CacheKeyCustomID CacheKeyType = "custom_id"
)

type AddRequest struct {
    URL         string
    Options     map[string]string // Engine-specific options
}

type AddResponse struct {
    EngineJobID string
    CacheKey    CacheKey
}

// JobStatus represents the engine's view of a job.
// The State field uses core states. Engine-specific states go in EngineState.
type JobStatus struct {
    EngineJobID    string
    State          JobState    // Core state (queued, downloading, completed, failed, cancelled)
    EngineState    string      // Engine-specific state (e.g., "seeding", "postprocessing", "checking")
    Progress       float64     // 0.0 - 1.0
    Speed          int64       // bytes/sec
    TotalSize      int64       // bytes
    DownloadedSize int64       // bytes
    ETA            time.Duration
    Error          string      // Non-empty on failure
    Extra          map[string]any // Engine-specific metadata (peers, seeders, format, etc.)
}

// JobState — core states only. Engines map their internal states to these.
// New engines MUST map to one of these. Engine-specific sub-states go in EngineState.
type JobState string
const (
    StateQueued      JobState = "queued"
    StateActive      JobState = "active"       // downloading, seeding, processing — all "in progress"
    StateCompleted   JobState = "completed"
    StateFailed      JobState = "failed"
    StateCancelled   JobState = "cancelled"
)

type FileInfo struct {
    Path         string   // Relative path within job directory
    Size         int64
    StorageURI   string   // file:///data/jobs/abc/video.mp4 or s3://bucket/...
    ContentType  string   // MIME type
}

type HealthStatus struct {
    OK      bool
    Message string
    Latency time.Duration
}
```

**Why `State` vs `EngineState`?** Core states (`active`, `completed`, `failed`) are what the scheduler, cache, UI, and database care about. Engine states (`seeding`, `postprocessing`, `checking_resume_data`) are display-only and engine-specific. Adding a new engine never requires modifying the core state enum — the engine just maps its internal states to one of the 5 core states and puts the detail in `EngineState`.

### 5.2 Storage Provider Interface

```go
package storage

import (
    "context"
    "io"
)

// Provider abstracts file storage. Local filesystem now, rclone/S3 later.
type Provider interface {
    // Name returns the provider identifier (e.g., "local", "s3", "rclone")
    Name() string

    // Store saves content to a path, returns the storage URI
    Store(ctx context.Context, reader io.Reader, destPath string) (storageURI string, err error)

    // Open returns a ReadSeekCloser for serving files (supports range requests)
    Open(ctx context.Context, storageURI string) (io.ReadSeekCloser, FileMetadata, error)

    // Delete removes a file
    Delete(ctx context.Context, storageURI string) error

    // Exists checks if a file exists
    Exists(ctx context.Context, storageURI string) (bool, error)

    // DiskUsage returns storage stats for this provider
    DiskUsage(ctx context.Context) (DiskStats, error)
}

type FileMetadata struct {
    Size        int64
    ContentType string
    ModTime     time.Time
}

type DiskStats struct {
    Total     int64 // bytes
    Used      int64
    Available int64
}
```

### 5.3 Process Manager (Daemon Lifecycle)

Go manages external daemon processes (aria2, future qBittorrent, Transmission) as child processes — no Supervisord, no systemd, no external process manager.

```go
package process

import (
    "context"
    "time"
)

// Daemon represents an external process managed by the application.
// Each engine that needs a background daemon implements this.
// On-demand engines (yt-dlp) don't need this — they spawn per-request.
type Daemon interface {
    // Name returns the daemon identifier (e.g., "aria2c")
    Name() string

    // Command returns the binary and args to launch
    Command() (bin string, args []string)

    // ReadyCheck returns a probe that determines if the daemon is ready.
    // Called repeatedly after Start() until it returns true or timeout.
    ReadyCheck() ReadyProbe

    // Healthy checks if a running daemon is still responsive.
    Healthy(ctx context.Context) bool
}

type ReadyProbe struct {
    Check    func(ctx context.Context) bool
    Interval time.Duration // polling interval (e.g., 200ms)
    Timeout  time.Duration // give up after (e.g., 10s)
}

// Manager manages daemon lifecycles as child processes.
type Manager struct { /* ... */ }

// Register adds a daemon to be managed.
func (m *Manager) Register(d Daemon) { /* ... */ }

// StartAll launches all registered daemons, waits for ready.
func (m *Manager) StartAll(ctx context.Context) error { /* ... */ }

// StopAll sends SIGTERM, waits for graceful exit, then SIGKILL.
func (m *Manager) StopAll(ctx context.Context) error { /* ... */ }

// Watch monitors daemons in a goroutine. Restarts crashed processes
// with exponential backoff (1s, 2s, 4s, max 30s).
func (m *Manager) Watch(ctx context.Context) { /* ... */ }
```

**How aria2 uses this:**

```go
type Aria2Daemon struct {
    rpcPort   int
    rpcSecret string
    downloadDir string
}

func (a *Aria2Daemon) Name() string { return "aria2c" }

func (a *Aria2Daemon) Command() (string, []string) {
    return "aria2c", []string{
        "--enable-rpc",
        "--rpc-listen-port=" + strconv.Itoa(a.rpcPort),
        "--rpc-secret=" + a.rpcSecret,
        "--dir=" + a.downloadDir,
        "--daemon=false",  // run in foreground, Go manages lifecycle
    }
}

func (a *Aria2Daemon) ReadyCheck() process.ReadyProbe {
    return process.ReadyProbe{
        Check: func(ctx context.Context) bool {
            // try JSON-RPC getVersion call
            _, err := a.client.GetVersion(ctx)
            return err == nil
        },
        Interval: 200 * time.Millisecond,
        Timeout:  10 * time.Second,
    }
}
```

**Future engines** (qBittorrent, Transmission) implement `Daemon` and get auto-restart, health monitoring, and graceful shutdown for free. On-demand engines (yt-dlp, gallery-dl) skip this entirely — they use `os/exec` per invocation inside their `Engine.Add()`.

### 5.4 Load Balancer Adapter Interface

```go
package scheduler

import "context"

// LoadBalancer determines which node should handle a new download job.
type LoadBalancer interface {
    // Name returns the adapter name (e.g., "round-robin", "least-loaded")
    Name() string

    // SelectNode picks the best node from the provided candidates.
    // Candidates are PRE-FILTERED by the scheduler (online, has engine, has disk space).
    // The adapter only decides ordering/preference among valid candidates.
    SelectNode(ctx context.Context, req SelectRequest, candidates []NodeInfo) (NodeSelection, error)
}

type SelectRequest struct {
    Engine        string   // "aria2" or "ytdlp"
    EstimatedSize int64    // bytes, 0 if unknown
    PreferredNode string   // user preference, empty = auto
}

// NodeInfo is the scheduler's view of a node. Adapters use this to make decisions.
type NodeInfo struct {
    ID             string
    Endpoint       string   // gRPC endpoint or "local"
    IsLocal        bool     // true for controller node
    Engines        []string // engines available on this node
    DiskAvailable  int64    // bytes
    ActiveJobs     int
    MaxConcurrent  int
}

type NodeSelection struct {
    NodeID   string
    Endpoint string
    Reason   string // why this node was selected (for logging/debugging)
}
```

**Key design: the scheduler pre-filters, the adapter only ranks.** The `Scheduler` (not the adapter) is responsible for filtering out nodes that are offline, don't have the requested engine, or have insufficient disk space. The `LoadBalancer` adapter receives only valid candidates and decides which one wins. This means adapters never need to understand node health or engine capabilities — they focus purely on selection strategy (round-robin, least-loaded, weighted, geographic, etc.).

### 5.5 NodeClient Interface (Transport Abstraction)

The service layer never talks to gRPC directly. It talks to a `NodeClient`, which abstracts whether the target node is local (same process) or remote (gRPC).

```go
package node

import "context"

// NodeClient abstracts communication with a node (local or remote).
// The controller holds one NodeClient per registered node.
// For the controller's own node: LocalNodeClient (direct engine calls).
// For workers: RemoteNodeClient (gRPC calls).
// Future: could be HTTP, NATS, or any other transport.
type NodeClient interface {
    // NodeID returns the target node identifier
    NodeID() string

    // DispatchJob sends a download job to this node
    DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error)

    // GetJobStatus queries live job status from the engine on this node
    GetJobStatus(ctx context.Context, jobID string, engineJobID string) (engine.JobStatus, error)

    // GetJobFiles queries file list from the engine on this node
    GetJobFiles(ctx context.Context, jobID string, engineJobID string) ([]engine.FileInfo, error)

    // CancelJob tells the node to cancel a job
    CancelJob(ctx context.Context, jobID string, engineJobID string) error

    // Healthy returns whether this node is reachable
    Healthy() bool
}

type DispatchRequest struct {
    JobID     string
    Engine    string
    URL       string
    CacheKey  string
    Options   map[string]string
}

type DispatchResponse struct {
    Accepted    bool
    EngineJobID string
    Error       string
}
```

**Two implementations:**

| Type | When | How |
|---|---|---|
| `LocalNodeClient` | Controller executing on itself | Calls `engine.Add()` directly, no serialization |
| `RemoteNodeClient` | Controller dispatching to worker | Wraps gRPC calls to `NodeService` |

This means swapping gRPC for NATS, HTTP, or any protocol requires changing only `RemoteNodeClient` — zero changes to service layer, scheduler, or handlers.

### 5.6 EventBus (Decoupled Component Communication)

Components publish events; other components subscribe. Prevents the service layer from becoming a god object that orchestrates every side effect.

```go
package event

import "context"

// Bus is the event dispatcher. Components publish events, subscribers react.
// In-process implementation for now. Can be backed by Redis PubSub, NATS,
// or PostgreSQL LISTEN/NOTIFY in the future without changing publishers/subscribers.
type Bus interface {
    // Publish emits an event to all subscribers of that event type.
    Publish(ctx context.Context, event Event) error

    // Subscribe registers a handler for a specific event type.
    // Returns an unsubscribe function.
    Subscribe(eventType EventType, handler Handler) (unsubscribe func())
}

type Handler func(ctx context.Context, event Event) error

type Event struct {
    Type      EventType
    Timestamp time.Time
    Payload   any          // Type-asserted by subscribers
}

type EventType string
const (
    // Job lifecycle
    EventJobCreated    EventType = "job.created"
    EventJobStarted    EventType = "job.started"
    EventJobProgress   EventType = "job.progress"
    EventJobCompleted  EventType = "job.completed"
    EventJobFailed     EventType = "job.failed"
    EventJobCancelled  EventType = "job.cancelled"

    // Cache
    EventCacheHit      EventType = "cache.hit"
    EventCacheEvicted  EventType = "cache.evicted"

    // Node
    EventNodeOnline    EventType = "node.online"
    EventNodeOffline   EventType = "node.offline"
    EventNodeDiskLow   EventType = "node.disk_low"
)

// === Event Payloads (type-safe) ===

type JobEvent struct {
    JobID       string
    UserID      string
    NodeID      string
    Engine      string
    CacheKey    string
    Progress    float64   // only for EventJobProgress
    Error       string    // only for EventJobFailed
}

type CacheEvent struct {
    CacheKey string
    JobID    string
    NodeID   string
}

type NodeEvent struct {
    NodeID        string
    DiskAvailable int64
}
```

**Who publishes and subscribes:**

| Event | Publisher | Subscribers |
|---|---|---|
| `job.completed` | Job Manager | Cache Manager (insert entry), SSE Broadcaster, future: Webhook sender |
| `job.progress` | Engine Watcher | SSE Broadcaster |
| `job.failed` | Job Manager | Cache Manager (cleanup), SSE Broadcaster |
| `cache.hit` | Cache Manager | Analytics (increment counter) |
| `node.offline` | Heartbeat Monitor | Scheduler (exclude node), Cache Manager (invalidate entries) |
| `node.disk_low` | Health Reporter | Scheduler (deprioritize node) |

**Why not direct function calls?** Adding a webhook notification system in the future means subscribing to `job.completed` — zero changes to the Job Manager or Service Layer. Same for Prometheus metrics, audit logging, or any future observer.

### 5.7 Download Service (Shared Business Logic)

The service layer is the single entry point for all download operations. Handlers and gRPC endpoints both call this — never the engines or node clients directly.

```go
package service

import "context"

// DownloadService encapsulates the unified download workflow.
// Both REST handlers and gRPC job dispatch use this.
//
// Dependencies (injected):
//   - engine.Registry     → lookup engines by name
//   - node.NodeClient     → dispatch to local or remote nodes
//   - scheduler.Scheduler → pick best node (pre-filters + adapter)
//   - event.Bus           → publish job lifecycle events
//   - database queries    → persist metadata
type DownloadService interface {
    // Add runs the full workflow: validate → cache check → schedule → dispatch → persist → publish
    Add(ctx context.Context, req AddDownloadRequest) (AddDownloadResponse, error)

    // Status fetches live status from the engine (via NodeClient)
    Status(ctx context.Context, jobID string, userID string) (DownloadStatus, error)

    // ListFiles fetches files from the engine (via NodeClient)
    ListFiles(ctx context.Context, jobID string, userID string) ([]FileInfo, error)

    // Cancel stops a download and publishes EventJobCancelled
    Cancel(ctx context.Context, jobID string, userID string) error

    // Remove deletes a job and its files (admin or owner). Always cleans up.
    Remove(ctx context.Context, jobID string, userID string) error

    // List returns paginated downloads for a user, optionally filtered by engine
    List(ctx context.Context, req ListRequest) (ListResponse, error)
}

type AddDownloadRequest struct {
    URL         string
    Engine      string            // "aria2" or "ytdlp"
    UserID      string
    Options     map[string]string
    PreferNode  string            // optional
}

type AddDownloadResponse struct {
    JobID    string
    CacheHit bool
    NodeID   string
    Status   DownloadStatus
}

type ListRequest struct {
    UserID string
    Engine string // empty = all engines
    Status string // empty = all statuses
    Page   int
    Limit  int
}
```

**Flow through the service layer:**

```
REST Handler / gRPC Handler
        │
        ▼
  DownloadService.Add()
        │
        ├─▶ registry.Get(engine) → engine instance
        ├─▶ engine.ResolveCacheKey(url) → cache key
        ├─▶ db.LookupCache(cacheKey)
        │       ├─ HIT  → publish EventCacheHit → return existing job
        │       └─ MISS → continue
        ├─▶ scheduler.SelectNode(engine, candidates)
        ├─▶ nodeClients[nodeID].DispatchJob()    ← NodeClient abstraction
        │       (LocalNodeClient or RemoteNodeClient)
        ├─▶ db.InsertJob() + db.InsertCacheEntry()
        ├─▶ eventBus.Publish(EventJobCreated)    ← EventBus decoupling
        └─▶ return response
```

The service layer is thin orchestration glue. It doesn't implement caching logic, scheduling logic, or engine communication — it delegates to injected interfaces. This means every component is independently testable with mocks.

---

## 6. Database Schema

Minimal persistence — engines and filesystem are the source of truth.

```sql
-- 001_init.up.sql

-- UUID v7 generation (time-sortable, K-sortable for index performance)
-- Requires pg_uuidv7 extension OR generate in Go via google/uuid and pass as parameter
CREATE EXTENSION IF NOT EXISTS pg_uuidv7;

-- ========================
-- Users & Authentication
-- ========================
CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    username    VARCHAR(64) UNIQUE NOT NULL,
    email       VARCHAR(255) UNIQUE,
    password    VARCHAR(255) NOT NULL,  -- bcrypt hash
    role        VARCHAR(16) NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    api_key     UUID UNIQUE NOT NULL DEFAULT uuid_generate_v7(), -- simple UUID key, regenerable
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_api_key ON users(api_key);

-- ========================
-- Nodes
-- ========================
CREATE TABLE nodes (
    id              VARCHAR(64) PRIMARY KEY,  -- e.g., "controller", "worker-us-east-1"
    name            VARCHAR(128) NOT NULL,
    grpc_endpoint   VARCHAR(255),              -- NULL for controller (local)
    file_endpoint   VARCHAR(255) NOT NULL,     -- HTTP endpoint for file serving
    engines         JSONB NOT NULL DEFAULT '[]', -- ["aria2", "ytdlp"]
    is_controller       BOOLEAN NOT NULL DEFAULT false,
    is_online       BOOLEAN NOT NULL DEFAULT false,
    disk_total      BIGINT NOT NULL DEFAULT 0,
    disk_available  BIGINT NOT NULL DEFAULT 0,
    last_heartbeat  TIMESTAMPTZ,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata        JSONB DEFAULT '{}'         -- extensible node metadata
);

-- ========================
-- Jobs (Download Tasks)
-- ========================
CREATE TABLE jobs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id         VARCHAR(64) NOT NULL REFERENCES nodes(id),
    engine          VARCHAR(32) NOT NULL,       -- "aria2" or "ytdlp"
    engine_job_id   VARCHAR(255),               -- Engine-internal ID (GID, process ID)
    url             TEXT NOT NULL,               -- Original URL/magnet
    cache_key       VARCHAR(512) NOT NULL,       -- Dedup key (info_hash, yt-dlp ID, etc.)
    status          VARCHAR(32) NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'active', 'completed', 'failed', 'cancelled')),
    engine_state    VARCHAR(64),                 -- Engine-specific sub-state (seeding, postprocessing, etc.)
    file_location   TEXT,                        -- StorageURI of the root download directory
    error_message   TEXT,
    metadata        JSONB DEFAULT '{}',          -- Extensible: priority, tags, extra engine data
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_jobs_user ON jobs(user_id);
CREATE INDEX idx_jobs_cache ON jobs(cache_key);
CREATE INDEX idx_jobs_node ON jobs(node_id);
CREATE INDEX idx_jobs_status ON jobs(status);
CREATE INDEX idx_jobs_engine ON jobs(engine);

-- ========================
-- Cache Registry
-- ========================
-- Global cache: maps cache_key → existing job/node for instant dedup
CREATE TABLE cache_entries (
    cache_key       VARCHAR(512) PRIMARY KEY,
    job_id          UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    node_id         VARCHAR(64) NOT NULL REFERENCES nodes(id),
    engine          VARCHAR(32) NOT NULL,
    file_location   TEXT NOT NULL,               -- StorageURI
    total_size      BIGINT DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_accessed   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    access_count    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_cache_node ON cache_entries(node_id);
CREATE INDEX idx_cache_accessed ON cache_entries(last_accessed);

-- ========================
-- Signed Download Links
-- ========================
CREATE TABLE download_links (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    file_path   TEXT NOT NULL,                   -- Relative path within job
    token       VARCHAR(128) UNIQUE NOT NULL,    -- Signed token
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    access_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_download_links_token ON download_links(token);
CREATE INDEX idx_download_links_expires ON download_links(expires_at);

-- ========================
-- App Settings (key-value)
-- ========================
CREATE TABLE settings (
    key         VARCHAR(128) PRIMARY KEY,
    value       JSONB NOT NULL,
    description TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed defaults (values with NULL are auto-generated on first boot)
INSERT INTO settings (key, value, description) VALUES
    ('jwt_secret', 'null', 'JWT signing secret — auto-generated on first boot if null'),
    ('worker_auth_token', 'null', 'Shared token for worker registration — auto-generated on first boot if null'),
    ('link_expiry_minutes', '60', 'Default download link expiry in minutes'),
    ('per_user_concurrent_downloads', '5', 'Max concurrent downloads per user'),
    ('per_node_concurrent_downloads', '10', 'Max concurrent downloads per node'),
    ('min_disk_free_bytes', '1073741824', 'Minimum free disk space per node (1GB)'),
    ('lru_cleanup_enabled', 'false', 'Enable LRU-based automatic cleanup'),
    ('default_load_balancer', '"round-robin"', 'Load balancing adapter name'),
    ('registration_enabled', 'true', 'Allow new user registration');

-- ========================
-- Triggers
-- ========================
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_jobs_updated_at BEFORE UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
```

---

## 7. API Design

### 7.1 Base Paths

```
/api/v1/auth/*           # Authentication (public)
/api/v1/aria2/*          # aria2 engine endpoints
/api/v1/ytdlp/*          # yt-dlp engine endpoints
/api/v1/downloads/*      # Unified download view (all engines)
/api/v1/files/*          # File browsing + signed URL generation
/api/v1/admin/*          # Admin-only management
/web/*                   # HTMX web UI routes
```

### 7.2 Endpoint Summary

#### Auth (`/api/v1/auth`)

| Method | Path | Description |
|---|---|---|
| POST | `/register` | Register new user (if enabled) |
| POST | `/login` | Login → JWT token |
| POST | `/refresh` | Refresh JWT |
| GET | `/me` | Current user info + API key |
| POST | `/apikey/regenerate` | Regenerate API key (invalidates old one) |

#### aria2 (`/api/v1/aria2`)

| Method | Path | Description |
|---|---|---|
| POST | `/add` | Add magnet/HTTP download |
| GET | `/jobs` | List user's aria2 jobs |
| GET | `/jobs/:id` | Job status (live from engine) |
| GET | `/jobs/:id/files` | List files in torrent (live) |
| DELETE | `/jobs/:id` | Cancel/remove job |

#### yt-dlp (`/api/v1/ytdlp`)

| Method | Path | Description |
|---|---|---|
| POST | `/add` | Add URL for download |
| POST | `/info` | Extract info without downloading |
| GET | `/jobs` | List user's yt-dlp jobs |
| GET | `/jobs/:id` | Job status |
| GET | `/jobs/:id/files` | List downloaded files |
| DELETE | `/jobs/:id` | Cancel/remove job |

#### Downloads (`/api/v1/downloads`) — Unified

| Method | Path | Description |
|---|---|---|
| GET | `/` | All downloads across engines |
| GET | `/:id` | Any download by ID |

#### Files (`/api/v1/files`)

| Method | Path | Description |
|---|---|---|
| GET | `/jobs/:id/browse` | Browse files in a job |
| POST | `/jobs/:id/link` | Generate signed download link |
| GET | `/d/:token` | Serve file (signed URL, public) |

#### Admin (`/api/v1/admin`)

| Method | Path | Description |
|---|---|---|
| GET | `/nodes` | List all nodes |
| GET | `/nodes/:id` | Node details + health |
| DELETE | `/nodes/:id` | Remove node |
| GET | `/users` | List all users |
| PATCH | `/users/:id` | Update user (role, active) |
| DELETE | `/users/:id` | Delete user |
| GET | `/settings` | Get all settings |
| PATCH | `/settings/:key` | Update setting |
| POST | `/cleanup` | Manual LRU cleanup trigger |

### 7.3 Standard Response Format

```json
{
    "success": true,
    "data": { ... },
    "error": null,
    "meta": {
        "page": 1,
        "per_page": 20,
        "total": 150
    }
}
```

```json
{
    "success": false,
    "data": null,
    "error": {
        "code": "CACHE_HIT",
        "message": "This torrent is already downloaded",
        "details": {
            "job_id": "uuid",
            "node_id": "worker-1"
        }
    }
}
```

---

## 8. gRPC Protocol

### 8.1 Node Service (`node.proto`)

```protobuf
syntax = "proto3";
package opendebrid;

service NodeService {
    // Worker calls this on startup to register with controller
    rpc Register(RegisterRequest) returns (RegisterResponse);

    // Worker sends periodic heartbeats (bidirectional stream)
    rpc Heartbeat(stream HeartbeatPing) returns (stream HeartbeatPong);

    // Controller dispatches a download job to worker
    rpc DispatchJob(DispatchJobRequest) returns (DispatchJobResponse);

    // Worker reports job status update to controller
    rpc ReportJobStatus(JobStatusReport) returns (Ack);

    // Controller queries worker for live job status
    rpc GetJobStatus(JobStatusRequest) returns (JobStatusResponse);

    // Controller queries worker for file list
    rpc GetJobFiles(JobFilesRequest) returns (JobFilesResponse);

    // Controller tells worker to cancel a job
    rpc CancelJob(CancelJobRequest) returns (Ack);

    // Controller tells worker to remove a job and its files
    rpc RemoveJob(RemoveJobRequest) returns (Ack);

    // Controller asks worker to resolve cache key for a URL (may involve I/O)
    rpc ResolveCacheKey(CacheKeyRequest) returns (CacheKeyResponse);
}

message RegisterRequest {
    string node_id = 1;
    string name = 2;
    string file_endpoint = 3;
    repeated string engines = 4;
    int64 disk_total = 5;
    int64 disk_available = 6;
    string version = 7;
    string auth_token = 8;
    bytes metadata = 9;          // raw JSON, extensible
}

message RegisterResponse {
    bool accepted = 1;
    string message = 2;
    int32 heartbeat_interval_sec = 3;
    bytes config = 4;            // raw JSON, controller can push config to worker
}

message HeartbeatPing {
    string node_id = 1;
    int64 disk_available = 2;
    int32 active_jobs = 3;
    map<string, bool> engine_health = 4;
    int64 timestamp = 5;
    bytes metrics = 6;           // raw JSON: cpu, memory, bandwidth, etc.
}

message HeartbeatPong {
    bool acknowledged = 1;
    repeated PendingAction actions = 2;
}

message PendingAction {
    string type = 1;             // "cancel_job", "cleanup", "update_config", etc.
    bytes payload = 2;           // raw JSON
}

message DispatchJobRequest {
    string job_id = 1;
    string engine = 2;
    string url = 3;
    string cache_key = 4;
    string user_id = 5;
    map<string, string> options = 6;
    bytes extra = 7;             // raw JSON: priority, tags, etc.
}

message DispatchJobResponse {
    bool accepted = 1;
    string engine_job_id = 2;
    string error = 3;
}

message JobStatusReport {
    string job_id = 1;
    string node_id = 2;
    string status = 3;           // Core state: queued, active, completed, failed, cancelled
    string engine_state = 4;     // Engine-specific: seeding, postprocessing, etc.
    double progress = 5;
    int64 speed = 6;
    int64 total_size = 7;
    int64 downloaded_size = 8;
    string file_location = 9;
    string error = 10;
    bytes extra = 11;            // raw JSON: engine-specific metadata
}

message JobStatusRequest {
    string job_id = 1;
    string engine_job_id = 2;
    string engine = 3;
}

message JobStatusResponse {
    JobStatusReport status = 1;
}

message JobFilesRequest {
    string job_id = 1;
    string engine_job_id = 2;
    string engine = 3;
}

message JobFilesResponse {
    repeated FileEntry files = 1;
}

message FileEntry {
    string path = 1;
    int64 size = 2;
    string storage_uri = 3;
    string content_type = 4;
}

message CancelJobRequest {
    string job_id = 1;
    string engine_job_id = 2;
    string engine = 3;
}

message RemoveJobRequest {
    string job_id = 1;
    string engine_job_id = 2;
    string engine = 3;
}

message CacheKeyRequest {
    string engine = 1;
    string url = 2;
}

message CacheKeyResponse {
    string cache_key_type = 1;
    string cache_key_value = 2;
    string error = 3;
}

message Ack {
    bool ok = 1;
    string message = 2;
}
```

**Extensibility via `bytes` (raw JSON):** Every message that might need future fields uses a `bytes` field containing raw JSON. On the Go side, this is `json.Marshal`/`json.Unmarshal` — the same thing we use everywhere else. No protobuf reflection, no `structpb` overhead. Adding node metrics or job priorities is just adding a key to the JSON — zero proto schema changes, zero redeployment coordination.

---

## 9. Cache Flow

### 9.1 Cache Key Generation

| Engine | Cache Key Strategy |
|---|---|
| aria2 (magnet) | `aria2:{info_hash}` — extracted from magnet URI |
| aria2 (HTTP) | `aria2:{sha256(normalized_url)}` |
| yt-dlp | `ytdlp:{extractor}:{video_id}` — from `--print id` / `--print extractor` |

### 9.2 Cache Lookup Sequence

```
User adds URL
     │
     ▼
┌─────────────────┐
│ Compute cache_key│
└────────┬────────┘
         │
         ▼
┌─────────────────────┐     ┌──────────────────────┐
│ Lookup in            │────▶│ Cache HIT:           │
│ cache_entries table  │     │ - Check node online  │
└────────┬────────────┘     │ - Check files exist   │
         │                   │ - Return existing job │
         │ MISS              │ - Increment counter   │
         ▼                   └──────────────────────┘
┌─────────────────────┐
│ Schedule on best     │
│ node via LoadBalancer│
└────────┬────────────┘
         │
         ▼
┌─────────────────────┐
│ Dispatch job         │
│ Insert into jobs +   │
│ cache_entries tables │
└─────────────────────┘
```

### 9.3 Cache Invalidation

Cache entries are invalidated when:
- Admin manually cleans up files
- Node goes offline permanently (admin removes node)
- LRU cleanup runs (when enabled)
- Original job is deleted

---

## 10. File Serving

### 10.1 Signed URL Flow

```
User requests file
     │
     ▼
┌───────────────────────┐
│ POST /api/v1/files/    │
│   jobs/:id/link       │
│                        │
│ Body: { "path":        │
│   "video.mp4" }       │
└──────────┬────────────┘
           │
           ▼
┌───────────────────────┐
│ Generate token:        │
│ HMAC-SHA256(           │
│   job_id + path +      │
│   user_id + expiry,    │
│   server_secret        │
│ )                      │
│ Store in download_links│
└──────────┬────────────┘
           │
           ▼
┌────────────────────────────────────────┐
│ Return URL:                             │
│ https://{node_file_endpoint}/d/{token}  │
│                                         │
│ Token encodes:                          │
│ - job_id, file_path, user_id, expiry    │
│ - node routes to correct file server    │
└─────────────────────────────────────────┘
```

### 10.2 File Server Features

| Feature | Implementation |
|---|---|
| Range requests | `http.ServeContent` (stdlib) handles `Range` headers |
| Resume support | Via range requests — no extra work needed |
| Expiry | Check `expires_at` before serving |
| Anti-hotlinking | Token validation + optional referer check |
| Bandwidth tracking | Middleware wraps `ResponseWriter` to count bytes |
| Content-Disposition | Set `attachment; filename="..."` header |
| MIME detection | `http.DetectContentType` + extension-based fallback |
| Streaming | `io.Copy` from storage provider → response writer |

---

## 11. Node Communication Lifecycle

### 11.1 Worker Startup Sequence

```
1. Worker starts → reads config (CONTROLLER_URL + CONTROLLER_TOKEN, everything else defaults)
2. Node ID auto-set from os.Hostname() if not configured
3. Worker scans $PATH for engine binaries (aria2c, yt-dlp) → auto-enables found engines
4. Process Manager starts daemon engines (aria2c), waits for ReadyCheck
5. Worker initializes local file server on default port (8081)
6. Worker calls controller.Register() via gRPC with:
   - node_id, name, file_endpoint, discovered engines, disk stats
7. Controller validates auth token, registers node in DB
8. Controller responds with heartbeat interval
9. Worker opens bidirectional Heartbeat stream
10. Worker sends HeartbeatPing every N seconds:
    - disk_available, active_jobs, engine_health
11. Controller responds with HeartbeatPong:
    - ack + any pending actions (cancel jobs, etc.)
```

### 11.2 Worker Offline Handling

| Event | Action |
|---|---|
| Missed 3 heartbeats | Controller marks node `is_online = false` |
| Node comes back online | Worker re-registers, controller reconciles jobs |
| Active downloads on offline node | Controller marks jobs as `failed`, cache entries invalidated |
| Node removed by admin | All jobs reassigned/failed, cache entries cleaned |

---

## 12. Configuration

### 12.0 Design Principle

**One env var to start, everything else has a sensible default.** The controller needs only `DATABASE_URL`. A worker needs only `CONTROLLER_URL`. All other values auto-generate or use safe defaults. Override only what you need via TOML config or env vars.

Env vars always override TOML values. The env var name is the TOML path uppercased with `_` separators and prefixed with `OD_` (e.g., `server.port` → `OD_SERVER_PORT`). Top-level vars like `DATABASE_URL` have no prefix for convenience.

### 12.1 Defaults Table

| Key | Default | Auto-generated? | Notes |
|---|---|:---:|---|
| `server.host` | `0.0.0.0` | | |
| `server.port` | `8080` | | API + Web UI |
| `server.grpc_port` | `9090` | | Inter-node communication |
| `database.url` | **required** | | Controller only |
| `database.max_connections` | `25` | | |
| `auth.jwt_secret` | — | ✅ random 32-byte hex | Auto-generated on first run, persisted in DB `settings` table. Logged as warning on first boot. |
| `auth.jwt_expiry` | `24h` | | |
| `auth.admin_username` | `admin` | | |
| `auth.admin_password` | — | ✅ random 16-char | Auto-generated on first run if admin doesn't exist. Printed to stdout once. |
| `node.id` | `hostname` | ✅ `os.Hostname()` | Must be unique across cluster |
| `node.name` | same as `node.id` | ✅ | |
| `node.file_server_port` | `8081` | | |
| `node.download_dir` | `/data/downloads` | | Base directory for all engines |
| `node.worker_auth_token` | — | ✅ random 32-byte hex | Auto-generated on first run, persisted in DB. Workers use this to register. |
| `engines.aria2.enabled` | `true` | | Disabled if aria2c binary not found |
| `engines.aria2.rpc_url` | `http://localhost:6800/jsonrpc` | | |
| `engines.aria2.rpc_secret` | `""` (empty) | | |
| `engines.aria2.max_concurrent` | `5` | | |
| `engines.aria2.download_dir` | `{node.download_dir}/aria2` | | |
| `engines.ytdlp.enabled` | `true` | | Disabled if yt-dlp binary not found |
| `engines.ytdlp.binary` | `yt-dlp` | | Resolved from `$PATH` |
| `engines.ytdlp.max_concurrent` | `3` | | |
| `engines.ytdlp.download_dir` | `{node.download_dir}/ytdlp` | | |
| `engines.ytdlp.default_format` | `bestvideo+bestaudio/best` | | |
| `storage.provider` | `local` | | |
| `storage.local.base_path` | `{node.download_dir}` | | |
| `scheduler.adapter` | `round-robin` | | |
| `limits.min_disk_free` | `1GB` | | |
| `limits.per_user_concurrent` | `5` | | |
| `limits.per_node_concurrent` | `10` | | |
| `limits.link_expiry` | `60m` | | |
| `limits.lru_enabled` | `false` | | |
| `logging.level` | `info` | | debug, info, warn, error |
| `logging.format` | `pretty` | | json, pretty |
| `controller.grpc_endpoint` | **required** | | Worker only |
| `controller.auth_token` | **required** | | Worker only — get from controller's auto-generated token |

### 12.2 Minimal Controller Setup

```bash
# Literally one env var. That's it.
DATABASE_URL="postgres://user:pass@localhost:5432/opendebrid?sslmode=disable" \
  opendebrid controller
```

On first boot:
1. Runs migrations
2. Auto-generates `jwt_secret`, persists to `settings` table
3. Auto-generates `worker_auth_token`, persists to `settings` table
4. Creates `admin` user with random password
5. Prints to stdout:

```
═══════════════════════════════════════════════════════
  OpenDebrid Controller started
  
  Admin credentials (save these, shown only once):
    Username: admin
    Password: Kx8mP2vL9nQw4rTy
  
  Worker auth token (use this when adding workers):
    Token: a1b2c3d4e5f6...
  
  API:   http://0.0.0.0:8080
  Files: http://0.0.0.0:8081
  gRPC:  0.0.0.0:9090
═══════════════════════════════════════════════════════
```

### 12.3 Minimal Worker Setup

```bash
# Two env vars. Controller endpoint + token from controller's first boot output.
CONTROLLER_URL="controller.example.com:9090" \
CONTROLLER_TOKEN="a1b2c3d4e5f6..." \
  opendebrid worker
```

Node ID auto-generates from hostname. Engines auto-detect from `$PATH`. Done.

### 12.4 Full Controller Config (all overrides)

Only create a TOML file if you need to override defaults:

```toml
# controller.toml — all values shown are defaults, remove what you don't need

[server]
host = "0.0.0.0"
port = 8080
grpc_port = 9090

[database]
url = "${DATABASE_URL}"      # REQUIRED — no default
max_connections = 25

[auth]
# jwt_secret = ""            # auto-generated on first run, persisted in DB
jwt_expiry = "24h"
admin_username = "admin"
# admin_password = ""        # auto-generated on first run

[node]
# id = ""                    # defaults to hostname
# name = ""                  # defaults to node.id
file_server_port = 8081
download_dir = "/data/downloads"
# worker_auth_token = ""     # auto-generated on first run, persisted in DB

[engines.aria2]
enabled = true               # auto-disabled if aria2c not in $PATH
rpc_url = "http://localhost:6800/jsonrpc"
rpc_secret = ""
max_concurrent = 5
# download_dir = ""          # defaults to {node.download_dir}/aria2

[engines.ytdlp]
enabled = true               # auto-disabled if yt-dlp not in $PATH
binary = "yt-dlp"
max_concurrent = 3
default_format = "bestvideo+bestaudio/best"
# download_dir = ""          # defaults to {node.download_dir}/ytdlp

[storage]
provider = "local"
# [storage.local]
# base_path = ""             # defaults to node.download_dir

[scheduler]
adapter = "round-robin"

[limits]
min_disk_free = "1GB"
per_user_concurrent = 5
per_node_concurrent = 10
link_expiry = "60m"
lru_enabled = false

[logging]
level = "info"
format = "pretty"
```

### 12.5 Full Worker Config (all overrides)

```toml
# worker.toml — minimal, most values inherited from defaults

[controller]
grpc_endpoint = "${CONTROLLER_URL}"    # REQUIRED
auth_token = "${CONTROLLER_TOKEN}"     # REQUIRED

[node]
# id = ""                    # defaults to hostname
# name = ""                  # defaults to node.id
file_server_port = 8081
download_dir = "/data/downloads"

[engines.aria2]
enabled = true
rpc_url = "http://localhost:6800/jsonrpc"
rpc_secret = ""
max_concurrent = 5

[engines.ytdlp]
enabled = true
binary = "yt-dlp"
max_concurrent = 3

[storage]
provider = "local"

[logging]
level = "info"
format = "pretty"
```

---

## 13. Docker Deployment

### 13.1 Controller Dockerfile

```dockerfile
# === Build Stage ===
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git make
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o opendebrid .

# === Runtime Stage ===
FROM alpine:3.20
RUN apk add --no-cache \
    aria2 \
    python3 \
    py3-pip \
    ffmpeg \
    ca-certificates \
    && pip3 install --break-system-packages yt-dlp

COPY --from=builder /build/opendebrid /usr/local/bin/opendebrid

RUN mkdir -p /data/downloads/aria2 /data/downloads/ytdlp

EXPOSE 8080 8081 9090

# Only DATABASE_URL is required. Everything else auto-generates.
ENTRYPOINT ["opendebrid", "controller"]
```

### 13.2 Worker Dockerfile

```dockerfile
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git make
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o opendebrid .

FROM alpine:3.20
RUN apk add --no-cache \
    aria2 \
    python3 \
    py3-pip \
    ffmpeg \
    ca-certificates \
    && pip3 install --break-system-packages yt-dlp

COPY --from=builder /build/opendebrid /usr/local/bin/opendebrid

RUN mkdir -p /data/downloads/aria2 /data/downloads/ytdlp

EXPOSE 8081

# Only CONTROLLER_URL and CONTROLLER_TOKEN are required.
# Node ID defaults to container hostname. Engines auto-detected from $PATH.
ENTRYPOINT ["opendebrid", "worker"]
```

### 13.3 Docker Compose (Full Stack)

```yaml
version: "3.9"

services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: opendebrid
      POSTGRES_USER: opendebrid
      POSTGRES_PASSWORD: ${DB_PASSWORD:-opendebrid}
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U opendebrid"]
      interval: 5s
      timeout: 5s
      retries: 5

  controller:
    build:
      context: .
      dockerfile: deployments/Dockerfile.controller
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      DATABASE_URL: "postgres://opendebrid:${DB_PASSWORD:-opendebrid}@postgres:5432/opendebrid?sslmode=disable"
      # Everything else auto-generates on first boot.
      # Check container logs for admin password and worker token.
    ports:
      - "8080:8080"   # REST API + Web UI
      - "8081:8081"   # File server
      - "9090:9090"   # gRPC (for workers)
    volumes:
      - controller_data:/data/downloads

  worker:
    build:
      context: .
      dockerfile: deployments/Dockerfile.worker
    environment:
      CONTROLLER_URL: "controller:9090"
      CONTROLLER_TOKEN: "${CONTROLLER_TOKEN}"  # from controller's first boot output
    ports:
      - "8082:8081"   # File server (different host port)
    volumes:
      - worker_data:/data/downloads
    # Scale: docker compose up --scale worker=3

volumes:
  pgdata:
  controller_data:
  worker_data:
```

---

## 14. Web UI Architecture

### 14.1 Technology Approach

No build step. No npm. No bundling. Server-rendered HTML with progressive enhancement.

| Technology | Role |
|---|---|
| **Go `html/template`** | Server-side rendering, type-safe templates |
| **HTMX** | AJAX requests, partial page updates, SSE for live status |
| **Alpine.js** | Minimal client-side interactivity (modals, dropdowns, toggles) |
| **Pico CSS** | Classless semantic CSS framework, dark mode built-in |

### 14.2 Page Inventory

| Page | Route | Features |
|---|---|---|
| Login | `/web/login` | Username/password form |
| Dashboard | `/web/` | Active downloads, node health overview |
| aria2 Downloads | `/web/aria2` | Add magnet/URL, list jobs, real-time progress via SSE |
| yt-dlp Downloads | `/web/ytdlp` | Add URL, extract info, list jobs |
| File Browser | `/web/files/:job_id` | Browse files, generate download links |
| Admin: Nodes | `/web/admin/nodes` | Node list, health, disk usage |
| Admin: Users | `/web/admin/users` | User management |
| Admin: Settings | `/web/admin/settings` | App settings editor |

### 14.3 Real-Time Updates

HTMX Server-Sent Events (SSE) for live download progress:

```html
<!-- Download row that auto-updates via SSE -->
<tr hx-ext="sse"
    sse-connect="/web/api/sse/jobs/{{ .Job.ID }}"
    sse-swap="status"
    hx-swap="outerHTML">
    <td>{{ .Job.URL }}</td>
    <td>{{ .Job.Status }}</td>
    <td>
        <progress value="{{ .Job.Progress }}" max="100"></progress>
    </td>
</tr>
```

---

## 15. Security Model

### 15.1 Authentication Flow

```
┌──────────────────────────────────┐
│          Authentication          │
│                                  │
│  ┌───────────┐  ┌────────────┐  │
│  │ JWT Token │  │  API Key   │  │
│  │ (Web UI)  │  │ (API)      │  │
│  └─────┬─────┘  └─────┬──────┘  │
│        │               │         │
│        ▼               ▼         │
│  ┌─────────────────────────────┐ │
│  │   Auth Middleware (Echo)    │ │
│  │   Extracts user_id + role  │ │
│  └──────────┬──────────────────┘ │
│             │                    │
│             ▼                    │
│  ┌──────────────────┐           │
│  │  Admin Middleware │           │
│  │  (role == admin)  │           │
│  └──────────────────┘           │
└──────────────────────────────────┘
```

### 15.2 Security Measures

| Concern | Mitigation |
|---|---|
| Password storage | bcrypt with cost 12 |
| JWT | Short-lived access (1h) + refresh tokens (24h), secret auto-generated and persisted |
| API keys | One UUID v7 per user, direct lookup via unique index, regenerable |
| File access | Signed URLs with HMAC-SHA256, time-limited |
| Worker auth | Auto-generated shared token, persisted in DB `settings`, passed via gRPC metadata |
| gRPC transport | TLS (mandatory in production) |
| Rate limiting | Per-user, configurable via settings |
| Input validation | OpenAPI schema validation middleware |
| SQL injection | Parameterized queries via sqlc |
| XSS | Go `html/template` auto-escapes |

---

## 16. Implementation Phases

### Phase 1 — Foundation (Weeks 1-2)

**Goal:** Core infrastructure, single node, one engine working.

- [ ] Project scaffolding (Go modules, directory structure, Makefile)
- [ ] Configuration system (koanf + TOML parser, env var overlay)
- [ ] Database setup (PostgreSQL, migrations, sqlc codegen)
- [ ] User system (register, login, JWT, UUID API key per user, admin auto-creation)
- [ ] Engine interface definition (with lifecycle: Init/Start/Stop)
- [ ] Engine registry (register/lookup by name)
- [ ] EventBus (in-process implementation, event types, handlers)
- [ ] NodeClient interface + LocalNodeClient implementation
- [ ] aria2 engine implementation (JSON-RPC client, lifecycle, add/status/list/cancel)
- [ ] Job manager (create, track, update status via EventBus)
- [ ] DownloadService implementation (orchestrates registry + cache + node client)
- [ ] Basic REST API (auth + aria2 endpoints, thin handlers → service)
- [ ] OpenAPI spec (initial version)

### Phase 2 — Complete Single Node (Weeks 3-4)

**Goal:** Both engines, caching, file serving, web UI.

- [ ] yt-dlp engine implementation (CLI runner, info extraction, lifecycle, ResolveCacheKey)
- [ ] Cache manager (lookup, invalidation — subscribes to EventBus events)
- [ ] Storage provider interface + local filesystem implementation
- [ ] File server (range requests, signed URLs, expiry)
- [ ] REST API: yt-dlp endpoints, downloads unified view, file endpoints
- [ ] Web UI: all pages with HTMX (login, dashboard, downloads, files, admin)
- [ ] SSE broadcaster (subscribes to EventBus job.progress events)
- [ ] Admin endpoints (users, settings)
- [ ] Per-user and per-node concurrent download limits
- [ ] Disk space monitoring and enforcement (1GB reserve)

### Phase 3 — Multi-Node (Weeks 5-6)

**Goal:** Controller-worker architecture, horizontal scaling.

- [ ] Protobuf definitions (with `bytes` JSON extensibility) + gRPC codegen
- [ ] gRPC server on controller (node registration, heartbeat, job dispatch, cache key resolution)
- [ ] gRPC client on worker (register, heartbeat stream, receive jobs, report status)
- [ ] RemoteNodeClient implementation (wraps gRPC, same interface as LocalNodeClient)
- [ ] Worker mode (stripped down binary: core + gRPC client + file server + EventBus)
- [ ] Scheduler with pre-filtering (online, has engine, has disk) + round-robin adapter
- [ ] Node health tracking (heartbeat monitoring → publishes node.online/offline events)
- [ ] Cross-node cache resolution (cache hit on node A serves user via node A's file server)
- [ ] Admin endpoints: node management
- [ ] Docker images (controller + worker)
- [ ] Docker Compose full stack

### Phase 4 — Hardening & Polish (Week 7+)

**Goal:** Production-ready, documented, releasable.

- [ ] Comprehensive error handling and recovery
- [ ] Graceful shutdown (drain jobs, close connections)
- [ ] API rate limiting
- [ ] OpenAPI spec finalization + Swagger UI integration
- [ ] Manual LRU cleanup endpoint
- [ ] Integration tests (engine mocks, multi-node simulation)
- [ ] README, deployment guide, API docs
- [ ] CI/CD (GitHub Actions: lint, test, build, Docker push)
- [ ] License selection

### Future Phases (Planned Architecture, Not Implemented)

- **rclone storage provider** — implement StorageProvider interface, move files to S3/GDrive/etc.
- **Additional engines** — qBittorrent, gallery-dl, Transmission (implement Engine interface)
- **Additional LB adapters** — least-loaded, geographic, weighted (implement LoadBalancer interface)
- **WebSocket** for real-time dashboard updates (EventBus subscriber)
- **File checksums** for deduplication verification
- **User tiers** — rate limits, storage quotas per tier
- **Prometheus metrics** — EventBus subscriber that exports counters/histograms
- **Webhook notifications** — EventBus subscriber for job.completed/job.failed
- **Alternative node transports** — NATS, HTTP (implement NodeClient interface)
- **Remote EventBus** — Redis PubSub or NATS backend (implement Bus interface)

---

## 17. Key Design Decisions & Rationale

| Decision | Choice | Rationale |
|---|---|---|
| Go over Rust | Go | Faster development, sufficient performance for I/O-bound workload, easier hiring/contribution |
| Go over Python | Go | Single binary, no runtime, lower memory, better concurrency model for node management |
| Echo over Fiber | Echo | More mature ecosystem, better middleware support, native `net/http` compatible |
| sqlc over GORM | sqlc | Zero runtime overhead, SQL is the source of truth, no magic, easier to debug |
| gRPC over REST | gRPC (for inter-node) | Bidirectional streaming for heartbeats, strong typing via protobuf, code generation |
| HTMX over React | HTMX | No build step, server-rendered (Go templates), minimal JS, aligns with "minimal UI" requirement |
| PostgreSQL over SQLite | PostgreSQL | JSONB for flexible metadata, LISTEN/NOTIFY for future events, battle-tested at scale |
| Go process manager over Supervisord | `os/exec` + Daemon interface | Zero external deps, auto-restart with backoff, engines get lifecycle for free |
| StorageURI from day one | URI-based paths | Enables seamless rclone migration without schema changes |
| Interface-driven design | Go interfaces | Every component (engine, storage, scheduler, node client) is a pluggable interface |
| Service layer over fat handlers | DownloadService | Business logic shared between REST + gRPC, handlers stay thin (parse → delegate → respond) |
| EventBus over direct calls | Pub/sub events | Decouples job lifecycle from side effects (SSE, cache, metrics). Adding webhooks = 1 subscriber, 0 changes |
| NodeClient abstraction | Local + Remote | Service layer doesn't know if target is local or gRPC. Swap transport = 1 implementation change |
| Core states + engine sub-states | 5 core + freeform | New engines never modify the state enum. Engine-specific states are display-only metadata |
| Scheduler pre-filters, adapter ranks | Separation of concerns | Adapters focus on selection strategy only, never health/capability logic. Simpler to write + test |
| Proto `bytes` over `Struct` | Raw JSON in proto fields | Same flexibility, zero reflection overhead, consistent with JSON used everywhere else |

---

## 18. Extensibility Guide

### Adding a New Download Engine

1. Create `internal/core/engine/myengine/` directory
2. Implement the `Engine` interface (§5.1) including:
   - `Init()` / `Start()` / `Stop()` for lifecycle
   - `ResolveCacheKey()` for dedup strategy
   - Map internal states to 5 core `JobState` values, put detail in `EngineState`
3. Register in `engine/registry.go`
4. The `DownloadService` auto-discovers via registry — **no service layer changes**
5. Add thin API handler in `internal/controller/api/handlers/myengine.go` (delegates to service)
6. Add API routes with `/api/v1/myengine/*` base path
7. Update OpenAPI spec + web UI page

**Zero changes required in:** DownloadService, Scheduler, Cache Manager, EventBus, NodeClient, gRPC proto (engine name is a string, not an enum).

### Adding a New Storage Provider

1. Create `internal/core/storage/myprovider.go`
2. Implement the `Provider` interface (§5.2)
3. Add config section in koanf struct + TOML config
4. Register in storage factory

**Zero changes required in:** Engine, FileServer, Database schema (StorageURI is opaque string).

### Adding a New Load Balancer Adapter

1. Create `internal/controller/scheduler/myadapter.go`
2. Implement the `LoadBalancer` interface (§5.4)
   - Receives pre-filtered candidates only (online, has engine, has disk)
   - Focus purely on ranking/selection strategy
3. Register in scheduler factory
4. Set via `default_load_balancer` setting

**Zero changes required in:** Scheduler pre-filter logic, DownloadService, Node management.

### Adding a New Side Effect (Webhooks, Metrics, Audit Log, etc.)

1. Create a subscriber (e.g., `internal/controller/webhooks/subscriber.go`)
2. Subscribe to relevant events on the `EventBus`:
   ```go
   bus.Subscribe(event.EventJobCompleted, webhookHandler)
   bus.Subscribe(event.EventJobFailed, webhookHandler)
   ```
3. Register during server bootstrap

**Zero changes required in:** DownloadService, Job Manager, Engine, or any existing subscriber.

### Adding a New Node Transport (e.g., NATS instead of gRPC)

1. Create `internal/core/node/nats.go`
2. Implement the `NodeClient` interface (§5.5)
3. Swap in the node client factory based on config

**Zero changes required in:** DownloadService, Scheduler, API handlers.

### Extension Points Summary

| To add... | Implement interface | Changes to existing code |
|---|---|---|
| Download engine | `Engine` | Registry entry + API route only |
| Storage backend | `StorageProvider` | Config + factory only |
| LB strategy | `LoadBalancer` | Config only |
| Side effect | `EventBus` subscriber | Bootstrap registration only |
| Node transport | `NodeClient` | Factory only |
| Auth method | Echo middleware | Middleware chain only |

---

## 19. Development Commands

```bash
# Setup
make setup           # Install tools (sqlc, oapi-codegen, protoc-gen-go)
make generate        # Run all code generators

# Development
make dev             # Run controller with hot reload (air)
make dev-worker       # Run worker with hot reload

# Database
make migrate-up      # Run migrations
make migrate-down    # Rollback last migration
make migrate-create  # Create new migration (NAME=xxx)
make sqlc            # Regenerate sqlc code

# Build
make build           # Build binary
make docker-controller   # Build controller Docker image
make docker-worker    # Build worker Docker image

# Test
make test            # Run all tests
make test-integration # Run integration tests (requires Docker)

# Code Quality
make lint            # golangci-lint
make fmt             # gofmt + goimports
```

---

## 20. Environment Variables

| Variable | Mode | Required | Description |
|---|---|:---:|---|
| `DATABASE_URL` | Controller | ✅ | PostgreSQL connection string |
| `CONTROLLER_URL` | Worker | ✅ | Controller gRPC endpoint (e.g., `host:9090`) |
| `CONTROLLER_TOKEN` | Worker | ✅ | Auth token (from controller's first boot output) |
| `OD_AUTH_JWT_SECRET` | Controller | ❌ | Auto-generated on first run, persisted in DB |
| `OD_AUTH_ADMIN_PASSWORD` | Controller | ❌ | Auto-generated on first run, printed to stdout |
| `OD_NODE_WORKER_AUTH_TOKEN` | Controller | ❌ | Auto-generated on first run, persisted in DB |
| `OD_NODE_ID` | Both | ❌ | Defaults to hostname |
| `OD_ENGINES_ARIA2_RPC_SECRET` | Both | ❌ | Defaults to empty |
| `OD_SERVER_PORT` | Controller | ❌ | Defaults to `8080` |
| `OD_LOGGING_LEVEL` | Both | ❌ | Defaults to `info` |
| `OD_CONFIG_PATH` | Both | ❌ | Override TOML config file path |

**Minimum to start:** Controller needs 1 env var (`DATABASE_URL`). Worker needs 2 (`CONTROLLER_URL` + `CONTROLLER_TOKEN`). Everything else auto-generates or defaults.

---

*OpenDebrid is designed to be the open-source debrid service the community deserves — extensible, self-hosted, and built to scale.*
