package grpc

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
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

	_, err := s.queries.UpsertNode(ctx, dbgen.UpsertNodeParams{
		ID:            req.NodeId,
		Name:          req.Name,
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

	// Create a RemoteNodeClient so the controller can dispatch jobs to this worker.
	remote := node.NewRemoteNodeClient(req.NodeId, req.FileEndpoint)
	s.AddNodeClient(req.NodeId, remote)

	_ = s.bus.Publish(ctx, event.Event{
		Type:      event.EventNodeOnline,
		Timestamp: time.Now(),
		Payload: event.NodeEvent{
			NodeID: req.NodeId,
		},
	})

	log.Info().
		Str("node_id", req.NodeId).
		Str("name", req.Name).
		Strs("engines", req.Engines).
		Msg("worker registered")

	return &pb.RegisterResponse{
		Accepted:             true,
		Message:              "registered",
		HeartbeatIntervalSec: 60,
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

		if err := s.queries.UpdateNodeHeartbeat(context.Background(), dbgen.UpdateNodeHeartbeatParams{
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
	cacheKeys, err := s.queries.ListStorageKeysByNode(ctx, req.NodeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list storage keys: %v", err)
	}

	storageKeys := make([]string, len(cacheKeys))
	for i, ck := range cacheKeys {
		storageKeys[i] = service.StorageKeyFromCacheKey(ck)
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
		case "completed", "failed", "cancelled":
			terminalUpdates = append(terminalUpdates, st)
		default:
			progressUpdates = append(progressUpdates, st)
		}
	}

	// Batch update progress (single DB round-trip)
	if len(progressUpdates) > 0 {
		s.batchUpdateProgress(ctx, req.NodeId, progressUpdates)
	}

	// Handle terminal states individually
	for _, st := range terminalUpdates {
		s.handleTerminalStatus(ctx, req.NodeId, st)
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
		ids[i] = textToUUID(u.JobId)
		progress[i] = u.Progress
		speed[i] = u.Speed
		downloaded[i] = u.DownloadedSize
		names[i] = u.Name
		sizes[i] = u.TotalSize
		engineJobIDs[i] = u.EngineJobId
	}

	if err := s.queries.BatchUpdateJobProgress(ctx, dbgen.BatchUpdateJobProgressParams{
		Ids:            ids,
		Progress:       progress,
		Speed:          speed,
		DownloadedSize: downloaded,
		Name:           names,
		Size:           sizes,
		EngineJobID:    engineJobIDs,
	}); err != nil {
		log.Warn().Err(err).Msg("batch progress update failed")
	}

	for _, u := range updates {
		_ = s.bus.Publish(ctx, event.Event{
			Type:      event.EventJobProgress,
			Timestamp: time.Now(),
			Payload: event.JobEvent{
				JobID:    u.JobId,
				NodeID:   nodeID,
				Progress: u.Progress,
				Speed:    u.Speed,
			},
		})
	}
}

// handleTerminalStatus processes completed/failed/cancelled job status.
func (s *Server) handleTerminalStatus(ctx context.Context, nodeID string, st *pb.JobStatusReport) {
	jobID := textToUUID(st.JobId)

	switch st.Status {
	case "completed":
		// file_location is already set via DispatchJobResponse at dispatch time
		if _, err := s.queries.CompleteJob(ctx, dbgen.CompleteJobParams{
			ID:      jobID,
			Column2: st.EngineJobId,
		}); err != nil {
			log.Warn().Err(err).Str("job_id", st.JobId).Msg("failed to complete job")
		}

		_ = s.bus.Publish(ctx, event.Event{
			Type:      event.EventJobCompleted,
			Timestamp: time.Now(),
			Payload: event.JobEvent{
				JobID:  st.JobId,
				NodeID: nodeID,
			},
		})

	case "failed":
		if err := s.queries.FailJob(ctx, dbgen.FailJobParams{
			ID:           jobID,
			ErrorMessage: pgtype.Text{String: st.Error, Valid: st.Error != ""},
		}); err != nil {
			log.Warn().Err(err).Str("job_id", st.JobId).Msg("failed to mark job as failed")
		}

		// Remove files on failure
		job, err := s.queries.GetJob(ctx, jobID)
		if err == nil {
			storageKey := service.StorageKeyFromCacheKey(job.CacheKey)
			if client, ok := s.GetNodeClient(nodeID); ok {
				_ = client.RemoveJob(ctx, job.Engine, storageKey, st.EngineJobId)
			}
		}

		_ = s.bus.Publish(ctx, event.Event{
			Type:      event.EventJobFailed,
			Timestamp: time.Now(),
			Payload: event.JobEvent{
				JobID:  st.JobId,
				NodeID: nodeID,
				Error:  st.Error,
			},
		})

	case "cancelled":
		if _, err := s.queries.UpdateJobStatus(ctx, dbgen.UpdateJobStatusParams{
			ID:      jobID,
			Status:  "cancelled",
			Column3: "",
			Column5: "",
		}); err != nil {
			log.Warn().Err(err).Str("job_id", st.JobId).Msg("failed to cancel job")
		}

		_ = s.bus.Publish(ctx, event.Event{
			Type:      event.EventJobCancelled,
			Timestamp: time.Now(),
			Payload:   event.JobEvent{JobID: st.JobId, NodeID: nodeID},
		})
	}
}

// textToUUID converts a UUID string to pgtype.UUID.
func textToUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	cleaned := ""
	for _, c := range s {
		if c != '-' {
			cleaned += string(c)
		}
	}
	if len(cleaned) != 32 {
		return u
	}
	for i := 0; i < 16; i++ {
		b := hexVal(cleaned[i*2])<<4 | hexVal(cleaned[i*2+1])
		u.Bytes[i] = b
	}
	u.Valid = true
	return u
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// markNodeOffline sets a node as offline in the DB and publishes an event.
func (s *Server) markNodeOffline(ctx context.Context, nodeID string) {
	if err := s.queries.SetNodeOffline(ctx, nodeID); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("failed to mark node offline")
	}

	_ = s.bus.Publish(ctx, event.Event{
		Type:      event.EventNodeOffline,
		Timestamp: time.Now(),
		Payload:   event.NodeEvent{NodeID: nodeID},
	})

	log.Warn().Str("node_id", nodeID).Msg("node went offline")
}
