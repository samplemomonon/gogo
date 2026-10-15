// Package grpc provides gRPC client adapters for external services.
package grpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"product-service/gen/mediapb"
	"product-service/internal/ports"
)

// MediaClientConfig holds configuration for the media gRPC client.
type MediaClientConfig struct {
	Target  string
	Timeout time.Duration
}

// MediaClient implements ports.MediaLookup using gRPC.
type MediaClient struct {
	conn    *grpc.ClientConn
	client  mediapb.MediaServiceClient
	timeout time.Duration
}

// NewMediaClient creates a new media gRPC client.
func NewMediaClient(cfg MediaClientConfig) (*MediaClient, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}

	conn, err := grpc.NewClient(
		cfg.Target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return nil, fmt.Errorf("dial media service: %w", err)
	}

	return &MediaClient{
		conn:    conn,
		client:  mediapb.NewMediaServiceClient(conn),
		timeout: cfg.Timeout,
	}, nil
}

// Close closes the gRPC connection.
func (c *MediaClient) Close() error {
	return c.conn.Close()
}

// Exists checks which media IDs exist.
func (c *MediaClient) Exists(ctx context.Context, ids []int64) (map[int64]bool, error) {
	if len(ids) == 0 {
		return map[int64]bool{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.BatchGetDownloadURLs(ctx, &mediapb.BatchGetDownloadURLsRequest{Ids: ids})
	if err != nil {
		return nil, wrapDependency("media-service BatchGetDownloadURLs", err)
	}

	exists := make(map[int64]bool, len(resp.GetDownloadUrls()))
	for id := range resp.GetDownloadUrls() {
		exists[id] = true
	}
	return exists, nil
}

// Compile-time interface check
var _ ports.MediaLookup = (*MediaClient)(nil)
