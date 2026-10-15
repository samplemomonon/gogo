// Package grpcservice adapts the auth application port to the auth.v1 contract.
package grpcservice

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
	"gogo/internal/shared/tenant"
)

var _ authv1.AuthServiceServer = (*service)(nil)

// service adapts the auth application port to the auth.v1 contract.
type service struct {
	authv1.UnimplementedAuthServiceServer

	svc    ports.AuthService
	logger *slog.Logger
}

// Register mounts the auth service on a gRPC server.
func Register(s grpc.ServiceRegistrar, svc ports.AuthService, logger *slog.Logger) {
	impl := &service{svc: svc, logger: logger}
	authv1.RegisterAuthServiceServer(s, impl)
}

func tenantOf(ctx context.Context) domain.Tenant {
	return domain.Tenant(tenant.FromContextOrDefault(ctx))
}

func (s *service) RegisterEmployee(ctx context.Context, req *authv1.RegisterEmployeeRequest) (*authv1.RegisterEmployeeResponse, error) {
	employee, err := s.svc.RegisterEmployee(ctx, tenantOf(ctx), req.GetId(), req.GetName(), req.GetEmail(), req.GetPin(), domain.Role(req.GetRole()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.RegisterEmployeeResponse{Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) GetEmployee(ctx context.Context, req *authv1.GetEmployeeRequest) (*authv1.GetEmployeeResponse, error) {
	employee, err := s.svc.GetEmployee(ctx, tenantOf(ctx), req.GetId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.GetEmployeeResponse{Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) GetEmployeeByEmail(ctx context.Context, req *authv1.GetEmployeeByEmailRequest) (*authv1.GetEmployeeByEmailResponse, error) {
	employee, err := s.svc.GetEmployeeByEmail(ctx, tenantOf(ctx), req.GetEmail())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.GetEmployeeByEmailResponse{Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) UpdateEmployee(ctx context.Context, req *authv1.UpdateEmployeeRequest) (*authv1.UpdateEmployeeResponse, error) {
	employee, err := s.svc.UpdateEmployee(ctx, tenantOf(ctx), req.GetId(), req.GetName(), req.GetEmail(), domain.Role(req.GetRole()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.UpdateEmployeeResponse{Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) ListEmployees(ctx context.Context, req *authv1.ListEmployeesRequest) (*authv1.ListEmployeesResponse, error) {
	employees, err := s.svc.ListEmployees(ctx, tenantOf(ctx), int(req.GetLimit()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*authv1.Employee, 0, len(employees))
	for _, e := range employees {
		out = append(out, s.toProtoEmployee(e))
	}
	return &authv1.ListEmployeesResponse{Employees: out}, nil
}

func (s *service) SetEmployeePIN(ctx context.Context, req *authv1.SetEmployeePINRequest) (*authv1.SetEmployeePINResponse, error) {
	if err := s.svc.SetEmployeePIN(ctx, tenantOf(ctx), req.GetId(), req.GetPin()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.SetEmployeePINResponse{}, nil
}

func (s *service) ActivateEmployee(ctx context.Context, req *authv1.ActivateEmployeeRequest) (*authv1.ActivateEmployeeResponse, error) {
	if err := s.svc.ActivateEmployee(ctx, tenantOf(ctx), req.GetId()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.ActivateEmployeeResponse{}, nil
}

func (s *service) DeactivateEmployee(ctx context.Context, req *authv1.DeactivateEmployeeRequest) (*authv1.DeactivateEmployeeResponse, error) {
	if err := s.svc.DeactivateEmployee(ctx, tenantOf(ctx), req.GetId()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.DeactivateEmployeeResponse{}, nil
}

func (s *service) Login(ctx context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	session, employee, err := s.svc.Login(ctx, tenantOf(ctx), req.GetEmail(), req.GetPin())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.LoginResponse{Session: s.toProtoSession(session), Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) Logout(ctx context.Context, req *authv1.LogoutRequest) (*authv1.LogoutResponse, error) {
	if err := s.svc.Logout(ctx, req.GetSessionId()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.LogoutResponse{}, nil
}

func (s *service) ValidateSession(ctx context.Context, req *authv1.ValidateSessionRequest) (*authv1.ValidateSessionResponse, error) {
	session, employee, err := s.svc.ValidateSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.ValidateSessionResponse{Session: s.toProtoSession(session), Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) ExtendSession(ctx context.Context, req *authv1.ExtendSessionRequest) (*authv1.ExtendSessionResponse, error) {
	session, err := s.svc.ExtendSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.ExtendSessionResponse{Session: s.toProtoSession(session)}, nil
}

func (s *service) toProtoEmployee(e *domain.Employee) *authv1.Employee {
	if e == nil {
		return nil
	}
	return &authv1.Employee{
		Id:        e.ID,
		TenantId:  e.TenantID,
		Name:      e.Name,
		Email:     e.Email,
		Role:      string(e.Role),
		Active:    e.Active,
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
	}
}

func (s *service) toProtoSession(sess *domain.Session) *authv1.Session {
	if sess == nil {
		return nil
	}
	out := &authv1.Session{
		Id:         sess.ID,
		TenantId:   sess.TenantID,
		EmployeeId: sess.EmployeeID,
		CreatedAt:  timestamppb.New(sess.CreatedAt),
		ExpiresAt:  timestamppb.New(sess.ExpiresAt),
	}
	if sess.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*sess.RevokedAt)
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
	case domain.KindUnauthenticated:
		return status.Error(codes.Unauthenticated, domain.Message(err))
	default:
		s.logger.Error("auth grpc internal error", "error", err)
		return status.Error(codes.Internal, domain.Message(err))
	}
}
