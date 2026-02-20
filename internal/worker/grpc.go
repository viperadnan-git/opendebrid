package worker

import (
	"context"

	"github.com/viperadnan-git/opendebrid/internal/core/node"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
)

type workerGRPCServer struct {
	pb.UnimplementedNodeServiceServer
	server *node.NodeServer
}

func newWorkerGRPCServer(server *node.NodeServer) *workerGRPCServer {
	return &workerGRPCServer{server: server}
}

func (s *workerGRPCServer) DispatchJob(ctx context.Context, req *pb.DispatchJobRequest) (*pb.DispatchJobResponse, error) {
	resp, err := s.server.DispatchJob(ctx, node.DispatchRequest{
		JobID:      req.JobId,
		Engine:     req.Engine,
		URL:        req.Url,
		StorageKey: req.StorageKey,
		Options:    req.Options,
	})
	if err != nil {
		return &pb.DispatchJobResponse{Accepted: false, Error: resp.Error}, nil
	}
	return &pb.DispatchJobResponse{
		Accepted:     resp.Accepted,
		EngineJobId:  resp.EngineJobID,
		FileLocation: resp.FileLocation,
		Error:        resp.Error,
	}, nil
}

func (s *workerGRPCServer) GetJobFiles(ctx context.Context, req *pb.JobFilesRequest) (*pb.JobFilesResponse, error) {
	files, err := s.server.GetJobFiles(ctx, node.JobRef{
		Engine:      req.Engine,
		StorageKey:  req.StorageKey,
		EngineJobID: req.EngineJobId,
	})
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
	if err := s.server.CancelJob(ctx, node.JobRef{
		Engine:      req.Engine,
		JobID:       req.JobId,
		EngineJobID: req.EngineJobId,
	}); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	return &pb.Ack{Ok: true}, nil
}

func (s *workerGRPCServer) RemoveJob(ctx context.Context, req *pb.RemoveJobRequest) (*pb.Ack, error) {
	if err := s.server.RemoveJob(ctx, node.JobRef{
		Engine:      req.Engine,
		StorageKey:  req.StorageKey,
		EngineJobID: req.EngineJobId,
	}); err != nil {
		return &pb.Ack{Ok: false, Message: err.Error()}, nil
	}
	return &pb.Ack{Ok: true}, nil
}

func (s *workerGRPCServer) ResolveCacheKey(ctx context.Context, req *pb.CacheKeyRequest) (*pb.CacheKeyResponse, error) {
	key, err := s.server.ResolveCacheKey(ctx, req.Engine, req.Url)
	if err != nil {
		return &pb.CacheKeyResponse{Error: err.Error()}, nil
	}
	return &pb.CacheKeyResponse{
		CacheKeyType:  string(key.Type),
		CacheKeyValue: key.Value,
	}, nil
}
