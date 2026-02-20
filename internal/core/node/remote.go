package node

import (
	"context"
	"crypto/tls"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
}

func NewRemoteNodeClient(nodeID, endpoint string) *RemoteNodeClient {
	return &RemoteNodeClient{
		nodeID:   nodeID,
		endpoint: endpoint,
	}
}

func (c *RemoteNodeClient) NodeID() string { return c.nodeID }

func (c *RemoteNodeClient) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	// Parse endpoint to extract host and detect TLS.
	target := c.endpoint
	useTLS := strings.HasPrefix(target, "https://")
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		target = u.Host
		// gRPC needs an explicit port; default to 443 for HTTPS.
		if useTLS && u.Port() == "" {
			target = u.Host + ":443"
		}
	}

	var creds grpc.DialOption
	if useTLS {
		creds = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{}))
	} else {
		creds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(target,
		creds,
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
		StorageKey: req.StorageKey,
		Options:    req.Options,
	})
	if err != nil {
		return DispatchResponse{Error: err.Error()}, err
	}
	return DispatchResponse{
		Accepted:     resp.Accepted,
		EngineJobID:  resp.EngineJobId,
		FileLocation: resp.FileLocation,
		Error:        resp.Error,
	}, nil
}

func (c *RemoteNodeClient) GetJobFiles(ctx context.Context, ref JobRef) ([]engine.FileInfo, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}
	resp, err := c.client.GetJobFiles(ctx, &pb.JobFilesRequest{
		JobId:       ref.JobID,
		EngineJobId: ref.EngineJobID,
		Engine:      ref.Engine,
		StorageKey:  ref.StorageKey,
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

func (c *RemoteNodeClient) CancelJob(ctx context.Context, ref JobRef) error {
	if err := c.connect(); err != nil {
		return err
	}
	_, err := c.client.CancelJob(ctx, &pb.CancelJobRequest{
		JobId:       ref.JobID,
		EngineJobId: ref.EngineJobID,
		Engine:      ref.Engine,
	})
	return err
}

func (c *RemoteNodeClient) RemoveJob(ctx context.Context, ref JobRef) error {
	if err := c.connect(); err != nil {
		return err
	}
	_, err := c.client.RemoveJob(ctx, &pb.RemoveJobRequest{
		EngineJobId: ref.EngineJobID,
		Engine:      ref.Engine,
		StorageKey:  ref.StorageKey,
	})
	return err
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
