package grpcservice

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/domain"
	"gogo/internal/shared/tenant"
)

// service adapts the inventory application port to the inventory.v1 contract.
type service struct {
	inventoryv1.UnimplementedInventoryServiceServer

	svc    ports.StockService
	logger *slog.Logger
	ping   func(context.Context) error
}

// Register mounts the inventory service and the standard gRPC health service.
func Register(s grpc.ServiceRegistrar, svc ports.StockService, logger *slog.Logger, ping func(context.Context) error) {
	impl := &service{svc: svc, logger: logger, ping: ping}
	inventoryv1.RegisterInventoryServiceServer(s, impl)
	healthpb.RegisterHealthServer(s, newHealthServer(ping))
}

func tenantOf(ctx context.Context) domain.Tenant {
	return domain.Tenant(tenant.FromContextOrDefault(ctx))
}

func (s *service) GetStock(ctx context.Context, req *inventoryv1.GetStockRequest) (*inventoryv1.GetStockResponse, error) {
	stock, err := s.svc.GetStock(ctx, tenantOf(ctx), req.GetProductId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &inventoryv1.GetStockResponse{Stock: stockToProto(stock)}, nil
}

func (s *service) ListStocks(ctx context.Context, req *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
	found, err := s.svc.ListStocks(ctx, tenantOf(ctx), req.GetProductIds())
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*inventoryv1.Stock, 0, len(found))
	for _, stock := range found {
		out = append(out, stockToProto(stock))
	}
	return &inventoryv1.ListStocksResponse{Stocks: out}, nil
}

func (s *service) AdjustStock(ctx context.Context, req *inventoryv1.AdjustStockRequest) (*inventoryv1.AdjustStockResponse, error) {
	stock, movement, err := s.svc.AdjustStock(
		ctx, tenantOf(ctx), req.GetProductId(), int(req.GetDelta()),
		req.GetKind(), req.GetNote(), req.GetActor(), req.GetReference(),
	)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &inventoryv1.AdjustStockResponse{Stock: stockToProto(stock), Movement: movementToProto(movement)}, nil
}

func (s *service) ListStockMovements(ctx context.Context, req *inventoryv1.ListStockMovementsRequest) (*inventoryv1.ListStockMovementsResponse, error) {
	found, err := s.svc.ListMovements(ctx, tenantOf(ctx), req.GetProductId(), int(req.GetPageSize()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*inventoryv1.StockMovement, 0, len(found))
	for _, movement := range found {
		out = append(out, movementToProto(movement))
	}
	return &inventoryv1.ListStockMovementsResponse{Movements: out}, nil
}

func stockToProto(stock *domain.Stock) *inventoryv1.Stock {
	if stock == nil {
		return nil
	}
	return &inventoryv1.Stock{
		ProductId: stock.ProductID,
		Quantity:  int32(stock.Quantity),
		UpdatedAt: timestamppb.New(stock.UpdatedAt),
	}
}

func movementToProto(movement *domain.Movement) *inventoryv1.StockMovement {
	if movement == nil {
		return nil
	}
	return &inventoryv1.StockMovement{
		Id:             movement.ID,
		ProductId:      movement.ProductID,
		Kind:           movement.Kind,
		QuantityDelta:  int32(movement.Delta),
		QuantityBefore: int32(movement.Before),
		QuantityAfter:  int32(movement.After),
		Note:           movement.Note,
		Actor:          movement.Actor,
		Reference:      movement.Reference,
		CreatedAt:      timestamppb.New(movement.CreatedAt),
	}
}

func (s *service) toStatus(err error) error {
	switch domain.Classify(err) {
	case domain.KindNotFound:
		return status.Error(codes.NotFound, domain.Message(err))
	case domain.KindInvalid:
		return status.Error(codes.InvalidArgument, domain.Message(err))
	case domain.KindFailedPrecondition:
		return status.Error(codes.FailedPrecondition, domain.Message(err))
	default:
		s.logger.Error("inventory grpc internal error", "error", err)
		return status.Error(codes.Internal, domain.Message(err))
	}
}
