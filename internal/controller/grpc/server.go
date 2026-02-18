package grpc

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	dbgen "github.com/viperadnan-git/opendebrid/internal/database/gen"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	nodeClients *node.NodeClientStore
	grpcServer  *grpc.Server
}

// NewServer creates a new controller gRPC server with the NodeService registered.
func NewServer(
	db *pgxpool.Pool,
	bus event.Bus,
	registry *engine.Registry,
	workerToken string,
	nodeClients *node.NodeClientStore,
) *Server {
	s := &Server{
		db:          db,
		queries:     dbgen.New(db),
		bus:         bus,
		registry:    registry,
		workerToken: workerToken,
		nodeClients: nodeClients,
	}
	s.grpcServer = grpc.NewServer(
		grpc.UnaryInterceptor(s.authUnaryInterceptor),
		grpc.StreamInterceptor(s.authStreamInterceptor),
	)
	pb.RegisterNodeServiceServer(s.grpcServer, s)
	return s
}

func (s *Server) authenticate(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	tokens := md.Get("authorization")
	if len(tokens) == 0 || tokens[0] != s.workerToken {
		return status.Error(codes.Unauthenticated, "invalid auth token")
	}
	return nil
}

func (s *Server) authUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *Server) authStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := s.authenticate(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// GRPCServer returns the underlying grpc.Server for use with h2c multiplexing.
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpcServer
}

// Stop stops the gRPC server. We use Stop() instead of GracefulStop()
// because when served via h2c (ServeHTTP), the serverHandlerTransport
// does not implement Drain() and GracefulStop() would panic.
// Graceful connection draining is handled by the HTTP server's Shutdown().
func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}
}

// AddNodeClient registers a node client (thread-safe).
func (s *Server) AddNodeClient(id string, client node.NodeClient) {
	s.nodeClients.Set(id, client)
}

// RemoveNodeClient removes a node client (thread-safe).
func (s *Server) RemoveNodeClient(id string) {
	s.nodeClients.Delete(id)
}

// GetNodeClient returns a node client by ID (thread-safe).
func (s *Server) GetNodeClient(id string) (node.NodeClient, bool) {
	return s.nodeClients.Get(id)
}
