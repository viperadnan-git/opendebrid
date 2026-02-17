package grpc

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/opendebrid/opendebrid/internal/core/event"
	"github.com/opendebrid/opendebrid/internal/core/node"
	dbgen "github.com/opendebrid/opendebrid/internal/database/gen"
	pb "github.com/opendebrid/opendebrid/internal/proto/gen"
	"github.com/rs/zerolog/log"
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

	s.bus.Publish(ctx, event.Event{
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

// ReportJobStatus handles a worker reporting a job status update.
func (s *Server) ReportJobStatus(ctx context.Context, req *pb.JobStatusReport) (*pb.Ack, error) {
	var evtType event.EventType
	switch req.Status {
	case "completed":
		evtType = event.EventJobCompleted
	case "failed":
		evtType = event.EventJobFailed
	case "cancelled":
		evtType = event.EventJobCancelled
	default:
		evtType = event.EventJobProgress
	}

	s.bus.Publish(ctx, event.Event{
		Type:      evtType,
		Timestamp: time.Now(),
		Payload: event.JobEvent{
			JobID:    req.JobId,
			NodeID:   req.NodeId,
			Status:   req.Status,
			Progress: req.Progress,
			Speed:    req.Speed,
			Error:    req.Error,
		},
	})

	return &pb.Ack{Ok: true}, nil
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

// markNodeOffline sets a node as offline in the DB and publishes an event.
func (s *Server) markNodeOffline(ctx context.Context, nodeID string) {
	if err := s.queries.SetNodeOffline(ctx, nodeID); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("failed to mark node offline")
	}

	s.bus.Publish(ctx, event.Event{
		Type:      event.EventNodeOffline,
		Timestamp: time.Now(),
		Payload:   event.NodeEvent{NodeID: nodeID},
	})

	log.Warn().Str("node_id", nodeID).Msg("node went offline")
}
