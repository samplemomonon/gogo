package grpcservice

import (
	"context"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// healthServer reports sales readiness through the standard gRPC health contract.
type healthServer struct {
	ping func(context.Context) error
}

func newHealthServer(ping func(context.Context) error) *healthServer {
	return &healthServer{ping: ping}
}

func (h *healthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: h.status(ctx)}, nil
}

func (h *healthServer) List(ctx context.Context, _ *healthpb.HealthListRequest) (*healthpb.HealthListResponse, error) {
	return &healthpb.HealthListResponse{
		Statuses: map[string]*healthpb.HealthCheckResponse{
			"": {Status: h.status(ctx)},
		},
	}, nil
}

func (h *healthServer) Watch(_ *healthpb.HealthCheckRequest, stream grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	for {
		if err := stream.Send(&healthpb.HealthCheckResponse{Status: h.status(stream.Context())}); err != nil {
			return err
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-ticker.C:
		}
	}
}

const watchInterval = 5 * time.Second

func (h *healthServer) status(ctx context.Context) healthpb.HealthCheckResponse_ServingStatus {
	if h.ping != nil {
		if err := h.ping(ctx); err != nil {
			return healthpb.HealthCheckResponse_NOT_SERVING
		}
	}
	return healthpb.HealthCheckResponse_SERVING
}
