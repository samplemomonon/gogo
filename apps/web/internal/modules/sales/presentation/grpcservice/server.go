package grpcservice

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	salesv1 "gogo/contracts/sales/v1"
	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/domain"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/authn"
	"gogo/internal/shared/tenant"
)

type Authorizer interface {
	Authorize(context.Context, string, string) (authconsumer.Principal, bool, error)
}

// service adapts the sales application port to the sales.v1 contract.
type service struct {
	salesv1.UnimplementedSalesServiceServer

	svc        ports.SalesService
	logger     *slog.Logger
	authorizer Authorizer
}

// Register mounts the sales service and the standard gRPC health service.
func Register(s grpc.ServiceRegistrar, svc ports.SalesService, logger *slog.Logger, ping func(context.Context) error, authorizers ...Authorizer) {
	var authorizer Authorizer
	if len(authorizers) > 0 {
		authorizer = authorizers[0]
	}
	impl := &service{svc: svc, logger: logger, authorizer: authorizer}
	salesv1.RegisterSalesServiceServer(s, impl)
	healthpb.RegisterHealthServer(s, newHealthServer(ping))
}

func tenantOf(ctx context.Context) domain.Tenant {
	return domain.Tenant(tenant.FromContextOrDefault(ctx))
}

func (s *service) Checkout(ctx context.Context, req *salesv1.CheckoutRequest) (*salesv1.CheckoutResponse, error) {
	principal, err := s.authorize(ctx, "seller.sales.checkout")
	if err != nil {
		return nil, err
	}
	items := make([]domain.CheckoutItem, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		items = append(items, domain.CheckoutItem{
			ProductID: item.GetProductId(),
			Name:      item.GetName(),
			Quantity:  int(item.GetQuantity()),
			Price:     item.GetPrice(),
			Discount:  item.GetDiscount(),
		})
	}

	transaction, err := s.svc.Checkout(ctx, tenantOf(ctx), items, req.GetDiscount(), req.GetPaid(),
		req.GetPaymentMethod(), principal.Name, principal.EmployeeID)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &salesv1.CheckoutResponse{Transaction: toProto(transaction)}, nil
}

func (s *service) GetTransaction(ctx context.Context, req *salesv1.GetTransactionRequest) (*salesv1.GetTransactionResponse, error) {
	if _, err := s.authorize(ctx, "seller.sales.read"); err != nil {
		return nil, err
	}
	transaction, err := s.svc.Get(ctx, tenantOf(ctx), req.GetId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &salesv1.GetTransactionResponse{Transaction: toProto(transaction)}, nil
}

func (s *service) ListTransactions(ctx context.Context, req *salesv1.ListTransactionsRequest) (*salesv1.ListTransactionsResponse, error) {
	if _, err := s.authorize(ctx, "seller.sales.read"); err != nil {
		return nil, err
	}
	found, err := s.svc.List(ctx, tenantOf(ctx), int(req.GetPageSize()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*salesv1.Transaction, 0, len(found))
	for _, transaction := range found {
		out = append(out, toProto(transaction))
	}
	return &salesv1.ListTransactionsResponse{Transactions: out}, nil
}

func (s *service) VoidTransaction(ctx context.Context, req *salesv1.VoidTransactionRequest) (*salesv1.VoidTransactionResponse, error) {
	principal, err := s.authorize(ctx, "seller.sales.void")
	if err != nil {
		return nil, err
	}
	transaction, err := s.svc.Void(ctx, tenantOf(ctx), req.GetId(), principal.EmployeeID, req.GetReason())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &salesv1.VoidTransactionResponse{Transaction: toProto(transaction)}, nil
}

func (s *service) authorize(ctx context.Context, permission string) (authconsumer.Principal, error) {
	if s.authorizer == nil {
		return authconsumer.Principal{}, status.Error(codes.Unauthenticated, "authorization is not configured")
	}
	principal, allowed, err := s.authorizer.Authorize(ctx, authn.Session(ctx), permission)
	if err != nil {
		return authconsumer.Principal{}, status.Error(codes.Unavailable, "authorization unavailable")
	}
	if !allowed {
		s.logger.WarnContext(ctx, "authorization denied", "tenant_id", tenantOf(ctx), "subject_id", principal.EmployeeID, "permission", permission, "service", "sales")
		return authconsumer.Principal{}, status.Error(codes.PermissionDenied, "permission denied")
	}
	return principal, nil
}

func toProto(transaction *domain.Transaction) *salesv1.Transaction {
	if transaction == nil {
		return nil
	}
	items := make([]*salesv1.TransactionItem, 0, len(transaction.Items))
	for _, item := range transaction.Items {
		items = append(items, &salesv1.TransactionItem{
			Id:        item.ID,
			ProductId: item.ProductID,
			Name:      item.Name,
			Quantity:  int32(item.Quantity),
			Price:     item.Price,
			Discount:  item.Discount,
			Subtotal:  item.Subtotal,
		})
	}

	out := &salesv1.Transaction{
		Id:            transaction.ID,
		Number:        transaction.Number,
		Total:         transaction.Total,
		Discount:      transaction.Discount,
		Paid:          transaction.Paid,
		Change:        transaction.Change,
		PaymentMethod: transaction.PaymentMethod,
		Cashier:       transaction.Cashier,
		EmployeeId:    transaction.EmployeeID,
		CreatedAt:     timestamppb.New(transaction.CreatedAt),
		VoidedBy:      transaction.VoidedBy,
		VoidReason:    transaction.VoidReason,
		Items:         items,
	}
	if transaction.VoidedAt != nil {
		out.VoidedAt = timestamppb.New(*transaction.VoidedAt)
	}
	return out
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
		s.logger.Error("sales grpc internal error", "error", err)
		return status.Error(codes.Internal, domain.Message(err))
	}
}
