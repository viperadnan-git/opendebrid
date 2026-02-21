package grpc

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database"
	dbgen "github.com/viperadnan-git/opendebrid/internal/database/gen"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Register handles a worker node registering with the controller.
func (s *Server) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	enginesBytes, _ := json.Marshal(req.Engines)
	enginesJSON := string(enginesBytes)

	// Extract the worker's peer address for gRPC callbacks.
	var grpcEndpoint pgtype.Text
	if p, ok := peer.FromContext(ctx); ok {
		grpcEndpoint = pgtype.Text{String: p.Addr.String(), Valid: true}
	}

	// Bound DB operations during registration to avoid hanging on a slow database.
	dbCtx, dbCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dbCancel()

	_, err := s.queries.UpsertNode(dbCtx, dbgen.UpsertNodeParams{
		ID:            req.NodeId,
		GrpcEndpoint:  grpcEndpoint,
		FileEndpoint:  req.FileEndpoint,
		Engines:       enginesJSON,
		IsController:  false,
		DiskTotal:     req.DiskTotal,
		DiskAvailable: req.DiskAvailable,
	})
	if err != nil {
		log.Error().Err(err).Str("node_id", req.NodeId).Msg("failed to upsert node")
		return nil, status.Errorf(codes.Internal, "failed to register node")
	}

	// Fail any active/queued jobs left over from the previous run BEFORE making
	// the node dispatchable — prevents a race where a new dispatch lands while
	// old jobs are still marked active. Inactive jobs stay inactive until the
	// worker calls ReconcileNode with its actual disk state.
	if err := s.queries.MarkNodeActiveJobsFailed(dbCtx, req.NodeId); err != nil {
		log.Warn().Err(err).Str("node_id", req.NodeId).Msg("failed to mark stale active jobs failed on node register")
	}

	// Now make the node available for dispatching.
	remote := node.NewRemoteNodeClient(req.NodeId, req.FileEndpoint)
	s.AddNodeClient(req.NodeId, remote)

	log.Info().
		Str("node_id", req.NodeId).
		Strs("engines", req.Engines).
		Msg("worker registered")

	return &pb.RegisterResponse{
		Accepted:             true,
		Message:              "registered",
		HeartbeatIntervalSec: int32(node.HeartbeatInterval.Seconds()),
	}, nil
}

// Deregister handles a worker node gracefully deregistering from the controller.
func (s *Server) Deregister(ctx context.Context, req *pb.DeregisterRequest) (*pb.Ack, error) {
	s.markNodeOffline(ctx, req.NodeId)
	s.RemoveNodeClient(req.NodeId)

	return &pb.Ack{Ok: true, Message: "deregistered"}, nil
}

// Heartbeat handles the bidirectional heartbeat stream from a worker node.
// The reaper goroutine handles marking nodes offline if heartbeats stop.
func (s *Server) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatPing, pb.HeartbeatPong]) error {
	for {
		ping, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// If the node is heartbeating but not in the in-memory client map
		// (e.g. after controller restart), restore it from the DB record and
		// mark its inactive jobs completed — the worker is still running so
		// its files are intact.
		if _, ok := s.GetNodeClient(ping.NodeId); !ok {
			dbNode, err := s.queries.GetNode(stream.Context(), ping.NodeId)
			if err != nil {
				return status.Errorf(codes.NotFound, "node %q not found in database", ping.NodeId)
			}
			remote := node.NewRemoteNodeClient(dbNode.ID, dbNode.FileEndpoint)
			s.AddNodeClient(dbNode.ID, remote)
			if err := s.queries.RestoreNodeInactiveJobs(stream.Context(), ping.NodeId); err != nil {
				log.Warn().Err(err).Str("node_id", ping.NodeId).Msg("failed to restore inactive jobs on heartbeat reconnect")
			}
			log.Info().Str("node_id", ping.NodeId).Msg("restored node client from heartbeat")
		}

		if err := s.queries.UpdateNodeHeartbeat(stream.Context(), dbgen.UpdateNodeHeartbeatParams{
			ID:            ping.NodeId,
			DiskTotal:     ping.DiskTotal,
			DiskAvailable: ping.DiskAvailable,
		}); err != nil {
			log.Error().Err(err).Str("node_id", ping.NodeId).Msg("failed to update heartbeat")
		}

		if err := stream.Send(&pb.HeartbeatPong{Acknowledged: true}); err != nil {
			return err
		}
	}
}

// ResolveCacheKey resolves a cache key for a URL using the specified engine.
func (s *Server) ResolveCacheKey(ctx context.Context, req *pb.CacheKeyRequest) (*pb.CacheKeyResponse, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return &pb.CacheKeyResponse{Error: err.Error()}, nil
	}

	key, err := eng.ResolveCacheKey(ctx, req.Url)
	if err != nil {
		return &pb.CacheKeyResponse{Error: err.Error()}, nil
	}

	return &pb.CacheKeyResponse{
		CacheKeyType:  string(key.Type),
		CacheKeyValue: key.Value,
	}, nil
}

// ListNodeStorageKeys returns the set of valid storage keys for a node,
// so the worker can identify and remove orphaned download directories.
func (s *Server) ListNodeStorageKeys(ctx context.Context, req *pb.ListNodeStorageKeysRequest) (*pb.ListNodeStorageKeysResponse, error) {
	storageKeys, err := s.queries.ListStorageKeysByNode(ctx, req.NodeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list storage keys: %v", err)
	}
	return &pb.ListNodeStorageKeysResponse{StorageKeys: storageKeys}, nil
}

// PushJobStatuses handles batch job status updates pushed from workers.
func (s *Server) PushJobStatuses(ctx context.Context, req *pb.PushJobStatusesRequest) (*pb.Ack, error) {
	if len(req.Statuses) == 0 {
		return &pb.Ack{Ok: true}, nil
	}

	log.Debug().
		Str("node_id", req.NodeId).
		Int("count", len(req.Statuses)).
		Msg("received status push from node")

	var progressUpdates []*pb.JobStatusReport
	var terminalUpdates []*pb.JobStatusReport

	for _, st := range req.Statuses {
		switch st.Status {
		case "completed", "failed":
			terminalUpdates = append(terminalUpdates, st)
		default:
			progressUpdates = append(progressUpdates, st)
		}
	}

	// Batch update progress (single DB round-trip)
	if len(progressUpdates) > 0 {
		s.batchUpdateProgress(ctx, req.NodeId, progressUpdates)
	}

	// Batch update terminal states
	if len(terminalUpdates) > 0 {
		s.batchHandleTerminal(ctx, req.NodeId, terminalUpdates)
	}

	return &pb.Ack{Ok: true}, nil
}

// batchUpdateProgress writes progress updates to DB and publishes events.
func (s *Server) batchUpdateProgress(ctx context.Context, nodeID string, updates []*pb.JobStatusReport) {
	ids := make([]pgtype.UUID, len(updates))
	progress := make([]float64, len(updates))
	speed := make([]int64, len(updates))
	downloaded := make([]int64, len(updates))
	names := make([]string, len(updates))
	sizes := make([]int64, len(updates))
	engineJobIDs := make([]string, len(updates))

	for i, u := range updates {
		ids[i] = util.TextToUUID(u.JobId)
		progress[i] = u.Progress
		speed[i] = u.Speed
		downloaded[i] = u.DownloadedSize
		names[i] = u.Name
		sizes[i] = u.TotalSize
		engineJobIDs[i] = u.EngineJobId
	}

	if err := s.queries.BatchUpdateJobProgress(ctx, dbgen.BatchUpdateJobProgressParams{
		Column1: ids,
		Column2: progress,
		Column3: speed,
		Column4: downloaded,
		Column5: names,
		Column6: sizes,
		Column7: engineJobIDs,
	}); err != nil {
		log.Warn().Err(err).Msg("batch progress update failed")
	}
}

// batchHandleTerminal processes completed and failed job statuses in batch.
func (s *Server) batchHandleTerminal(ctx context.Context, nodeID string, updates []*pb.JobStatusReport) {
	var completed, failed []*pb.JobStatusReport
	for _, st := range updates {
		switch st.Status {
		case "completed":
			completed = append(completed, st)
		case "failed":
			failed = append(failed, st)
		}
	}

	// Batch complete
	if len(completed) > 0 {
		ids := make([]pgtype.UUID, len(completed))
		engineJobIDs := make([]string, len(completed))
		names := make([]string, len(completed))
		sizes := make([]int64, len(completed))
		for i, st := range completed {
			ids[i] = util.TextToUUID(st.JobId)
			engineJobIDs[i] = st.EngineJobId
			names[i] = st.Name
			sizes[i] = st.TotalSize
		}
		if err := s.queries.BatchCompleteJobs(ctx, dbgen.BatchCompleteJobsParams{
			Column1: ids,
			Column2: engineJobIDs,
			Column3: names,
			Column4: sizes,
		}); err != nil {
			log.Warn().Err(err).Int("count", len(completed)).Msg("batch complete jobs failed")
		}
	}

	// Batch fail + cleanup files
	if len(failed) > 0 {
		ids := make([]pgtype.UUID, len(failed))
		errors := make([]string, len(failed))
		for i, st := range failed {
			ids[i] = util.TextToUUID(st.JobId)
			errors[i] = st.Error
		}
		failedRows, err := s.queries.BatchFailJobs(ctx, dbgen.BatchFailJobsParams{
			Column1: ids,
			Column2: errors,
		})
		if err != nil {
			log.Warn().Err(err).Int("count", len(failed)).Msg("batch fail jobs failed")
		}

		// Remove files for failed jobs using the returned engine/storageKey info
		if client, ok := s.GetNodeClient(nodeID); ok {
			engineJobMap := make(map[pgtype.UUID]string, len(failed))
			for _, st := range failed {
				engineJobMap[util.TextToUUID(st.JobId)] = st.EngineJobId
			}
			for _, row := range failedRows {
				if err := client.RemoveJob(ctx, node.JobRef{
					Engine:      row.Engine,
					StorageKey:  row.StorageKey,
					EngineJobID: engineJobMap[row.ID],
				}); err != nil {
					log.Warn().Err(err).Str("job_id", util.UUIDToStr(row.ID)).Msg("failed to remove job files after failure")
				}
			}
		}
	}
}

// ResolveDownloadToken validates a download token and returns the relative file path.
// Called by workers so they can validate tokens without a direct DB connection.
func (s *Server) ResolveDownloadToken(ctx context.Context, req *pb.ResolveTokenRequest) (*pb.ResolveTokenResponse, error) {
	link, err := s.queries.GetDownloadLinkByToken(ctx, req.Token)
	if err != nil {
		return &pb.ResolveTokenResponse{Valid: false, Error: "invalid or expired token"}, nil
	}
	if req.Increment {
		go func() { _ = s.queries.IncrementLinkAccess(context.Background(), req.Token) }()
	}
	return &pb.ResolveTokenResponse{Valid: true, RelPath: link.FilePath}, nil
}

// ReconcileNode performs verified restoration of inactive jobs based on the
// storage keys actually present on the worker's filesystem. Returns the set
// of valid storage keys so the worker can clean up orphaned directories.
func (s *Server) ReconcileNode(ctx context.Context, req *pb.ReconcileNodeRequest) (*pb.ReconcileNodeResponse, error) {
	result := database.ReconcileNodeOnStartup(ctx, s.queries, req.NodeId, req.StorageKeys)
	return &pb.ReconcileNodeResponse{
		Ok:               true,
		ValidStorageKeys: result.ValidKeys,
		RestoredCount:    int32(result.RestoredCount),
		FailedCount:      int32(result.FailedCount),
	}, nil
}

func (s *Server) markNodeOffline(ctx context.Context, nodeID string) {
	database.MarkNodeOffline(ctx, s.queries, nodeID)
	log.Warn().Str("node_id", nodeID).Msg("node went offline")
}
