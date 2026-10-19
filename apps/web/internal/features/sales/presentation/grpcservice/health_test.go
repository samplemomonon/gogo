package grpcservice

import (
	"context"
	"errors"
	"testing"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

// TestHealthCheckAndListReportThePing pins the readiness contract: a nil ping
// is serving (the in-memory store), a passing ping is serving, a failing ping
// is not, and List reports the same status under the empty service name.
func TestHealthCheckAndListReportThePing(t *testing.T) {
	t.Parallel()

	errPing := errors.New("db down")

	tests := []struct {
		name string
		ping func(context.Context) error
		want healthpb.HealthCheckResponse_ServingStatus
	}{
		{"no ping is serving", nil, healthpb.HealthCheckResponse_SERVING},
		{"a passing ping is serving", func(context.Context) error { return nil }, healthpb.HealthCheckResponse_SERVING},
		{"a failing ping is not serving", func(context.Context) error { return errPing }, healthpb.HealthCheckResponse_NOT_SERVING},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			health := newHealthServer(tt.ping)

			check, err := health.Check(context.Background(), &healthpb.HealthCheckRequest{})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if check.GetStatus() != tt.want {
				t.Errorf("Check status = %v, want %v", check.GetStatus(), tt.want)
			}

			list, err := health.List(context.Background(), &healthpb.HealthListRequest{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			statuses := list.GetStatuses()
			if len(statuses) != 1 {
				t.Fatalf("List statuses = %v, want exactly the empty service name", statuses)
			}
			entry, ok := statuses[""]
			if !ok {
				t.Fatalf("List has no entry for the empty service name: %v", statuses)
			}
			if entry.GetStatus() != tt.want {
				t.Errorf("List status = %v, want %v", entry.GetStatus(), tt.want)
			}
		})
	}
}

// canceledStream is a gRPC server stream whose context is already canceled, so
// Watch's first send succeeds and the following select observes the
// cancellation instead of waiting on the real ticker.
type canceledStream struct {
	ctx    context.Context
	status []healthpb.HealthCheckResponse_ServingStatus
}

func (s *canceledStream) SetHeader(metadata.MD) error  { return nil }
func (s *canceledStream) SendHeader(metadata.MD) error { return nil }
func (s *canceledStream) SetTrailer(metadata.MD)       {}
func (s *canceledStream) Context() context.Context     { return s.ctx }
func (s *canceledStream) SendMsg(any) error            { return nil }
func (s *canceledStream) RecvMsg(any) error            { return nil }

func (s *canceledStream) Send(resp *healthpb.HealthCheckResponse) error {
	s.status = append(s.status, resp.GetStatus())
	return nil
}

// TestHealthWatchSendsOnceThenReturnsOnCancellation pins Watch's loop contract
// under cancellation: it emits the current status once, then returns the
// stream's context error rather than blocking on the real watch interval.
func TestHealthWatchSendsOnceThenReturnsOnCancellation(t *testing.T) {
	t.Parallel()

	errPing := errors.New("db down")

	tests := []struct {
		name string
		ping func(context.Context) error
		want healthpb.HealthCheckResponse_ServingStatus
	}{
		{"serving", nil, healthpb.HealthCheckResponse_SERVING},
		{"not serving", func(context.Context) error { return errPing }, healthpb.HealthCheckResponse_NOT_SERVING},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			stream := &canceledStream{ctx: ctx}
			err := newHealthServer(tt.ping).Watch(&healthpb.HealthCheckRequest{}, stream)

			if !errors.Is(err, context.Canceled) {
				t.Errorf("Watch = %v, want context.Canceled", err)
			}
			if len(stream.status) != 1 {
				t.Fatalf("statuses sent = %v, want exactly one before cancellation", stream.status)
			}
			if stream.status[0] != tt.want {
				t.Errorf("first status = %v, want %v", stream.status[0], tt.want)
			}
		})
	}
}
