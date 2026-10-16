package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"media-service/internal/apperror"
	"media-service/internal/handler"
	"media-service/internal/model"
)

// stubService implements handler.MediaService and records what the handler
// passed down, so the wire-to-service translation is observable.
type stubService struct {
	registerResult *model.Media
	registerUpload *model.PresignedUpload
	registerErr    error
	lastCreate     *model.CreateMediaRequest

	getByIDResult *model.Media
	getByIDErr    error
	lastGetByID   int64

	listResult *model.ListMediaResult
	listErr    error
	lastQuery  *model.ListMediaQuery

	getUploadURLResult *model.PresignedUpload
	getUploadURLErr    error

	completeResult *model.Media
	completeErr    error

	getDownloadURLResult *model.DownloadURL
	getDownloadURLErr    error
	lastDownloadID       int64
	lastDownloadTTL      time.Duration

	deleteErr    error
	lastDeleteID int64
}

func (s *stubService) Register(_ context.Context, req *model.CreateMediaRequest) (*model.Media, *model.PresignedUpload, error) {
	s.lastCreate = req
	return s.registerResult, s.registerUpload, s.registerErr
}

func (s *stubService) GetByID(_ context.Context, id int64) (*model.Media, error) {
	s.lastGetByID = id
	return s.getByIDResult, s.getByIDErr
}

func (s *stubService) List(_ context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error) {
	s.lastQuery = q
	return s.listResult, s.listErr
}

func (s *stubService) GetUploadURL(_ context.Context, _ int64) (*model.PresignedUpload, error) {
	return s.getUploadURLResult, s.getUploadURLErr
}

func (s *stubService) CompleteUpload(_ context.Context, _ int64) (*model.Media, error) {
	return s.completeResult, s.completeErr
}

func (s *stubService) GetDownloadURL(_ context.Context, id int64, ttl time.Duration) (*model.DownloadURL, error) {
	s.lastDownloadID = id
	s.lastDownloadTTL = ttl
	return s.getDownloadURLResult, s.getDownloadURLErr
}

func (s *stubService) Delete(_ context.Context, id int64) error {
	s.lastDeleteID = id
	return s.deleteErr
}

// serve mounts the handler routes and dispatches one request through the real
// mux, so path values and route patterns are exercised rather than simulated.
func serve(t *testing.T, svc *stubService, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	handler.NewMediaHandler(svc, quietLogger()).RegisterRoutes(mux)

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestRegisterTranslatesTheBody pins the happy path: the request is decoded
// into the documented model, the response carries the metadata and the upload
// target, and Location names the created resource.
func TestRegisterTranslatesTheBody(t *testing.T) {
	t.Parallel()

	svc := &stubService{
		registerResult: &model.Media{ID: 7, Filename: "cover.png"},
		registerUpload: &model.PresignedUpload{URL: "https://store/upload", Method: "PUT"},
	}

	rec := serve(t, svc, http.MethodPost, "/api/v1/media",
		`{"owner_id":"owner-1","filename":"cover.png","content_type":"image/png","size_bytes":42}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/media/7" {
		t.Errorf("Location = %q, want /api/v1/media/7", got)
	}

	if svc.lastCreate == nil {
		t.Fatal("service was not called")
	}
	if svc.lastCreate.OwnerID != "owner-1" || svc.lastCreate.Filename != "cover.png" {
		t.Errorf("service received %+v", svc.lastCreate)
	}
	if svc.lastCreate.SizeBytes != 42 {
		t.Errorf("SizeBytes = %d, want 42", svc.lastCreate.SizeBytes)
	}

	var body handler.RegisterMediaResponse
	decodeInto(t, rec, &body)
	if body.Media == nil || body.Media.ID != 7 || body.Upload == nil {
		t.Errorf("body = %+v, want media 7 and an upload target", body)
	}
}

// TestRegisterRejectsBadBodies pins the 400 paths: malformed JSON and unknown
// fields are answered before the service is consulted.
func TestRegisterRejectsBadBodies(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		body string
	}{
		{"malformed", `{"owner_id":`},
		{"unknown field", `{"owner_id":"owner-1","ownerId":"typo"}`},
		{"not an object", `[1,2,3]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &stubService{}

			rec := serve(t, svc, http.MethodPost, "/api/v1/media", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if svc.lastCreate != nil {
				t.Error("service was consulted for an invalid body")
			}
		})
	}
}

// TestRegisterMapsDomainFailures pins the status mapping through the full
// stack: the service's sentinel becomes the handler's response.
func TestRegisterMapsDomainFailures(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"invalid input", apperror.ErrInvalidInput, http.StatusBadRequest},
		{"conflict", apperror.ErrConflict, http.StatusConflict},
		{"not found", apperror.ErrNotFound, http.StatusNotFound},
		{"storage", apperror.ErrStorage, http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &stubService{registerErr: tt.err}
			rec := serve(t, svc, http.MethodPost, "/api/v1/media", `{"owner_id":"o","filename":"f"}`)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// TestListForwardsValidFilters pins the query translation: owner, status,
// limit and offset reach the service, and the page metadata is echoed back.
func TestListForwardsValidFilters(t *testing.T) {
	t.Parallel()

	svc := &stubService{
		listResult: &model.ListMediaResult{
			Media: []*model.Media{{ID: 1}},
			Total: 1,
		},
	}

	rec := serve(t, svc, http.MethodGet, "/api/v1/media?owner_id=owner-1&status=ready&limit=10&offset=20", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	if svc.lastQuery == nil {
		t.Fatal("service was not called")
	}
	if svc.lastQuery.OwnerID != "owner-1" {
		t.Errorf("OwnerID = %q, want owner-1", svc.lastQuery.OwnerID)
	}
	if svc.lastQuery.Status == nil || *svc.lastQuery.Status != model.StatusReady {
		t.Errorf("Status = %v, want ready", svc.lastQuery.Status)
	}
	if svc.lastQuery.Limit != 10 || svc.lastQuery.Offset != 20 {
		t.Errorf("Limit/Offset = %d/%d, want 10/20", svc.lastQuery.Limit, svc.lastQuery.Offset)
	}

	var body handler.ListMediaResponse
	decodeInto(t, rec, &body)
	if body.Total != 1 || body.Limit != 10 || body.Offset != 20 {
		t.Errorf("body = %+v, want total 1, limit 10, offset 20", body)
	}
}

// TestListLeavesAnAbsentStatusUnset pins that "no filter" stays a nil pointer
// rather than becoming an empty status, which the repository would filter on.
func TestListLeavesAnAbsentStatusUnset(t *testing.T) {
	t.Parallel()

	svc := &stubService{listResult: &model.ListMediaResult{Media: []*model.Media{}}}

	rec := serve(t, svc, http.MethodGet, "/api/v1/media", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.lastQuery == nil {
		t.Fatal("service was not called")
	}
	if svc.lastQuery.Status != nil {
		t.Errorf("Status = %v, want nil for an absent filter", svc.lastQuery.Status)
	}
}

// TestListRejectsBadQueryValues pins the 400 paths for each filter: a value
// that is present but wrong must be answered, not silently defaulted.
func TestListRejectsBadQueryValues(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		query string
	}{
		{"unknown status", "status=live"},
		{"limit not a number", "limit=many"},
		{"negative limit", "limit=-1"},
		{"offset not a number", "offset=soon"},
		{"negative offset", "offset=-5"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &stubService{}
			rec := serve(t, svc, http.MethodGet, "/api/v1/media?"+tt.query, "")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if svc.lastQuery != nil {
				t.Error("service was consulted for an invalid query")
			}
		})
	}
}

// TestPathIDValidation pins the id contract for every by-id route: only a
// positive integer reaches the service.
func TestPathIDValidation(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/api/v1/media/abc",
		"/api/v1/media/0",
		"/api/v1/media/-3",
		"/api/v1/media/1e3",
	} {
		svc := &stubService{}
		rec := serve(t, svc, http.MethodGet, target, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, rec.Code)
		}
	}

	svc := &stubService{getByIDResult: &model.Media{ID: 12}}
	rec := serve(t, svc, http.MethodGet, "/api/v1/media/12", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/media/12 = %d, want 200", rec.Code)
	}
	if svc.lastGetByID != 12 {
		t.Errorf("service received id %d, want 12", svc.lastGetByID)
	}
}

// TestByIDRoutesReachTheRightServiceMethod pins the route table against the
// service interface: each sub-resource URL calls its own method, so a route
// wired to the wrong method would fail here rather than in production.
func TestByIDRoutesReachTheRightServiceMethod(t *testing.T) {
	t.Parallel()

	svc := &stubService{
		getUploadURLResult:   &model.PresignedUpload{URL: "https://store/put"},
		completeResult:       &model.Media{ID: 5, Status: model.StatusReady},
		getDownloadURLResult: &model.DownloadURL{URL: "https://store/get"},
	}

	if rec := serve(t, svc, http.MethodGet, "/api/v1/media/5/upload-url", ""); rec.Code != http.StatusOK {
		t.Errorf("upload-url = %d, want 200", rec.Code)
	}
	if rec := serve(t, svc, http.MethodPost, "/api/v1/media/5/complete", ""); rec.Code != http.StatusOK {
		t.Errorf("complete = %d, want 200", rec.Code)
	}
	if rec := serve(t, svc, http.MethodGet, "/api/v1/media/5/download-url", ""); rec.Code != http.StatusOK {
		t.Errorf("download-url = %d, want 200", rec.Code)
	}
	if svc.lastDownloadID != 5 {
		t.Errorf("download-url used id %d, want 5", svc.lastDownloadID)
	}
}

// TestDownloadURLTTLParsing pins the optional lifetime parameter: absent
// means zero ("use the configured default"), a number becomes seconds, and a
// present-but-invalid value is refused rather than treated as absent.
func TestDownloadURLTTLParsing(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		t.Parallel()

		svc := &stubService{getDownloadURLResult: &model.DownloadURL{}}
		rec := serve(t, svc, http.MethodGet, "/api/v1/media/5/download-url", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if svc.lastDownloadTTL != 0 {
			t.Errorf("ttl = %v, want 0 (use the default)", svc.lastDownloadTTL)
		}
	})

	t.Run("explicit", func(t *testing.T) {
		t.Parallel()

		svc := &stubService{getDownloadURLResult: &model.DownloadURL{}}
		rec := serve(t, svc, http.MethodGet, "/api/v1/media/5/download-url?presign_ttl_seconds=120", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if svc.lastDownloadTTL != 120*time.Second {
			t.Errorf("ttl = %v, want 2m", svc.lastDownloadTTL)
		}
	})

	for _, bad := range []string{"abc", "-5"} {
		svc := &stubService{}
		rec := serve(t, svc, http.MethodGet, "/api/v1/media/5/download-url?presign_ttl_seconds="+bad, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("presign_ttl_seconds=%s = %d, want 400", bad, rec.Code)
		}
	}
}

// TestDeleteAnswersWithNoContent pins the 204 contract: no body, and the
// service consulted exactly once with the parsed id.
func TestDeleteAnswersWithNoContent(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	rec := serve(t, svc, http.MethodDelete, "/api/v1/media/9", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if svc.lastDeleteID != 9 {
		t.Errorf("service received id %d, want 9", svc.lastDeleteID)
	}
}

// TestGetByIDMapsNotFound pins the not-found answer through a by-id route.
func TestGetByIDMapsNotFound(t *testing.T) {
	t.Parallel()

	svc := &stubService{getByIDErr: apperror.ErrNotFound}
	rec := serve(t, svc, http.MethodGet, "/api/v1/media/404", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "media not found") {
		t.Errorf("body = %q, want the fixed not-found message", rec.Body.String())
	}
}

// TestCompleteUploadMapsNotReady pins the lifecycle failure: completing an
// upload whose bytes never arrived is a conflict, not a server fault.
func TestCompleteUploadMapsNotReady(t *testing.T) {
	t.Parallel()

	svc := &stubService{completeErr: apperror.ErrNotReady}
	rec := serve(t, svc, http.MethodPost, "/api/v1/media/5/complete", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "media is not ready") {
		t.Errorf("body = %q, want the not-ready message", rec.Body.String())
	}
}

// TestUnknownRouteIs404 pins that the route table owns its namespace: a path
// that matches no pattern falls through to the mux's 404 rather than a
// handler's permissive match.
func TestUnknownRouteIs404(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	rec := serve(t, svc, http.MethodGet, "/api/v1/media/5/unknown-action", "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestStorageFailureIsServerFault pins the last mapping not covered above:
// the object store failing is the server's problem and is answered opaquely.
func TestStorageFailureIsServerFault(t *testing.T) {
	t.Parallel()

	svc := &stubService{deleteErr: errors.Join(apperror.ErrStorage, errors.New("minio: dial refused"))}
	rec := serve(t, svc, http.MethodDelete, "/api/v1/media/9", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "minio") {
		t.Errorf("body = %q leaked the store error", rec.Body.String())
	}
}
