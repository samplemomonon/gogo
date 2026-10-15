// Package grpcsrv exposes the service layer over gRPC for internal callers. It
// is a thin adapter: it converts protobuf messages to and from the domain types
// and maps domain errors to gRPC status codes. No business rules live here.
package grpcsrv

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"media-service/gen/mediapb"
	"media-service/internal/apperror"
	"media-service/internal/model"
)

// MediaService is the behaviour this server needs from the service layer.
type MediaService interface {
	Register(ctx context.Context, req *model.CreateMediaRequest) (*model.Media, *model.PresignedUpload, error)
	GetByID(ctx context.Context, id int64) (*model.Media, error)
	List(ctx context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error)
	GetUploadURL(ctx context.Context, id int64) (*model.PresignedUpload, error)
	CompleteUpload(ctx context.Context, id int64) (*model.Media, error)
	GetDownloadURL(ctx context.Context, id int64, ttl time.Duration) (*model.DownloadURL, error)
	BatchGetDownloadURLs(ctx context.Context, ids []int64, ttl time.Duration) (map[int64]string, error)
	Delete(ctx context.Context, id int64) error
}

// Server adapts MediaService to the generated MediaServiceServer interface.
type Server struct {
	mediapb.UnimplementedMediaServiceServer

	svc MediaService
	log *slog.Logger
}

// NewServer returns a gRPC server backed by svc.
func NewServer(svc MediaService, log *slog.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// Register implements mediapb.MediaServiceServer.
func (s *Server) Register(ctx context.Context, req *mediapb.RegisterMediaRequest) (*mediapb.RegisterMediaResponse, error) {
	kind, err := model.ParseKind(kindToString(req.GetKind()))
	if err != nil {
		return nil, toStatus(err)
	}

	media, upload, err := s.svc.Register(ctx, &model.CreateMediaRequest{
		OwnerID:     req.GetOwnerId(),
		Filename:    req.GetFilename(),
		ContentType: req.GetContentType(),
		Kind:        kind,
		SizeBytes:   req.GetSizeBytes(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.RegisterMediaResponse{
		Media:  mediaToProto(media),
		Upload: uploadToProto(upload),
	}, nil
}

// GetMedia implements mediapb.MediaServiceServer.
func (s *Server) GetMedia(ctx context.Context, req *mediapb.GetMediaRequest) (*mediapb.GetMediaResponse, error) {
	media, err := s.svc.GetByID(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.GetMediaResponse{Media: mediaToProto(media)}, nil
}

// ListMedia implements mediapb.MediaServiceServer.
func (s *Server) ListMedia(ctx context.Context, req *mediapb.ListMediaRequest) (*mediapb.ListMediaResponse, error) {
	var statusFilter *model.Status
	if raw := statusToString(req.GetStatus()); raw != "" {
		parsed, err := model.ParseStatus(raw)
		if err != nil {
			return nil, toStatus(err)
		}
		statusFilter = &parsed
	}

	result, err := s.svc.List(ctx, &model.ListMediaQuery{
		OwnerID: req.GetOwnerId(),
		Status:  statusFilter,
		Limit:   int(req.GetLimit()),
		Offset:  int(req.GetOffset()),
	})
	if err != nil {
		return nil, toStatus(err)
	}

	items := make([]*mediapb.Media, 0, len(result.Media))
	for _, m := range result.Media {
		items = append(items, mediaToProto(m))
	}
	return &mediapb.ListMediaResponse{Media: items, Total: result.Total}, nil
}

// GetUploadURL implements mediapb.MediaServiceServer.
func (s *Server) GetUploadURL(ctx context.Context, req *mediapb.GetUploadURLRequest) (*mediapb.GetUploadURLResponse, error) {
	upload, err := s.svc.GetUploadURL(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.GetUploadURLResponse{Upload: uploadToProto(upload)}, nil
}

// CompleteUpload implements mediapb.MediaServiceServer.
func (s *Server) CompleteUpload(ctx context.Context, req *mediapb.CompleteUploadRequest) (*mediapb.CompleteUploadResponse, error) {
	media, err := s.svc.CompleteUpload(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.CompleteUploadResponse{Media: mediaToProto(media)}, nil
}

// GetDownloadURL implements mediapb.MediaServiceServer.
func (s *Server) GetDownloadURL(ctx context.Context, req *mediapb.GetDownloadURLRequest) (*mediapb.GetDownloadURLResponse, error) {
	ttl := durationFromSeconds(req.GetPresignTtlSeconds())

	download, err := s.svc.GetDownloadURL(ctx, req.GetId(), ttl)
	if err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.GetDownloadURLResponse{
		Url:       download.URL,
		ExpiresAt: timestamppb.New(download.ExpiresAt),
	}, nil
}

// BatchGetDownloadURLs implements mediapb.MediaServiceServer.
func (s *Server) BatchGetDownloadURLs(
	ctx context.Context,
	req *mediapb.BatchGetDownloadURLsRequest,
) (*mediapb.BatchGetDownloadURLsResponse, error) {
	ttl := durationFromSeconds(req.GetPresignTtlSeconds())

	urls, err := s.svc.BatchGetDownloadURLs(ctx, req.GetIds(), ttl)
	if err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.BatchGetDownloadURLsResponse{DownloadUrls: urls}, nil
}

// DeleteMedia implements mediapb.MediaServiceServer.
func (s *Server) DeleteMedia(ctx context.Context, req *mediapb.DeleteMediaRequest) (*mediapb.DeleteMediaResponse, error) {
	if err := s.svc.Delete(ctx, req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &mediapb.DeleteMediaResponse{Deleted: true}, nil
}

// toStatus maps a domain error to a gRPC status so callers can branch on a code
// instead of parsing a message. Anything unrecognised becomes Internal, and the
// real cause is logged rather than returned, so internals never leak to a peer.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apperror.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, apperror.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, apperror.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, apperror.ErrNotReady):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, apperror.ErrStorage):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func mediaToProto(m *model.Media) *mediapb.Media {
	if m == nil {
		return nil
	}
	proto := &mediapb.Media{
		Id:          m.ID,
		OwnerId:     m.OwnerID,
		Filename:    m.Filename,
		ContentType: m.ContentType,
		SizeBytes:   m.SizeBytes,
		ObjectKey:   m.ObjectKey,
		Kind:        kindFromString(m.Kind),
		Status:      statusFromString(m.Status),
		CreatedAt:   timestamppb.New(m.CreatedAt),
		UpdatedAt:   timestamppb.New(m.UpdatedAt),
	}
	if m.ETag != nil {
		proto.Etag = *m.ETag
	}
	return proto
}

func uploadToProto(u *model.PresignedUpload) *mediapb.UploadURL {
	if u == nil {
		return nil
	}
	return &mediapb.UploadURL{
		Url:       u.URL,
		Method:    u.Method,
		Headers:   u.Headers,
		ExpiresAt: timestamppb.New(u.ExpiresAt),
	}
}

// kindToString converts the protobuf enum to the string the domain uses. The
// unspecified value becomes empty, which the domain treats as "infer it".
func kindToString(k mediapb.MediaKind) string {
	switch k {
	case mediapb.MediaKind_MEDIA_KIND_IMAGE:
		return string(model.KindImage)
	case mediapb.MediaKind_MEDIA_KIND_VIDEO:
		return string(model.KindVideo)
	case mediapb.MediaKind_MEDIA_KIND_AUDIO:
		return string(model.KindAudio)
	case mediapb.MediaKind_MEDIA_KIND_DOCUMENT:
		return string(model.KindDocument)
	case mediapb.MediaKind_MEDIA_KIND_OTHER:
		return string(model.KindOther)
	default:
		return ""
	}
}

func kindFromString(k model.Kind) mediapb.MediaKind {
	switch k {
	case model.KindImage:
		return mediapb.MediaKind_MEDIA_KIND_IMAGE
	case model.KindVideo:
		return mediapb.MediaKind_MEDIA_KIND_VIDEO
	case model.KindAudio:
		return mediapb.MediaKind_MEDIA_KIND_AUDIO
	case model.KindDocument:
		return mediapb.MediaKind_MEDIA_KIND_DOCUMENT
	default:
		return mediapb.MediaKind_MEDIA_KIND_OTHER
	}
}

func statusToString(s mediapb.MediaStatus) string {
	switch s {
	case mediapb.MediaStatus_MEDIA_STATUS_PENDING:
		return string(model.StatusPending)
	case mediapb.MediaStatus_MEDIA_STATUS_READY:
		return string(model.StatusReady)
	default:
		return ""
	}
}

func statusFromString(s model.Status) mediapb.MediaStatus {
	switch s {
	case model.StatusPending:
		return mediapb.MediaStatus_MEDIA_STATUS_PENDING
	case model.StatusReady:
		return mediapb.MediaStatus_MEDIA_STATUS_READY
	default:
		return mediapb.MediaStatus_MEDIA_STATUS_UNSPECIFIED
	}
}

// durationFromSeconds turns the wire representation of a lifetime into a
// duration. Zero means "use the configured default" and is passed through.
func durationFromSeconds(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
