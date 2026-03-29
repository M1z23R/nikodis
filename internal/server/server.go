package server

import (
	"fmt"
	"net"

	"google.golang.org/grpc"

	pb "github.com/dimitrije/nikodis/pkg/gen"

	"github.com/dimitrije/nikodis/internal/broker"
	"github.com/dimitrije/nikodis/internal/cache"
	"github.com/dimitrije/nikodis/internal/config"
)

type Server struct {
	grpcServer *grpc.Server
	listener   net.Listener
}

func NewServer(cacheStore *cache.Store, b *broker.Broker, cfg *config.Config, port int) (*Server, error) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(newAuthInterceptor(cfg)),
		grpc.ChainStreamInterceptor(newStreamAuthInterceptor(cfg)),
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterCacheServiceServer(srv, NewCacheService(cacheStore))
	pb.RegisterBrokerServiceServer(srv, NewBrokerService(b))
	return &Server{grpcServer: srv, listener: lis}, nil
}

func (s *Server) Start() error {
	return s.grpcServer.Serve(s.listener)
}

func (s *Server) GracefulStop() {
	s.grpcServer.GracefulStop()
}

func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}
