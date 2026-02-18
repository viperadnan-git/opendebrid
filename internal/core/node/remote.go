package node

import (
	"context"
	"net/url"
	"sync"

	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// RemoteNodeClient implements NodeClient by wrapping gRPC calls to a worker's endpoint.
type RemoteNodeClient struct {
	nodeID   string
	endpoint string
	conn     *grpc.ClientConn
	client   pb.NodeServiceClient
	mu       sync.Mutex
	healthy  bool
}

func NewRemoteNodeClient(nodeID, endpoint string) *RemoteNodeClient {
	return &RemoteNodeClient{
		nodeID:   nodeID,
		endpoint: endpoint,
		healthy:  true,
	}
}

func (c *RemoteNodeClient) NodeID() string { return c.nodeID }

func (c *RemoteNodeClient) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	// Strip scheme if present (endpoint may be "http://host:port")
	target := c.endpoint
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		target = u.Host
	}
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                15 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return err
	}
	c.conn = conn
	c.client = pb.NewNodeServiceClient(conn)
	return nil
}

func (c *RemoteNodeClient) DispatchJob(ctx context.Context, req DispatchRequest) (DispatchResponse, error) {
	if err := c.connect(); err != nil {
		return DispatchResponse{Error: err.Error()}, err
	}
	resp, err := c.client.DispatchJob(ctx, &pb.DispatchJobRequest{
		JobId:      req.JobID,
		Engine:     req.Engine,
		Url:        req.URL,
		CacheKey:   req.CacheKey,
		StorageKey: req.StorageKey,
		Options:    req.Options,
	})
	if err != nil {
		c.healthy = false
		return DispatchResponse{Error: err.Error()}, err
	}
	return DispatchResponse{
		Accepted:    resp.Accepted,
		EngineJobID: resp.EngineJobId,
		Error:       resp.Error,
	}, nil
}

func (c *RemoteNodeClient) BatchGetJobStatus(ctx context.Context, reqs []BatchStatusRequest) (map[string]engine.JobStatus, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}

	pbJobs := make([]*pb.JobRef, len(reqs))
	for i, r := range reqs {
		pbJobs[i] = &pb.JobRef{
			JobId:       r.JobID,
			Engine:      r.Engine,
			EngineJobId: r.EngineJobID,
		}
	}

	resp, err := c.client.BatchGetJobStatus(ctx, &pb.BatchJobStatusRequest{Jobs: pbJobs})
	if err != nil {
		return nil, err
	}

	result := make(map[string]engine.JobStatus, len(resp.Statuses))
	for jobID, s := range resp.Statuses {
		result[jobID] = engine.JobStatus{
			EngineJobID:    s.EngineJobId,
			Name:           s.Name,
			State:          engine.JobState(s.Status),
			EngineState:    s.EngineState,
			Progress:       s.Progress,
			Speed:          s.Speed,
			TotalSize:      s.TotalSize,
			DownloadedSize: s.DownloadedSize,
			Error:          s.Error,
		}
	}
	return result, nil
}

func (c *RemoteNodeClient) GetJobFiles(ctx context.Context, engineName, jobID, engineJobID string) ([]engine.FileInfo, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	resp, err := c.client.GetJobFiles(ctx, &pb.JobFilesRequest{
		JobId:       jobID,
		EngineJobId: engineJobID,
		Engine:      engineName,
		StorageKey:  jobID,
	})
	if err != nil {
		return nil, err
	}
	files := make([]engine.FileInfo, len(resp.Files))
	for i, f := range resp.Files {
		files[i] = engine.FileInfo{
			Path:        f.Path,
			Size:        f.Size,
			StorageURI:  f.StorageUri,
			ContentType: f.ContentType,
		}
	}
	return files, nil
}

func (c *RemoteNodeClient) CancelJob(ctx context.Context, engineName, jobID, engineJobID string) error {
	if err := c.connect(); err != nil {
		return err
	}
	_, err := c.client.CancelJob(ctx, &pb.CancelJobRequest{
		JobId:       jobID,
		EngineJobId: engineJobID,
		Engine:      engineName,
	})
	return err
}

func (c *RemoteNodeClient) RemoveJob(ctx context.Context, engineName, jobID, engineJobID string) error {
	if err := c.connect(); err != nil {
		return err
	}
	_, err := c.client.RemoveJob(ctx, &pb.RemoveJobRequest{
		JobId:       jobID,
		EngineJobId: engineJobID,
		Engine:      engineName,
		StorageKey:  jobID,
	})
	return err
}

func (c *RemoteNodeClient) Healthy() bool { return c.healthy }

func (c *RemoteNodeClient) SetHealthy(h bool) {
	c.mu.Lock()
	c.healthy = h
	c.mu.Unlock()
}

// Close shuts down the underlying gRPC connection.
func (c *RemoteNodeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
