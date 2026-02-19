package worker

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
)

type workerGRPCServer struct {
	pb.UnimplementedNodeServiceServer
	registry    *engine.Registry
	bus         event.Bus
	tracker     *statusloop.Tracker
	downloadDir string
}

func newWorkerGRPCServer(registry *engine.Registry, bus event.Bus, tracker *statusloop.Tracker, downloadDir string) *workerGRPCServer {
	return &workerGRPCServer{
		registry:    registry,
		bus:         bus,
		tracker:     tracker,
		downloadDir: strings.TrimRight(downloadDir, "/"),
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
		JobID:      req.JobId,
		StorageKey: req.StorageKey,
		URL:        req.Url,
		Options:    req.Options,
	})
	if err != nil {
		return &pb.DispatchJobResponse{
			Accepted: false,
			Error:    err.Error(),
		}, nil
	}

	// Track job for status push
	s.tracker.Add(req.JobId, req.Engine, resp.EngineJobID)

	absPath := filepath.Join(eng.DownloadDir(), req.StorageKey)
	relPath := strings.TrimPrefix(absPath, s.downloadDir+"/")
	fileLocation := "file://" + relPath
	log.Info().Str("job_id", req.JobId).Str("engine", req.Engine).Msg("job dispatched to worker")

	return &pb.DispatchJobResponse{
		Accepted:     true,
		EngineJobId:  resp.EngineJobID,
		FileLocation: fileLocation,
	}, nil
}

func (s *workerGRPCServer) GetJobFiles(ctx context.Context, req *pb.JobFilesRequest) (*pb.JobFilesResponse, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return nil, err
	}

	files, err := eng.ListFiles(ctx, req.StorageKey, req.EngineJobId)
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

	s.tracker.Remove(req.JobId)

	return &pb.Ack{Ok: true}, nil
}

func (s *workerGRPCServer) RemoveJob(ctx context.Context, req *pb.RemoveJobRequest) (*pb.Ack, error) {
	eng, err := s.registry.Get(req.Engine)
	if err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}

	if err := eng.Remove(ctx, req.StorageKey, req.EngineJobId); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}

	s.tracker.Remove(req.JobId)

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
