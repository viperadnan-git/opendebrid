package node

import (
	"context"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/process"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
)

// Runner encapsulates the shared node lifecycle: daemon processes and the status loop.
// Both the controller's local node and the worker use Runner; they differ only in the
// Sink implementation (controllerLocalSink vs grpcSink).
type Runner struct {
	server   *NodeServer
	procMgr  *process.Manager
	sink     statusloop.Sink
	nodeID   string
	interval time.Duration
}

func NewRunner(
	server *NodeServer,
	procMgr *process.Manager,
	sink statusloop.Sink,
	nodeID string,
	interval time.Duration,
) *Runner {
	return &Runner{
		server:   server,
		procMgr:  procMgr,
		sink:     sink,
		nodeID:   nodeID,
		interval: interval,
	}
}

// Start launches daemon processes and the status push loop.
// procMgr.StartAll is synchronous (blocks until daemons are ready).
// The status loop and process watcher run as background goroutines tied to ctx.
// A partial daemon failure is logged but does not prevent the status loop from starting,
// since other engines (e.g. yt-dlp) may still be operational.
func (r *Runner) Start(ctx context.Context) error {
	err := r.procMgr.StartAll(ctx)
	go statusloop.Run(ctx, r.sink, r.nodeID, r.server.registry, r.server.tracker, r.interval)
	go r.procMgr.Watch(ctx)
	return err
}

// Stop gracefully shuts down daemon processes.
func (r *Runner) Stop(ctx context.Context) error {
	return r.procMgr.StopAll(ctx)
}
