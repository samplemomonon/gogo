package grpcservice

import (
	"context"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/features/inventory/application/ports"
	"gogo/internal/features/inventory/domain"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/authn"
	"gogo/internal/shared/tenant"
)

type Authorizer interface {
	Authorize(context.Context, string, string) (authconsumer.Principal, bool, error)
}

type Security struct {
	Authorizer      Authorizer
	SalesCapability authn.Capability
}

// service adapts the inventory application port to the inventory.v1 contract.
type service struct {
	inventoryv1.UnimplementedInventoryServiceServer

	svc             ports.StockService
	logger          *slog.Logger
	ping            func(context.Context) error
	authorizer      Authorizer
	salesCapability authn.Capability
}

// Register mounts the inventory service and the standard gRPC health service.
func Register(s grpc.ServiceRegistrar, svc ports.StockService, logger *slog.Logger, ping func(context.Context) error, security ...Security) {
	var cfg Security
	if len(security) > 0 {
		cfg = security[0]
	}
	impl := &service{svc: svc, logger: logger, ping: ping, authorizer: cfg.Authorizer, salesCapability: cfg.SalesCapability}
	inventoryv1.RegisterInventoryServiceServer(s, impl)
	healthpb.RegisterHealthServer(s, newHealthServer(ping))
}

func tenantOf(ctx context.Context) domain.Tenant {
	return domain.Tenant(tenant.FromContextOrDefault(ctx))
}

func (s *service) GetStock(ctx context.Context, req *inventoryv1.GetStockRequest) (*inventoryv1.GetStockResponse, error) {
	if err := s.authorize(ctx, "seller.inventory.read"); err != nil {
		return nil, err
	}
	stock, err := s.svc.GetStock(ctx, tenantOf(ctx), req.GetProductId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &inventoryv1.GetStockResponse{Stock: stockToProto(stock)}, nil
}

func (s *service) ListStocks(ctx context.Context, req *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
	if err := s.authorize(ctx, "seller.inventory.read"); err != nil {
		return nil, err
	}
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
	permission := "seller.inventory.manage"
	if authn.HasServiceCapability(ctx, authn.SalesCaller, s.salesCapability) {
		permission = "seller.sales.checkout"
		if req.GetKind() == "void" && !strings.Contains(req.GetReference(), ":comp:") {
			permission = "seller.sales.void"
		}
	} else if authn.Caller(ctx) != "" {
		return nil, status.Error(codes.PermissionDenied, "unrecognized service caller")
	}
	principal, err := s.authorizePrincipal(ctx, permission)
	if err != nil {
		return nil, err
	}
	serviceActor := ""
	if authn.HasServiceCapability(ctx, authn.SalesCaller, s.salesCapability) {
		serviceActor = authn.SalesCaller
	}
	stock, movement, err := s.svc.AdjustStock(
		ctx, tenantOf(ctx), req.GetProductId(), int(req.GetDelta()),
		req.GetKind(), req.GetNote(), principal.EmployeeID, serviceActor, req.GetReference(),
	)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &inventoryv1.AdjustStockResponse{Stock: stockToProto(stock), Movement: movementToProto(movement)}, nil
}

func (s *service) ListStockMovements(ctx context.Context, req *inventoryv1.ListStockMovementsRequest) (*inventoryv1.ListStockMovementsResponse, error) {
	if err := s.authorize(ctx, "seller.inventory.read"); err != nil {
		return nil, err
	}
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

func (s *service) authorize(ctx context.Context, permission string) error {
	_, err := s.authorizePrincipal(ctx, permission)
	return err
}

func (s *service) authorizePrincipal(ctx context.Context, permission string) (authconsumer.Principal, error) {
	if s.authorizer == nil {
		return authconsumer.Principal{}, status.Error(codes.Unauthenticated, "authorization is not configured")
	}
	principal, allowed, err := s.authorizer.Authorize(ctx, authn.Session(ctx), permission)
	if err != nil {
		return authconsumer.Principal{}, status.Error(codes.Unavailable, "authorization unavailable")
	}
	if !allowed {
		s.logger.WarnContext(ctx, "authorization denied", "tenant_id", tenantOf(ctx), "subject_id", principal.EmployeeID, "permission", permission, "service", "inventory")
		return authconsumer.Principal{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	return principal, nil
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
