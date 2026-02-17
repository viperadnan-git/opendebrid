package worker

import (
	"context"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"github.com/rs/zerolog/log"
)

type workerGRPCServer struct {
	pb.UnimplementedNodeServiceServer
	registry *engine.Registry
	bus      event.Bus
}

func newWorkerGRPCServer(registry *engine.Registry, bus event.Bus) *workerGRPCServer {
	return &workerGRPCServer{
		registry: registry,
		bus:      bus,
	}
}

func (s *workerGRPCServer) DispatchJob(ctx context.Context, req *pb.DispatchJobRequest) (*pb.DispatchJobResponse, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return &pb.DispatchJobResponse{
			Accepted: false,
			Error:    err.Error(),
		}, nil
	}

	resp, err := eng.Add(ctx, engine.AddRequest{
		JobID:   req.JobId,
		URL:     req.Url,
		Options: req.Options,
	})
	if err != nil {
		return &pb.DispatchJobResponse{
			Accepted: false,
			Error:    err.Error(),
		}, nil
	}

	log.Info().Str("job_id", req.JobId).Str("engine", req.Engine).Msg("job dispatched to worker")

	return &pb.DispatchJobResponse{
		Accepted:    true,
		EngineJobId: resp.EngineJobID,
	}, nil
}

func (s *workerGRPCServer) BatchGetJobStatus(ctx context.Context, req *pb.BatchJobStatusRequest) (*pb.BatchJobStatusResponse, error) {
	// Group requests by engine
	type engineGroup struct {
		engineJobIDs []string
		jobIDs       []string
	}
	groups := make(map[string]*engineGroup)
	for _, j := range req.Jobs {
		g, ok := groups[j.Engine]
		if !ok {
			g = &engineGroup{}
			groups[j.Engine] = g
		}
		g.engineJobIDs = append(g.engineJobIDs, j.EngineJobId)
		g.jobIDs = append(g.jobIDs, j.JobId)
	}

	pbStatuses := make(map[string]*pb.JobStatusReport)
	for engineName, g := range groups {
		eng, err := s.registry.Get(engineName)
		if err != nil {
			continue
		}
		statuses, err := eng.BatchStatus(ctx, g.engineJobIDs)
		if err != nil {
			continue
		}
		for i, engineJobID := range g.engineJobIDs {
			if status, ok := statuses[engineJobID]; ok {
				pbStatuses[g.jobIDs[i]] = &pb.JobStatusReport{
					JobId:          g.jobIDs[i],
					EngineJobId:    status.EngineJobID,
					Name:           status.Name,
					Status:         string(status.State),
					EngineState:    status.EngineState,
					Progress:       status.Progress,
					Speed:          status.Speed,
					TotalSize:      status.TotalSize,
					DownloadedSize: status.DownloadedSize,
					Error:          status.Error,
				}
			}
		}
	}

	return &pb.BatchJobStatusResponse{Statuses: pbStatuses}, nil
}

func (s *workerGRPCServer) GetJobFiles(ctx context.Context, req *pb.JobFilesRequest) (*pb.JobFilesResponse, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return nil, err
	}

	files, err := eng.ListFiles(ctx, req.JobId, req.EngineJobId)
	if err != nil {
		return nil, err
	}

	pbFiles := make([]*pb.FileEntry, len(files))
	for i, f := range files {
		pbFiles[i] = &pb.FileEntry{
			Path:        f.Path,
			Size:        f.Size,
			StorageUri:  f.StorageURI,
			ContentType: f.ContentType,
		}
	}

	return &pb.JobFilesResponse{Files: pbFiles}, nil
}

func (s *workerGRPCServer) CancelJob(ctx context.Context, req *pb.CancelJobRequest) (*pb.Ack, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}

	if err := eng.Cancel(ctx, req.EngineJobId); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}

	return &pb.Ack{Ok: true}, nil
}

func (s *workerGRPCServer) RemoveJob(ctx context.Context, req *pb.RemoveJobRequest) (*pb.Ack, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}

	if err := eng.Remove(ctx, req.JobId, req.EngineJobId); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}

	return &pb.Ack{Ok: true}, nil
}

func (s *workerGRPCServer) ResolveCacheKey(ctx context.Context, req *pb.CacheKeyRequest) (*pb.CacheKeyResponse, error) {
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
