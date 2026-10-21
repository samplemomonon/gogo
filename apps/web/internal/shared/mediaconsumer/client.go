// Package mediaconsumer is the shared remote-client kit for media-service: the
// platform service that owns object metadata for MinIO (an S3-compatible store)
// and hands out presigned URLs so callers never proxy file contents.
//
// media-service is the platform's one authority for media (images, video, …), and
// it is a peer of the four product services under D-018 — the storefront reads a
// product's primary_media_id, but the bytes live in MinIO behind a presigned URL
// only media-service can issue. This package owns what every caller in gogo would
// otherwise repeat:
//
//   - wrapping the generated client (gogo/contracts/media/v1),
//   - running calls under the shared convention (grpcconsumer — tenant stamp,
//     circuit breaker, transport-vs-business error classification), and
//   - translating the wire shapes into one neutral Media shape.
//
// Callers map that neutral shape into their own read model; none of them imports
// a generated contract directly. Resolution is deliberately the bridge between a
// stored primary_media_id and the image a product card actually renders: a
// product's browse/search hit carries the id, and BatchDownloadURLs turns a page
// of them into a page of presigned URLs in one round trip (the N+1 the
// aggregator's own media batching resolves).
package mediaconsumer

import (
	"context"
	"time"

	"google.golang.org/grpc"

	mediav1 "gogo/contracts/media/v1"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
)

// Upstream names the media-service connection. It exists so the composition root
// can file a breaker under one name and the gateway's metrics and readiness
// probe can report the same identity a config file names.
type Upstream string

const UpstreamMedia Upstream = "media"

// Kind is the broad category of a stored object, normalized from the contract's
// MediaKind enum.
type Kind string

const (
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindDocument Kind = "document"
	KindOther    Kind = "other"
)

// Status is the upload lifecycle state, normalized from the contract's
// MediaStatus enum: a record exists as soon as it is registered, but only
// becomes downloadable once the bytes are confirmed in the bucket.
type Status string

const (
	// StatusPending means the record exists but the object was never uploaded.
	StatusPending Status = "pending"
	// StatusReady means the object was verified in the object store.
	StatusReady Status = "ready"
)

// Media is the neutral representation of one stored object. The bytes live in
// MinIO; this is the metadata the caller needs to render an image (or to know
// there is none). The object key is deliberately absent — callers must not
// construct object keys, they use the presigned URLs media-service issues.
type Media struct {
	ID          string
	OwnerID     string
	Filename    string
	ContentType string
	SizeBytes   int64
	Kind        Kind
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UploadURL is a direct-to-object-store upload target, plus the headers the
// client must send. The signature covers the headers, so changing one
// invalidates the URL.
type UploadURL struct {
	URL       string
	Method    string
	Headers   map[string]string
	ExpiresAt time.Time
}

// RegisterInput is a create-request in gogo's own shapes. Kind and ContentType
// are both advisory; media-service infers a kind from the content type when one
// is not stated, and the size confirmed by CompleteUpload wins over this one.
type RegisterInput struct {
	OwnerID     string
	Filename    string
	ContentType string
	Kind        Kind
	SizeBytes   int64
}

// Client wraps the generated media client, its circuit breaker and the
// wire↔Media translation. It is embedded in a surface's adapter, which knows
// that surface's own read model and port interface.
type Client struct {
	client mediav1.MediaServiceClient
	caller *grpcconsumer.Caller
}

// New builds a client over a single media-service connection. Pass a connection
// built by grpcx: Dial for a deployed service, or InProcess for a same-process
// pairing. b may be nil, in which case a default breaker is used so the upstream
// is never called without one.
func New(conn grpc.ClientConnInterface, b *breaker.Breaker) *Client {
	return &Client{
		client: mediav1.NewMediaServiceClient(conn),
		caller: grpcconsumer.New(orDefault(b), vocabulary),
	}
}

func orDefault(b *breaker.Breaker) *breaker.Breaker {
	if b == nil {
		return breaker.NewDefault()
	}
	return b
}

// Breaker returns the breaker guarding the media-service upstream, so the
// gateway's metrics and readiness probe can report it.
func (c *Client) Breaker() *breaker.Breaker { return c.caller.Breaker() }

// ---- reads ----------------------------------------------------------------

// BatchDownloadURLs resolves a batch of media ids to their presigned download
// URLs, in one round trip. Ids without a READY object (never uploaded, or the
// object was removed) are absent from the returned map rather than mapped to an
// empty string — a caller must not read an absent id as "an image that is
// genuinely blank".
func (c *Client) BatchDownloadURLs(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	resp, err := grpcconsumer.Do(c.caller, ctx, "batch media download urls", func(ctx context.Context) (*mediav1.BatchGetDownloadURLsResponse, error) {
		return c.client.BatchGetDownloadURLs(ctx, &mediav1.BatchGetDownloadURLsRequest{
			Ids: numericIDs(ids),
		})
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(resp.GetDownloadUrls()))
	for id, url := range resp.GetDownloadUrls() {
		out[formatID(id)] = url
	}
	return out, nil
}

// GetDownloadURL resolves one media id to its presigned download URL. A missing
// or not-ready object is reported as ErrMediaNotFound so a caller can degrade
// (render no image) rather than treating it as a broken reference.
func (c *Client) GetDownloadURL(ctx context.Context, id string) (string, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "get media download url", func(ctx context.Context) (*mediav1.GetDownloadURLResponse, error) {
		return c.client.GetDownloadURL(ctx, &mediav1.GetDownloadURLRequest{Id: numericID(id)})
	})
	if err != nil {
		return "", err
	}
	return resp.GetUrl(), nil
}

// Get returns the metadata of one media record. It does not touch the object
// store, so it costs nothing to call for an id the caller already knows.
func (c *Client) Get(ctx context.Context, id string) (Media, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "get media", func(ctx context.Context) (*mediav1.GetMediaResponse, error) {
		return c.client.GetMedia(ctx, &mediav1.GetMediaRequest{Id: numericID(id)})
	})
	if err != nil {
		return Media{}, err
	}
	return mediaFromWire(resp.GetMedia()), nil
}

// List returns a page of an owner's media, newest first, plus the unpaged total.
func (c *Client) List(ctx context.Context, ownerID string, limit, offset int32) ([]Media, int64, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list media", func(ctx context.Context) (*mediav1.ListMediaResponse, error) {
		return c.client.ListMedia(ctx, &mediav1.ListMediaRequest{
			OwnerId: ownerID,
			Limit:   limit,
			Offset:  offset,
		})
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Media, 0, len(resp.GetMedia()))
	for _, m := range resp.GetMedia() {
		out = append(out, mediaFromWire(m))
	}
	return out, resp.GetTotal(), nil
}

// ---- writes ---------------------------------------------------------------

// Register creates a PENDING media record and returns its presigned PUT URL, so
// the client can upload the bytes directly to MinIO without proxying them
// through this process or media-service. Once the upload completes the caller
// confirms with CompleteUpload.
func (c *Client) Register(ctx context.Context, in RegisterInput) (Media, UploadURL, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "register media", func(ctx context.Context) (*mediav1.RegisterMediaResponse, error) {
		return c.client.RegisterMedia(ctx, &mediav1.RegisterMediaRequest{
			OwnerId:     in.OwnerID,
			Filename:    in.Filename,
			ContentType: in.ContentType,
			Kind:        wireKind(in.Kind),
			SizeBytes:   in.SizeBytes,
		})
	})
	if err != nil {
		return Media{}, UploadURL{}, err
	}
	return mediaFromWire(resp.GetMedia()), uploadURLFromWire(resp.GetUpload()), nil
}

// GetUploadURL issues a fresh presigned PUT URL for an unfinished upload, which
// is what a retry of a failed upload needs.
func (c *Client) GetUploadURL(ctx context.Context, id string) (UploadURL, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "get media upload url", func(ctx context.Context) (*mediav1.GetUploadURLResponse, error) {
		return c.client.GetUploadURL(ctx, &mediav1.GetUploadURLRequest{Id: numericID(id)})
	})
	if err != nil {
		return UploadURL{}, err
	}
	return uploadURLFromWire(resp.GetUpload()), nil
}

// CompleteUpload verifies the object landed in MinIO and marks the record READY.
// The returned Media is the committed row; the object is now downloadable.
func (c *Client) CompleteUpload(ctx context.Context, id string) (Media, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "complete media upload", func(ctx context.Context) (*mediav1.CompleteUploadResponse, error) {
		return c.client.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{Id: numericID(id)})
	})
	if err != nil {
		return Media{}, err
	}
	return mediaFromWire(resp.GetMedia()), nil
}

// Delete removes the object from MinIO and the metadata row. The service reports
// whether a row was actually removed, so deleting something already gone is
// reported rather than silently treated as success.
func (c *Client) Delete(ctx context.Context, id string) (bool, error) {
	return grpcconsumer.Do(c.caller, ctx, "delete media", func(ctx context.Context) (bool, error) {
		resp, err := c.client.DeleteMedia(ctx, &mediav1.DeleteMediaRequest{Id: numericID(id)})
		if err != nil {
			return false, err
		}
		return resp.GetDeleted(), nil
	})
}
