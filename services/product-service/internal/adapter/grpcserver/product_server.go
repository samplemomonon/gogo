// Package grpcserver provides the gRPC server adapter for the product service.
package grpcserver

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"product-service/gen/productpb"
	productdomain "product-service/internal/domain/product"
	productusecase "product-service/internal/usecase/product"
)

// ProductServer implements the gRPC ProductServiceServer.
type ProductServer struct {
	productpb.UnimplementedProductServiceServer

	usecase productusecase.ProductUseCases
	log     *slog.Logger
}

func (s *ProductServer) ListProductTypes(context.Context, *productpb.ListProductTypesRequest) (*productpb.ListProductTypesResponse, error) {
	types := productdomain.ListProductTypes()
	response := &productpb.ListProductTypesResponse{ProductTypes: make([]*productpb.ProductType, 0, len(types))}
	for _, typ := range types {
		item := &productpb.ProductType{Id: typ.ID, Name: typ.Name, SchemaVersion: typ.SchemaVersion}
		for _, attr := range typ.Attributes {
			item.Attributes = append(item.Attributes, &productpb.ProductAttributeDefinition{
				Key: attr.Key, Label: attr.Label, Kind: attr.Kind, Required: attr.Required, Options: attr.Options, Unit: attr.Unit,
			})
		}
		response.ProductTypes = append(response.ProductTypes, item)
	}
	return response, nil
}

// NewProductServer creates a new gRPC product server.
func NewProductServer(uc productusecase.ProductUseCases, log *slog.Logger) *ProductServer {
	return &ProductServer{usecase: uc, log: log}
}

// CreateProduct implements productpb.ProductServiceServer.
func (s *ProductServer) CreateProduct(
	ctx context.Context,
	req *productpb.CreateProductRequest,
) (*productpb.CreateProductResponse, error) {
	status, err := productdomain.ParseStatus(statusToString(req.GetStatus()))
	if err != nil {
		return nil, toStatus(err)
	}

	var primaryMediaID *int64
	if id := req.GetPrimaryMediaId(); id != 0 {
		primaryMediaID = &id
	}

	variants := make([]productdomain.CreateVariantRequest, 0, len(req.GetVariants()))
	for _, v := range req.GetVariants() {
		spec := productdomain.CreateVariantRequest{
			SKU:        v.GetSku(),
			Name:       v.GetName(),
			PriceCents: v.GetPriceCents(),
			Currency:   v.GetCurrency(),
			Stock:      int64(v.GetStock()),
		}
		if id := v.GetMediaId(); id != 0 {
			spec.MediaID = &id
		}
		variants = append(variants, spec)
	}

	p, err := s.usecase.CreateProduct(ctx, &productdomain.CreateProductRequest{
		ProductTypeID:  req.GetProductTypeId(),
		SchemaVersion:  req.GetSchemaVersion(),
		Attributes:     req.GetAttributes(),
		CategoryID:     req.GetCategoryId(),
		Name:           req.GetName(),
		Slug:           req.GetSlug(),
		Description:    optionalString(req.GetDescription()),
		Brand:          optionalString(req.GetBrand()),
		PriceCents:     req.GetPriceCents(),
		Currency:       req.GetCurrency(),
		Stock:          int64(req.GetStock()),
		Status:         status,
		PrimaryMediaID: primaryMediaID,
		Variants:       variants,
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &productpb.CreateProductResponse{Product: productToProto(p)}, nil
}

// UpdateProduct implements productpb.ProductServiceServer.
func (s *ProductServer) UpdateProduct(
	ctx context.Context,
	req *productpb.UpdateProductRequest,
) (*productpb.UpdateProductResponse, error) {
	var statusFilter *productdomain.Status
	if v := req.Status; v != nil {
		parsed, err := productdomain.ParseStatus(statusToString(*v))
		if err != nil {
			return nil, toStatus(err)
		}
		statusFilter = &parsed
	}

	var stock *int64
	if req.Stock != nil {
		value := int64(*req.Stock)
		stock = &value
	}

	var currency *productdomain.Currency
	if req.Currency != nil {
		c := productdomain.Currency(*req.Currency)
		currency = &c
	}

	update := productdomain.NewUpdateProductRequest(productdomain.UpdateFields{
		ProductTypeID:  req.ProductTypeId,
		SchemaVersion:  req.SchemaVersion,
		Attributes:     req.GetAttributes(),
		CategoryID:     req.CategoryId,
		Name:           req.Name,
		Slug:           req.Slug,
		Description:    req.Description,
		Brand:          req.Brand,
		PriceCents:     req.PriceCents,
		Currency:       currency,
		Stock:          stock,
		Status:         statusFilter,
		PrimaryMediaID: req.PrimaryMediaId,
	})

	p, err := s.usecase.UpdateProduct(ctx, req.GetId(), update)
	if err != nil {
		return nil, toStatus(err)
	}
	return &productpb.UpdateProductResponse{Product: productToProto(p)}, nil
}

// DeleteProduct implements productpb.ProductServiceServer.
func (s *ProductServer) DeleteProduct(
	ctx context.Context,
	req *productpb.DeleteProductRequest,
) (*productpb.DeleteProductResponse, error) {
	if err := s.usecase.DeleteProduct(ctx, req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &productpb.DeleteProductResponse{Deleted: true}, nil
}

// toStatus maps a domain error to a gRPC status.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, productdomain.ErrProductNotFound), errors.Is(err, productdomain.ErrVariantNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, productdomain.ErrInvalidInput),
		errors.Is(err, productdomain.ErrEmptyName),
		errors.Is(err, productdomain.ErrEmptySKU),
		errors.Is(err, productdomain.ErrInvalidSlug),
		errors.Is(err, productdomain.ErrInvalidSKU),
		errors.Is(err, productdomain.ErrInvalidCurrency),
		errors.Is(err, productdomain.ErrDescriptionTooLong),
		errors.Is(err, productdomain.ErrNameTooLong),
		errors.Is(err, productdomain.ErrSKUTooLong),
		errors.Is(err, productdomain.ErrSlugTooLong),
		errors.Is(err, productdomain.ErrNegativePrice),
		errors.Is(err, productdomain.ErrNegativeStock),
		errors.Is(err, productdomain.ErrMaxVariantsExceeded),
		errors.Is(err, productdomain.ErrDuplicateSKU),
		errors.Is(err, productdomain.ErrVariantNotOwnedByProduct):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, productdomain.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, productdomain.ErrOutboxBlocked), errors.Is(err, productdomain.ErrUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func productToProto(p *productdomain.Product) *productpb.Product {
	if p == nil {
		return nil
	}
	proto := &productpb.Product{
		Id:            p.ID,
		ProductTypeId: p.ProductTypeID,
		SchemaVersion: p.SchemaVersion,
		Attributes:    p.Attributes,
		CategoryId:    p.CategoryID,
		Name:          p.Name,
		Slug:          p.Slug,
		PriceCents:    p.Price.Amount,
		Currency:      string(p.Price.Currency),
		Stock:         int32(p.Stock),
		Status:        statusFromString(p.Status),
		CreatedAt:     timestamppb.New(p.CreatedAt),
		UpdatedAt:     timestamppb.New(p.UpdatedAt),
	}
	if p.Description != nil {
		proto.Description = *p.Description
	}
	if p.Brand != nil {
		proto.Brand = *p.Brand
	}
	if p.PrimaryMediaID != nil {
		proto.PrimaryMediaId = *p.PrimaryMediaID
	}
	proto.Variants = make([]*productpb.Variant, 0, len(p.Variants))
	for _, v := range p.Variants {
		proto.Variants = append(proto.Variants, variantToProto(v))
	}
	return proto
}

func variantToProto(v *productdomain.Variant) *productpb.Variant {
	if v == nil {
		return nil
	}
	proto := &productpb.Variant{
		Id:         v.ID,
		ProductId:  v.ProductID,
		Sku:        v.SKU,
		Name:       v.Name,
		PriceCents: v.Price.Amount,
		Currency:   string(v.Price.Currency),
		Stock:      int32(v.Stock),
	}
	if v.MediaID != nil {
		proto.MediaId = *v.MediaID
	}
	return proto
}

func statusToString(s productpb.ProductStatus) string {
	switch s {
	case productpb.ProductStatus_PRODUCT_STATUS_DRAFT:
		return string(productdomain.StatusDraft)
	case productpb.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return string(productdomain.StatusActive)
	case productpb.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return string(productdomain.StatusArchived)
	default:
		return ""
	}
}

func statusFromString(s productdomain.Status) productpb.ProductStatus {
	switch s {
	case productdomain.StatusDraft:
		return productpb.ProductStatus_PRODUCT_STATUS_DRAFT
	case productdomain.StatusActive:
		return productpb.ProductStatus_PRODUCT_STATUS_ACTIVE
	case productdomain.StatusArchived:
		return productpb.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return productpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}
