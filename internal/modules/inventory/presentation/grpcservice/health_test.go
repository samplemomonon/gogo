package grpcservice

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// TestHealthStatusFollowsThePing pins the standard health contract's status
// rule: no ping configured (the in-memory default) is serving, a succeeding
// ping is serving, a failing ping is not serving. Check reports the overall
// status and List reports the same status under the empty service name — the
// two reads a readiness probe makes.
func TestHealthStatusFollowsThePing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ping func(context.Context) error
		want healthpb.HealthCheckResponse_ServingStatus
	}{
		{
			name: "no ping configured is serving",
			ping: nil,
			want: healthpb.HealthCheckResponse_SERVING,
		},
		{
			name: "a succeeding ping is serving",
			ping: func(context.Context) error { return nil },
			want: healthpb.HealthCheckResponse_SERVING,
		},
		{
			name: "a failing ping is not serving",
			ping: func(context.Context) error { return errors.New("driver: connection refused") },
			want: healthpb.HealthCheckResponse_NOT_SERVING,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := newHealthServer(tc.ping)

			check, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if check.GetStatus() != tc.want {
				t.Errorf("Check status = %v, want %v", check.GetStatus(), tc.want)
			}

			list, err := server.List(context.Background(), &healthpb.HealthListRequest{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			status, ok := list.GetStatuses()[""]
			if !ok {
				t.Fatal("List does not report the overall status under the empty service name")
			}
			if status.GetStatus() != tc.want {
				t.Errorf("List status = %v, want %v", status.GetStatus(), tc.want)
			}
		})
	}
}

// fakeHealthStream is the minimal server stream Watch needs: a context and a
// Send that counts calls. It exists so the Watch tests never touch a real
// clock — Watch ticks every 5 seconds in production, and a test must not.
type fakeHealthStream struct {
	grpc.ServerStream

	ctx     context.Context
	sendErr error
	sends   int
}

var _ grpc.ServerStreamingServer[healthpb.HealthCheckResponse] = (*fakeHealthStream)(nil)

func (f *fakeHealthStream) Context() context.Context { return f.ctx }

func (f *fakeHealthStream) Send(*healthpb.HealthCheckResponse) error {
	f.sends++
	return f.sendErr
}

// TestHealthWatchStopsOnACancelledContext pins Watch's exit path without
// waiting for a tick: the first status is sent immediately, then the loop
// selects on the stream context, so an already-cancelled context returns after
// exactly one send. A missing context case would hang this test rather than
// pass it.
func TestHealthWatchStopsOnACancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream := &fakeHealthStream{ctx: ctx}

	err := newHealthServer(nil).Watch(&healthpb.HealthCheckRequest{}, stream)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Watch = %v, want context.Canceled", err)
	}
	if stream.sends != 1 {
		t.Errorf("Watch sent %d statuses, want 1 before the context ended it", stream.sends)
	}
}

// TestHealthWatchReturnsSendErrors pins the other exit: once the stream can no
// longer carry the status, Watch returns the send error immediately rather
// than ticking on a dead stream.
func TestHealthWatchReturnsSendErrors(t *testing.T) {
	t.Parallel()

	sendErr := errors.New("test: stream closed")
	stream := &fakeHealthStream{ctx: context.Background(), sendErr: sendErr}

	err := newHealthServer(nil).Watch(&healthpb.HealthCheckRequest{}, stream)
	if !errors.Is(err, sendErr) {
		t.Errorf("Watch = %v, want the send error", err)
	}
	if stream.sends != 1 {
		t.Errorf("Watch sent %d statuses, want 1 before returning", stream.sends)
	}
}
