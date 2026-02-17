package grpc

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/event"
	"github.com/opendebrid/opendebrid/internal/core/node"
	dbgen "github.com/opendebrid/opendebrid/internal/database/gen"
	pb "github.com/opendebrid/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
)

// Server is the controller's gRPC server that workers connect to.
// It handles worker registration, heartbeats, and job status reports.
type Server struct {
	pb.UnimplementedNodeServiceServer

	db          *pgxpool.Pool
	queries     *dbgen.Queries
	bus         event.Bus
	registry    *engine.Registry
	workerToken string
	nodeClients map[string]node.NodeClient
	mu          sync.RWMutex
	grpcServer  *grpc.Server
}

// NewServer creates a new controller gRPC server with the NodeService registered.
func NewServer(
	db *pgxpool.Pool,
	bus event.Bus,
	registry *engine.Registry,
	workerToken string,
	nodeClients map[string]node.NodeClient,
) *Server {
	s := &Server{
		db:          db,
		queries:     dbgen.New(db),
		bus:         bus,
		registry:    registry,
		workerToken: workerToken,
		nodeClients: nodeClients,
	}
	s.grpcServer = grpc.NewServer()
	pb.RegisterNodeServiceServer(s.grpcServer, s)
	return s
}

// GRPCServer returns the underlying grpc.Server for use with h2c multiplexing.
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpcServer
}

// Stop gracefully stops the gRPC server.
func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
}

// AddNodeClient registers a node client (thread-safe).
func (s *Server) AddNodeClient(id string, client node.NodeClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeClients[id] = client
}

// RemoveNodeClient removes a node client (thread-safe).
func (s *Server) RemoveNodeClient(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodeClients, id)
}

// GetNodeClient returns a node client by ID (thread-safe).
func (s *Server) GetNodeClient(id string) (node.NodeClient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.nodeClients[id]
	return c, ok
}
