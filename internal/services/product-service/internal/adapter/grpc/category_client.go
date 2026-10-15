// Package grpc provides gRPC client adapters for external services.
package grpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"product-service/gen/categorypb"
	"product-service/internal/ports"
)

// CategoryClientConfig holds configuration for the category gRPC client.
type CategoryClientConfig struct {
	Target  string
	Timeout time.Duration
}

// CategoryClient implements ports.CategoryLookup using gRPC.
type CategoryClient struct {
	conn    *grpc.ClientConn
	client  categorypb.CategoryServiceClient
	timeout time.Duration
}

// NewCategoryClient creates a new category gRPC client.
func NewCategoryClient(cfg CategoryClientConfig) (*CategoryClient, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}

	conn, err := grpc.NewClient(
		cfg.Target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return nil, fmt.Errorf("dial category service: %w", err)
	}

	return &CategoryClient{
		conn:    conn,
		client:  categorypb.NewCategoryServiceClient(conn),
		timeout: cfg.Timeout,
	}, nil
}

// Close closes the gRPC connection.
func (c *CategoryClient) Close() error {
	return c.conn.Close()
}

// Exists checks which category IDs exist.
func (c *CategoryClient) Exists(ctx context.Context, ids []int64) (map[int64]bool, error) {
	if len(ids) == 0 {
		return map[int64]bool{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.CategoryExists(ctx, &categorypb.CategoryExistsRequest{Ids: ids})
	if err != nil {
		return nil, wrapDependency("category-service CategoryExists", err)
	}

	exists := make(map[int64]bool, len(resp.GetExists()))
	for _, id := range resp.GetExists() {
		exists[id] = true
	}
	return exists, nil
}

// Compile-time interface check
var _ ports.CategoryLookup = (*CategoryClient)(nil)
