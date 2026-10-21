package mediaconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	mediav1 "gogo/contracts/media/v1"
	"gogo/internal/shared/breaker"
)

// fakeMediaServer implements just enough of the media contract to drive the
// client. Only the method a test exercises returns a real response; the rest
// fail loudly so a test cannot accidentally pass by calling the wrong method.
type fakeMediaServer struct {
	mediav1.UnimplementedMediaServiceServer

	registerFn func(context.Context, *mediav1.RegisterMediaRequest) (*mediav1.RegisterMediaResponse, error)
	batchFn    func(context.Context, *mediav1.BatchGetDownloadURLsRequest) (*mediav1.BatchGetDownloadURLsResponse, error)
	urlFn      func(context.Context, *mediav1.GetDownloadURLRequest) (*mediav1.GetDownloadURLResponse, error)
	getFn      func(context.Context, *mediav1.GetMediaRequest) (*mediav1.GetMediaResponse, error)
	completeFn func(context.Context, *mediav1.CompleteUploadRequest) (*mediav1.CompleteUploadResponse, error)
	deleteFn   func(context.Context, *mediav1.DeleteMediaRequest) (*mediav1.DeleteMediaResponse, error)

	lastRegister *mediav1.RegisterMediaRequest
	lastBatch    *mediav1.BatchGetDownloadURLsRequest
	lastDelete   *mediav1.DeleteMediaRequest
	batchCalls   counter
}

func (f *fakeMediaServer) RegisterMedia(ctx context.Context, req *mediav1.RegisterMediaRequest) (*mediav1.RegisterMediaResponse, error) {
	f.lastRegister = req
	if f.registerFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected RegisterMedia")
	}
	return f.registerFn(ctx, req)
}

func (f *fakeMediaServer) BatchGetDownloadURLs(ctx context.Context, req *mediav1.BatchGetDownloadURLsRequest) (*mediav1.BatchGetDownloadURLsResponse, error) {
	f.lastBatch = req
	f.batchCalls.inc()
	if f.batchFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected BatchGetDownloadURLs")
	}
	return f.batchFn(ctx, req)
}

func (f *fakeMediaServer) GetDownloadURL(ctx context.Context, req *mediav1.GetDownloadURLRequest) (*mediav1.GetDownloadURLResponse, error) {
	if f.urlFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected GetDownloadURL")
	}
	return f.urlFn(ctx, req)
}

func (f *fakeMediaServer) GetMedia(ctx context.Context, req *mediav1.GetMediaRequest) (*mediav1.GetMediaResponse, error) {
	if f.getFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected GetMedia")
	}
	return f.getFn(ctx, req)
}

func (f *fakeMediaServer) CompleteUpload(ctx context.Context, req *mediav1.CompleteUploadRequest) (*mediav1.CompleteUploadResponse, error) {
	if f.completeFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected CompleteUpload")
	}
	return f.completeFn(ctx, req)
}

func (f *fakeMediaServer) DeleteMedia(ctx context.Context, req *mediav1.DeleteMediaRequest) (*mediav1.DeleteMediaResponse, error) {
	f.lastDelete = req
	if f.deleteFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected DeleteMedia")
	}
	return f.deleteFn(ctx, req)
}

func readyMedia(id int64, status mediav1.MediaStatus) *mediav1.Media {
	return &mediav1.Media{Id: id, Kind: mediav1.MediaKind_MEDIA_KIND_IMAGE, Status: status}
}

// ---- reads -----------------------------------------------------------------

func TestBatchDownloadURLsMapsWireToStringKeys(t *testing.T) {
	fake := &fakeMediaServer{batchFn: func(_ context.Context, _ *mediav1.BatchGetDownloadURLsRequest) (*mediav1.BatchGetDownloadURLsResponse, error) {
		return &mediav1.BatchGetDownloadURLsResponse{DownloadUrls: map[int64]string{
			9: "https://minio.local/gogo/9/hero.jpg?X-Amz-Signature=abc",
		}}, nil
	}}
	client, _ := newClient(t, fake)

	urls, err := client.BatchDownloadURLs(context.Background(), []string{"1", "9", "unset"})
	if err != nil {
		t.Fatalf("BatchDownloadURLs: %v", err)
	}

	// The int64 → decimal-string conversion is the load-bearing part of this
	// test: gogo's own modules carry media ids as text next to product ids, so a
	// caller must never see an int64 key.
	if got := urls["9"]; got == "" {
		t.Error("no URL for media id \"9\"")
	}
	// Ids without a READY object are absent rather than mapped to an empty
	// string, so callers cannot confuse "no image" with "empty URL".
	if _, ok := urls["1"]; ok {
		t.Error("id \"1\" appeared in the map; want it absent (no READY object)")
	}
	if _, ok := urls["unset"]; ok {
		t.Error("malformed id \"unset\" appeared in the map; want it absent")
	}
	// Malformed ids degrade to 0 on the wire, not to a wrong row.
	if got := fake.lastBatch.GetIds(); len(got) != 3 || got[0] != 1 || got[1] != 9 || got[2] != 0 {
		t.Errorf("wire ids = %v, want [1 9 0]", got)
	}
}

// An empty request must not dial the upstream at all: a page with no products
// needs no image resolution.
func TestBatchDownloadURLsShortCircuitsEmpty(t *testing.T) {
	fake := &fakeMediaServer{batchFn: func(_ context.Context, _ *mediav1.BatchGetDownloadURLsRequest) (*mediav1.BatchGetDownloadURLsResponse, error) {
		t.Error("media service was called with an empty id batch")
		return &mediav1.BatchGetDownloadURLsResponse{}, nil
	}}
	client, fake := newClient(t, fake)

	urls, err := client.BatchDownloadURLs(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchDownloadURLs: %v", err)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, want empty", urls)
	}
	if got := fake.batchCalls.load(); got != 0 {
		t.Errorf("upstream was called %d times; want 0", got)
	}
}

func TestGetDownloadURLNotFoundMapsSentinel(t *testing.T) {
	fake := &fakeMediaServer{urlFn: func(_ context.Context, _ *mediav1.GetDownloadURLRequest) (*mediav1.GetDownloadURLResponse, error) {
		return nil, status.Error(codes.NotFound, "no such object")
	}}
	client, _ := newClient(t, fake)

	_, err := client.GetDownloadURL(context.Background(), "99")
	if !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("err = %v, want ErrMediaNotFound", err)
	}
}

func TestGetNormalizesEnums(t *testing.T) {
	created := timestamppb.New(time.Unix(1700000000, 0))
	fake := &fakeMediaServer{getFn: func(_ context.Context, _ *mediav1.GetMediaRequest) (*mediav1.GetMediaResponse, error) {
		return &mediav1.GetMediaResponse{Media: &mediav1.Media{
			Id: 12, OwnerId: "merchant-1", Filename: "hero.jpg", ContentType: "image/jpeg",
			SizeBytes: 2048, Kind: mediav1.MediaKind_MEDIA_KIND_IMAGE,
			Status: mediav1.MediaStatus_MEDIA_STATUS_READY, CreatedAt: created,
		}}, nil
	}}
	client, _ := newClient(t, fake)

	got, err := client.Get(context.Background(), "12")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "12" || got.OwnerID != "merchant-1" || got.Kind != KindImage || got.Status != StatusReady {
		t.Errorf("Get = %+v, want id 12, image, ready", got)
	}
	if !got.CreatedAt.Equal(created.AsTime()) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, created.AsTime())
	}
}

// ---- writes ----------------------------------------------------------------

func TestRegisterMapsInputToWireAndBack(t *testing.T) {
	fake := &fakeMediaServer{registerFn: func(_ context.Context, _ *mediav1.RegisterMediaRequest) (*mediav1.RegisterMediaResponse, error) {
		return &mediav1.RegisterMediaResponse{
			Media:  readyMedia(7, mediav1.MediaStatus_MEDIA_STATUS_PENDING),
			Upload: &mediav1.UploadURL{Url: "https://minio.local/gogo/7", Method: "PUT", Headers: map[string]string{"Content-Type": "image/jpeg"}},
		}, nil
	}}
	client, fake := newClient(t, fake)

	media, upload, err := client.Register(context.Background(), RegisterInput{
		OwnerID: "merchant-1", Filename: "hero.jpg", ContentType: "image/jpeg",
		Kind: KindImage, SizeBytes: 2048,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if media.ID != "7" || media.Status != StatusPending {
		t.Errorf("media = %+v, want pending id 7", media)
	}
	if upload.Method != "PUT" || upload.Headers["Content-Type"] != "image/jpeg" {
		t.Errorf("upload = %+v, want PUT with a Content-Type header", upload)
	}
	req := fake.lastRegister
	if req.GetOwnerId() != "merchant-1" || req.GetFilename() != "hero.jpg" {
		t.Errorf("wire owner/filename = %q/%q", req.GetOwnerId(), req.GetFilename())
	}
	if req.GetKind() != mediav1.MediaKind_MEDIA_KIND_IMAGE {
		t.Errorf("wire kind = %v, want IMAGE", req.GetKind())
	}
}

// The storefront degrades rather than fails when media-service is down: this is
// the one sentinel a surface can branch on, and it must survive wrapping.
func TestTransportFailureOpensBreakerAndSurfacesOutage(t *testing.T) {
	fake := &fakeMediaServer{batchFn: func(_ context.Context, _ *mediav1.BatchGetDownloadURLsRequest) (*mediav1.BatchGetDownloadURLsResponse, error) {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}}
	client, fake := newClient(t, fake)

	// Drive it past the breaker's failure threshold.
	var lastErr error
	for range 10 {
		_, lastErr = client.BatchDownloadURLs(context.Background(), []string{"1"})
	}
	if !errors.Is(lastErr, ErrMediaUnavailable) {
		t.Fatalf("err = %v, want ErrMediaUnavailable", lastErr)
	}
	if got := client.Breaker().State(); got != breaker.StateOpen {
		t.Errorf("breaker = %v, want open after repeated transport failures", got)
	}

	// Once open the call is not made at all, and the sentinel still survives.
	before := fake.batchCalls.load()
	_, err := client.BatchDownloadURLs(context.Background(), []string{"1"})
	if !errors.Is(err, ErrMediaUnavailable) {
		t.Errorf("err on open circuit = %v, want ErrMediaUnavailable", err)
	}
	if got := fake.batchCalls.load(); got != before {
		t.Errorf("upstream was called %d more times with the circuit open; want 0", got-before)
	}
}

// A business rejection must leave the breaker closed: the upstream is working.
func TestBusinessRejectionDoesNotOpenBreaker(t *testing.T) {
	fake := &fakeMediaServer{getFn: func(_ context.Context, _ *mediav1.GetMediaRequest) (*mediav1.GetMediaResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "bad id")
	}}
	client, _ := newClient(t, fake)

	for range 5 {
		if _, err := client.Get(context.Background(), "1"); !errors.Is(err, ErrMediaInvalid) {
			t.Fatalf("err = %v, want ErrMediaInvalid", err)
		}
	}
	if got := client.Breaker().State(); got != breaker.StateClosed {
		t.Errorf("breaker = %v, want closed after repeated business rejections", got)
	}
}

func TestCompleteUploadMapsAndDeleteReports(t *testing.T) {
	fake := &fakeMediaServer{
		completeFn: func(_ context.Context, req *mediav1.CompleteUploadRequest) (*mediav1.CompleteUploadResponse, error) {
			return &mediav1.CompleteUploadResponse{Media: readyMedia(req.GetId(), mediav1.MediaStatus_MEDIA_STATUS_READY)}, nil
		},
		deleteFn: func(_ context.Context, req *mediav1.DeleteMediaRequest) (*mediav1.DeleteMediaResponse, error) {
			return &mediav1.DeleteMediaResponse{Deleted: req.GetId() == 12}, nil
		},
	}
	client, fake := newClient(t, fake)

	done, err := client.CompleteUpload(context.Background(), "7")
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if done.ID != "7" || done.Status != StatusReady {
		t.Errorf("completed = %+v, want ready id 7", done)
	}

	deleted, err := client.Delete(context.Background(), "12")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Error("deleted = false, want true")
	}
	if fake.lastDelete.GetId() != 12 {
		t.Errorf("wire delete id = %d, want 12", fake.lastDelete.GetId())
	}
}

func TestDeleteMapsNotFound(t *testing.T) {
	fake := &fakeMediaServer{deleteFn: func(_ context.Context, _ *mediav1.DeleteMediaRequest) (*mediav1.DeleteMediaResponse, error) {
		return nil, status.Error(codes.NotFound, "gone")
	}}
	client, _ := newClient(t, fake)

	if _, err := client.Delete(context.Background(), "1"); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("err = %v, want ErrMediaNotFound", err)
	}
}

func TestBreakerIsExposedForReadiness(t *testing.T) {
	client, _ := newClient(t, &fakeMediaServer{})
	if client.Breaker() == nil {
		t.Fatal("Breaker() = nil, want a breaker for readiness reporting")
	}
}
