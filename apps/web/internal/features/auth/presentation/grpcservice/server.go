// Package grpcservice adapts the auth application port to the auth.v1 contract.
package grpcservice

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/features/auth/application/ports"
	"gogo/internal/features/auth/domain"
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
	return nil, s.employeeAdministrationUnavailable(ctx, "RegisterEmployee")
}

func (s *service) GetEmployee(ctx context.Context, req *authv1.GetEmployeeRequest) (*authv1.GetEmployeeResponse, error) {
	return nil, s.employeeAdministrationUnavailable(ctx, "GetEmployee")
}

func (s *service) GetEmployeeByEmail(ctx context.Context, req *authv1.GetEmployeeByEmailRequest) (*authv1.GetEmployeeByEmailResponse, error) {
	return nil, s.employeeAdministrationUnavailable(ctx, "GetEmployeeByEmail")
}

func (s *service) UpdateEmployee(ctx context.Context, req *authv1.UpdateEmployeeRequest) (*authv1.UpdateEmployeeResponse, error) {
	return nil, s.employeeAdministrationUnavailable(ctx, "UpdateEmployee")
}

func (s *service) ListEmployees(ctx context.Context, req *authv1.ListEmployeesRequest) (*authv1.ListEmployeesResponse, error) {
	session, err := s.authorizeSession(ctx, req.GetSessionId(), "authz.employee.read")
	if err != nil {
		return nil, err
	}
	employees, err := s.svc.ListEmployees(ctx, domain.Tenant(session.TenantID), int(req.GetLimit()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*authv1.Employee, 0, len(employees))
	for _, employee := range employees {
		out = append(out, s.toProtoEmployee(employee))
	}
	return &authv1.ListEmployeesResponse{Employees: out}, nil
}

func (s *service) SetEmployeePIN(ctx context.Context, req *authv1.SetEmployeePINRequest) (*authv1.SetEmployeePINResponse, error) {
	return nil, s.employeeAdministrationUnavailable(ctx, "SetEmployeePIN")
}

func (s *service) ActivateEmployee(ctx context.Context, req *authv1.ActivateEmployeeRequest) (*authv1.ActivateEmployeeResponse, error) {
	return nil, s.employeeAdministrationUnavailable(ctx, "ActivateEmployee")
}

func (s *service) DeactivateEmployee(ctx context.Context, req *authv1.DeactivateEmployeeRequest) (*authv1.DeactivateEmployeeResponse, error) {
	return nil, s.employeeAdministrationUnavailable(ctx, "DeactivateEmployee")
}

// employeeAdministrationUnavailable closes legacy RPCs that have no session
// and permission fields. Employee reads and writes must not bypass the dynamic
// authorization model. Login/session and policy administration use separate
// supported RPCs.
func (s *service) employeeAdministrationUnavailable(ctx context.Context, method string) error {
	s.logger.WarnContext(ctx, "legacy employee administration RPC rejected", "method", method)
	return status.Error(codes.Unimplemented, "employee administration requires a session-authorized API")
}

func (s *service) authorizeSession(ctx context.Context, sessionID, permission string) (*domain.Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, status.Error(codes.Unauthenticated, "session required")
	}
	session, employee, err := s.svc.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, s.toStatus(err)
	}
	allowed, err := s.svc.CheckPermission(ctx, sessionID, permission)
	if err != nil {
		return nil, s.toStatus(err)
	}
	if !allowed {
		s.logger.WarnContext(ctx, "authorization denied", "tenant_id", session.TenantID, "subject_id", employee.ID, "permission", permission, "service", "auth")
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	return session, nil
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

func (s *service) CheckPermission(ctx context.Context, req *authv1.CheckPermissionRequest) (*authv1.CheckPermissionResponse, error) {
	allowed, err := s.svc.CheckPermission(ctx, req.GetSessionId(), req.GetPermissionKey())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.CheckPermissionResponse{Allowed: allowed}, nil
}

func optionalTime(ts *timestamppb.Timestamp) (*time.Time, error) {
	if ts == nil {
		return nil, nil
	}
	if err := ts.CheckValid(); err != nil {
		return nil, domain.ErrInvalidPolicyChange
	}
	value := ts.AsTime()
	return &value, nil
}

func (s *service) CreateRole(ctx context.Context, req *authv1.CreateRoleRequest) (*authv1.CreateRoleResponse, error) {
	roleID, err := s.svc.CreateRole(ctx, req.GetSessionId(), req.GetName(), req.GetDescription(), req.GetChangeReference())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.CreateRoleResponse{RoleId: roleID}, nil
}

func (s *service) GrantPermission(ctx context.Context, req *authv1.GrantPermissionRequest) (*authv1.GrantPermissionResponse, error) {
	expiresAt, err := optionalTime(req.GetExpiresAt())
	if err != nil {
		return nil, s.toStatus(err)
	}
	grantID, err := s.svc.GrantPermission(ctx, req.GetSessionId(), req.GetRoleId(), req.GetPermissionKey(), expiresAt, req.GetReason(), req.GetChangeReference())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.GrantPermissionResponse{GrantId: grantID}, nil
}

func (s *service) RevokePermission(ctx context.Context, req *authv1.RevokePermissionRequest) (*authv1.RevokePermissionResponse, error) {
	if err := s.svc.RevokePermission(ctx, req.GetSessionId(), req.GetGrantId(), req.GetReason(), req.GetChangeReference()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.RevokePermissionResponse{}, nil
}

func (s *service) AssignRole(ctx context.Context, req *authv1.AssignRoleRequest) (*authv1.AssignRoleResponse, error) {
	validFrom, err := optionalTime(req.GetValidFrom())
	if err != nil {
		return nil, s.toStatus(err)
	}
	expiresAt, err := optionalTime(req.GetExpiresAt())
	if err != nil {
		return nil, s.toStatus(err)
	}
	assignmentID, err := s.svc.AssignRole(ctx, req.GetSessionId(), req.GetEmployeeId(), req.GetRoleId(), validFrom, expiresAt, req.GetReason(), req.GetChangeReference())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.AssignRoleResponse{AssignmentId: assignmentID}, nil
}

func (s *service) RevokeRoleAssignment(ctx context.Context, req *authv1.RevokeRoleAssignmentRequest) (*authv1.RevokeRoleAssignmentResponse, error) {
	if err := s.svc.RevokeRoleAssignment(ctx, req.GetSessionId(), req.GetAssignmentId(), req.GetReason(), req.GetChangeReference()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.RevokeRoleAssignmentResponse{}, nil
}

func (s *service) ListAuthorizationRoles(ctx context.Context, req *authv1.ListAuthorizationRolesRequest) (*authv1.ListAuthorizationRolesResponse, error) {
	roles, err := s.svc.ListAuthorizationRoles(ctx, req.GetSessionId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	response := &authv1.ListAuthorizationRolesResponse{Roles: make([]*authv1.AuthorizationRole, 0, len(roles))}
	for _, role := range roles {
		response.Roles = append(response.Roles, &authv1.AuthorizationRole{Id: role.ID, Name: role.Name, Description: role.Description, ActivePermissionKeys: role.Permissions})
	}
	return response, nil
}

func (s *service) ListRoleAssignments(ctx context.Context, req *authv1.ListRoleAssignmentsRequest) (*authv1.ListRoleAssignmentsResponse, error) {
	assignments, err := s.svc.ListRoleAssignments(ctx, req.GetSessionId(), req.GetEmployeeId(), req.GetIncludeHistory())
	if err != nil {
		return nil, s.toStatus(err)
	}
	response := &authv1.ListRoleAssignmentsResponse{Assignments: make([]*authv1.RoleAssignment, 0, len(assignments))}
	for _, assignment := range assignments {
		item := &authv1.RoleAssignment{
			Id: assignment.ID, EmployeeId: assignment.EmployeeID, RoleId: assignment.RoleID,
			Reason: assignment.Reason, AssignedAt: timestamppb.New(assignment.AssignedAt),
			ValidFrom: timestamppb.New(assignment.ValidFrom), AssignedBy: assignment.AssignedBy, Source: assignment.Source,
		}
		if assignment.ExpiresAt != nil {
			item.ExpiresAt = timestamppb.New(*assignment.ExpiresAt)
		}
		if assignment.RevokedAt != nil {
			item.RevokedAt = timestamppb.New(*assignment.RevokedAt)
		}
		response.Assignments = append(response.Assignments, item)
	}
	return response, nil
}

func (s *service) ListTenantRoleAssignments(ctx context.Context, req *authv1.ListTenantRoleAssignmentsRequest) (*authv1.ListTenantRoleAssignmentsResponse, error) {
	assignments, next, err := s.svc.ListTenantRoleAssignments(ctx, req.GetSessionId(), int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, s.toStatus(err)
	}
	response := &authv1.ListTenantRoleAssignmentsResponse{Assignments: make([]*authv1.RoleAssignment, 0, len(assignments)), NextPageToken: next}
	for _, assignment := range assignments {
		item := &authv1.RoleAssignment{Id: assignment.ID, EmployeeId: assignment.EmployeeID, RoleId: assignment.RoleID, Reason: assignment.Reason, AssignedAt: timestamppb.New(assignment.AssignedAt), ValidFrom: timestamppb.New(assignment.ValidFrom), AssignedBy: assignment.AssignedBy, Source: assignment.Source}
		if assignment.ExpiresAt != nil {
			item.ExpiresAt = timestamppb.New(*assignment.ExpiresAt)
		}
		if assignment.RevokedAt != nil {
			item.RevokedAt = timestamppb.New(*assignment.RevokedAt)
		}
		response.Assignments = append(response.Assignments, item)
	}
	return response, nil
}

func (s *service) ListAuthorizationAdminEvents(ctx context.Context, req *authv1.ListAuthorizationAdminEventsRequest) (*authv1.ListAuthorizationAdminEventsResponse, error) {
	events, next, err := s.svc.ListAuthorizationAdminEvents(ctx, req.GetSessionId(), int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, s.toStatus(err)
	}
	response := &authv1.ListAuthorizationAdminEventsResponse{Events: make([]*authv1.AuthorizationAdminEvent, 0, len(events)), NextPageToken: next}
	for _, event := range events {
		response.Events = append(response.Events, &authv1.AuthorizationAdminEvent{Id: event.ID, Action: event.Action, SubjectId: event.SubjectID, RoleId: event.RoleID, ActorEmployeeId: event.ActorEmployeeID, ActorDatabaseUser: event.ActorDatabaseUser, ChangeReference: event.ChangeReference, Reason: event.Reason, OccurredAt: timestamppb.New(event.OccurredAt)})
	}
	return response, nil
}

// ---- merchant relationship handlers ---------------------------------------

func (s *service) RegisterAccount(ctx context.Context, req *authv1.RegisterAccountRequest) (*authv1.RegisterAccountResponse, error) {
	employee, err := s.svc.RegisterAccount(ctx, tenantOf(ctx), req.GetName(), req.GetEmail(), req.GetPin())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.RegisterAccountResponse{Employee: s.toProtoEmployee(employee)}, nil
}

func (s *service) OpenMerchant(ctx context.Context, req *authv1.OpenMerchantRequest) (*authv1.OpenMerchantResponse, error) {
	merchant, err := s.svc.OpenMerchant(ctx, req.GetSessionId(), req.GetName())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.OpenMerchantResponse{Merchant: s.toProtoMerchant(merchant)}, nil
}

func (s *service) ListMyMerchants(ctx context.Context, req *authv1.ListMyMerchantsRequest) (*authv1.ListMyMerchantsResponse, error) {
	merchants, err := s.svc.ListMyMerchants(ctx, req.GetSessionId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*authv1.Merchant, 0, len(merchants))
	for _, merchant := range merchants {
		out = append(out, s.toProtoMerchant(merchant))
	}
	return &authv1.ListMyMerchantsResponse{Merchants: out}, nil
}

func (s *service) GetMerchant(ctx context.Context, req *authv1.GetMerchantRequest) (*authv1.GetMerchantResponse, error) {
	merchant, err := s.svc.GetMerchant(ctx, req.GetSessionId(), req.GetMerchantId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.GetMerchantResponse{Merchant: s.toProtoMerchant(merchant)}, nil
}

func (s *service) ListMembers(ctx context.Context, req *authv1.ListMembersRequest) (*authv1.ListMembersResponse, error) {
	members, err := s.svc.ListMembers(ctx, req.GetSessionId(), req.GetMerchantId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	out := make([]*authv1.MerchantMembership, 0, len(members))
	for _, member := range members {
		out = append(out, s.toProtoMembership(member))
	}
	return &authv1.ListMembersResponse{Members: out}, nil
}

func (s *service) InviteMember(ctx context.Context, req *authv1.InviteMemberRequest) (*authv1.InviteMemberResponse, error) {
	invitation, err := s.svc.InviteMember(ctx, req.GetSessionId(), req.GetMerchantId(), req.GetEmail(), domain.MerchantRole(req.GetRole()))
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.InviteMemberResponse{Invitation: s.toProtoInvitation(invitation)}, nil
}

func (s *service) AcceptInvitation(ctx context.Context, req *authv1.AcceptInvitationRequest) (*authv1.AcceptInvitationResponse, error) {
	membership, err := s.svc.AcceptInvitation(ctx, req.GetSessionId(), req.GetToken())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.AcceptInvitationResponse{Membership: s.toProtoMembership(membership)}, nil
}

func (s *service) RevokeInvitation(ctx context.Context, req *authv1.RevokeInvitationRequest) (*authv1.RevokeInvitationResponse, error) {
	if err := s.svc.RevokeInvitation(ctx, req.GetSessionId(), req.GetInvitationId()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.RevokeInvitationResponse{}, nil
}

func (s *service) GrantMerchantPermission(ctx context.Context, req *authv1.GrantMerchantPermissionRequest) (*authv1.GrantMerchantPermissionResponse, error) {
	if err := s.svc.GrantMerchantPermission(ctx, req.GetSessionId(), req.GetMerchantId(), req.GetEmployeeId(), req.GetPermissionKey()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.GrantMerchantPermissionResponse{}, nil
}

func (s *service) RevokeMerchantPermission(ctx context.Context, req *authv1.RevokeMerchantPermissionRequest) (*authv1.RevokeMerchantPermissionResponse, error) {
	if err := s.svc.RevokeMerchantPermission(ctx, req.GetSessionId(), req.GetMerchantId(), req.GetEmployeeId(), req.GetPermissionKey()); err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.RevokeMerchantPermissionResponse{}, nil
}

func (s *service) ListMerchantPermissions(ctx context.Context, req *authv1.ListMerchantPermissionsRequest) (*authv1.ListMerchantPermissionsResponse, error) {
	keys, err := s.svc.ListMerchantPermissions(ctx, req.GetSessionId(), req.GetMerchantId(), req.GetEmployeeId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.ListMerchantPermissionsResponse{PermissionKeys: keys}, nil
}

func (s *service) CanManageProduct(ctx context.Context, req *authv1.CanManageProductRequest) (*authv1.CanManageProductResponse, error) {
	allowed, err := s.svc.CanManageProduct(ctx, req.GetSessionId(), req.GetMerchantId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &authv1.CanManageProductResponse{Allowed: allowed}, nil
}

func (s *service) toProtoMerchant(m *domain.Merchant) *authv1.Merchant {
	if m == nil {
		return nil
	}
	return &authv1.Merchant{
		Id:              m.ID,
		TenantId:        m.TenantID,
		OwnerEmployeeId: m.OwnerEmployeeID,
		Name:            m.Name,
		CreatedAt:       timestamppb.New(m.CreatedAt),
		UpdatedAt:       timestamppb.New(m.UpdatedAt),
	}
}

func (s *service) toProtoMembership(m *domain.Membership) *authv1.MerchantMembership {
	if m == nil {
		return nil
	}
	return &authv1.MerchantMembership{
		TenantId:   m.TenantID,
		MerchantId: m.MerchantID,
		EmployeeId: m.EmployeeID,
		Role:       string(m.Role),
		GrantedBy:  m.GrantedBy,
		CreatedAt:  timestamppb.New(m.CreatedAt),
	}
}

func (s *service) toProtoInvitation(inv *domain.Invitation) *authv1.MerchantInvitation {
	if inv == nil {
		return nil
	}
	out := &authv1.MerchantInvitation{
		Id:         inv.ID,
		TenantId:   inv.TenantID,
		MerchantId: inv.MerchantID,
		Email:      inv.Email,
		Role:       string(inv.Role),
		Status:     string(inv.Status),
		Token:      inv.Token,
		ExpiresAt:  timestamppb.New(inv.ExpiresAt),
		InvitedBy:  inv.InvitedBy,
		CreatedAt:  timestamppb.New(inv.CreatedAt),
	}
	if inv.AcceptedAt != nil {
		out.AcceptedAt = timestamppb.New(*inv.AcceptedAt)
	}
	if inv.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*inv.RevokedAt)
	}
	return out
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
	case domain.KindForbidden:
		return status.Error(codes.PermissionDenied, domain.Message(err))
	case domain.KindConflict:
		return status.Error(codes.AlreadyExists, domain.Message(err))
	default:
		s.logger.Error("auth grpc internal error", "error", err)
		return status.Error(codes.Internal, domain.Message(err))
	}
}
