// Package grpc provides gRPC client adapters for external services.
package grpc

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"product-service/internal/domain/product"
)

// wrapDependency turns a gRPC failure into a domain error.
func wrapDependency(op string, err error) error {
	code := status.Code(err)
	switch code {
	case codes.NotFound:
		return product.NewValidationError("reference", product.ErrInvalidInput)
	case codes.InvalidArgument, codes.FailedPrecondition, codes.AlreadyExists:
		return product.NewValidationError("reference", product.ErrInvalidInput)
	default:
		return product.NewValidationError("reference", product.ErrUnavailable)
	}
}