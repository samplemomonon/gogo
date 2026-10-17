package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gogo/internal/modules/auth/application"
	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
	"gogo/internal/modules/auth/infrastructure"
)

func newService() *application.Service {
	store := infrastructure.NewStore()
	return application.NewService(store, store, 0, time.Hour, store)
}

func TestRegisterEmployeeHashesPINAndAllowsLogin(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	employee, err := svc.RegisterEmployee(ctx, "default", "emp-1", "Budi", "budi@toko.com", "123456", domain.RoleSeller)
	if err != nil {
		t.Fatalf("RegisterEmployee: %v", err)
	}
	if employee.PINHash == "123456" {
		t.Fatal("PIN stored in plaintext")
	}

	if _, _, err := svc.Login(ctx, "default", "budi@toko.com", "123456"); err != nil {
		t.Fatalf("Login with the correct PIN: %v", err)
	}
	if _, _, err := svc.Login(ctx, "default", "budi@toko.com", "654321"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Login with the wrong PIN = %v, want ErrInvalidCredentials", err)
	}
}

func TestRegisterEmployeeRejectsMalformedPIN(t *testing.T) {
	svc := newService()

	_, err := svc.RegisterEmployee(context.Background(), "default", "emp-1", "Budi", "budi@toko.com", "12ab", domain.RoleSeller)
	if !errors.Is(err, domain.ErrInvalidPIN) {
		t.Fatalf("RegisterEmployee with a malformed PIN = %v, want ErrInvalidPIN", err)
	}
}

type failingEmployeeStore struct {
	ports.EmployeeStore
	err error
}

func (f failingEmployeeStore) GetByEmail(context.Context, domain.Tenant, string) (*domain.Employee, error) {
	return nil, f.err
}

func TestLoginSurfacesStoreFailure(t *testing.T) {
	boom := errors.New("database down")
	store := infrastructure.NewStore()
	svc := application.NewService(failingEmployeeStore{EmployeeStore: store, err: boom}, store, 0, time.Hour, store)

	_, _, err := svc.Login(context.Background(), "default", "budi@toko.com", "123456")
	if !errors.Is(err, boom) {
		t.Fatalf("Login = %v, want the store failure surfaced", err)
	}
	if errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Fatal("Login masked a store failure as ErrEmployeeNotFound")
	}
}

func TestSetEmployeePINReplacesCredential(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	if _, err := svc.RegisterEmployee(ctx, "default", "emp-1", "Budi", "budi@toko.com", "123456", domain.RoleSeller); err != nil {
		t.Fatalf("RegisterEmployee: %v", err)
	}

	if err := svc.SetEmployeePIN(ctx, "default", "emp-1", "111111"); err != nil {
		t.Fatalf("SetEmployeePIN: %v", err)
	}
	if _, _, err := svc.Login(ctx, "default", "budi@toko.com", "111111"); err != nil {
		t.Fatalf("Login with the new PIN: %v", err)
	}
	if err := svc.SetEmployeePIN(ctx, "default", "emp-1", "abc"); !errors.Is(err, domain.ErrInvalidPIN) {
		t.Fatalf("SetEmployeePIN with a malformed PIN = %v, want ErrInvalidPIN", err)
	}
}

// --- merchant relationship use cases ----------------------------------------

func seedAccount(t *testing.T, svc *application.Service, tenant, name, email, pin string) *domain.Employee {
	t.Helper()
	employee, err := svc.RegisterAccount(context.Background(), domain.Tenant(tenant), name, email, pin)
	if err != nil {
		t.Fatalf("RegisterAccount(%s): %v", email, err)
	}
	return employee
}

func loginSession(t *testing.T, svc *application.Service, tenant, email, pin string) string {
	t.Helper()
	session, _, err := svc.Login(context.Background(), domain.Tenant(tenant), email, pin)
	if err != nil {
		t.Fatalf("Login(%s): %v", email, err)
	}
	return session.ID
}

func TestOpenMerchantMakesTheEmployeePenjual(t *testing.T) {
	store := infrastructure.NewStore()
	svc := application.NewService(store, store, 0, time.Hour, store)
	ctx := context.Background()

	owner := seedAccount(t, svc, "default", "Penjual", "penjual@toko.com", "123456")
	session := loginSession(t, svc, "default", "penjual@toko.com", "123456")

	merchant, err := svc.OpenMerchant(ctx, session, "Toko Penjual")
	if err != nil {
		t.Fatalf("OpenMerchant: %v", err)
	}
	if merchant.OwnerEmployeeID != owner.ID {
		t.Fatalf("OwnerEmployeeID = %q, want %q", merchant.OwnerEmployeeID, owner.ID)
	}

	membership, err := store.GetMembership(ctx, "default", merchant.ID, owner.ID)
	if err != nil {
		t.Fatalf("owner membership: %v", err)
	}
	if !membership.IsOwner() {
		t.Fatalf("owner membership role = %q, want owner", membership.Role)
	}

	myMerchants, err := svc.ListMyMerchants(ctx, session)
	if err != nil {
		t.Fatalf("ListMyMerchants: %v", err)
	}
	if len(myMerchants) != 1 || myMerchants[0].ID != merchant.ID {
		t.Fatalf("ListMyMerchants = %d, want the one merchant", len(myMerchants))
	}
}

func TestOnlyOwnerMayManageProductInTheirMerchant(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	_ = seedAccount(t, svc, "default", "Penjual", "penjual@toko.com", "123456")
	ownerSession := loginSession(t, svc, "default", "penjual@toko.com", "123456")

	merchant, err := svc.OpenMerchant(ctx, ownerSession, "Toko Penjual")
	if err != nil {
		t.Fatalf("OpenMerchant: %v", err)
	}

	// The owner can manage products by default.
	allowed, err := svc.CanManageProduct(ctx, ownerSession, merchant.ID)
	if err != nil {
		t.Fatalf("CanManageProduct(owner): %v", err)
	}
	if !allowed {
		t.Fatal("owner should be able to manage products")
	}

	// A non-member cannot.
	_ = seedAccount(t, svc, "default", "Stranger", "stranger@toko.com", "123456")
	strangerSession := loginSession(t, svc, "default", "stranger@toko.com", "123456")
	allowed, err = svc.CanManageProduct(ctx, strangerSession, merchant.ID)
	if err != nil {
		t.Fatalf("CanManageProduct(stranger): %v", err)
	}
	if allowed {
		t.Fatal("stranger must not be able to manage products")
	}
}

func TestInviteAndAcceptMakesKaryawanWithoutProductAuthority(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	_ = seedAccount(t, svc, "default", "Penjual", "penjual@toko.com", "123456")
	ownerSession := loginSession(t, svc, "default", "penjual@toko.com", "123456")

	merchant, err := svc.OpenMerchant(ctx, ownerSession, "Toko Penjual")
	if err != nil {
		t.Fatalf("OpenMerchant: %v", err)
	}

	// A non-owner may not invite.
	_ = seedAccount(t, svc, "default", "Stranger", "stranger@toko.com", "123456")
	strangerSession := loginSession(t, svc, "default", "stranger@toko.com", "123456")
	if _, err := svc.InviteMember(ctx, strangerSession, merchant.ID, "karyawan@toko.com", domain.MerchantRoleEmployee); !errors.Is(err, domain.ErrMerchantPermissionDenied) {
		t.Fatalf("stranger invite = %v, want ErrMerchantPermissionDenied", err)
	}

	// The owner invites a karyawan.
	invitation, err := svc.InviteMember(ctx, ownerSession, merchant.ID, "karyawan@toko.com", domain.MerchantRoleEmployee)
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}

	// Only the invited account may accept (email must match).
	_ = seedAccount(t, svc, "default", "Other", "other@toko.com", "123456")
	wrongSession := loginSession(t, svc, "default", "other@toko.com", "123456")
	if _, err := svc.AcceptInvitation(ctx, wrongSession, invitation.Token); !errors.Is(err, domain.ErrInvalidMerchant) {
		t.Fatalf("accept by non-invited email = %v, want ErrInvalidMerchant", err)
	}

	karyawan := seedAccount(t, svc, "default", "Karyawan", "karyawan@toko.com", "123456")
	karyawanSession := loginSession(t, svc, "default", "karyawan@toko.com", "123456")

	membership, err := svc.AcceptInvitation(ctx, karyawanSession, invitation.Token)
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	if membership.IsOwner() {
		t.Fatal("karyawan membership must not be an owner")
	}

	// A karyawan cannot manage products without an owner grant.
	allowed, err := svc.CanManageProduct(ctx, karyawanSession, merchant.ID)
	if err != nil {
		t.Fatalf("CanManageProduct(karyawan): %v", err)
	}
	if allowed {
		t.Fatal("karyawan must not manage products before an owner grant")
	}

	// The owner grants product management; the karyawan can now manage.
	if err := svc.GrantMerchantPermission(ctx, ownerSession, merchant.ID, karyawan.ID, "seller.catalog.manage"); err != nil {
		t.Fatalf("GrantMerchantPermission: %v", err)
	}
	allowed, err = svc.CanManageProduct(ctx, karyawanSession, merchant.ID)
	if err != nil {
		t.Fatalf("CanManageProduct(granted): %v", err)
	}
	if !allowed {
		t.Fatal("karyawan with a grant must manage products")
	}

	// A non-owner may not grant.
	if err := svc.GrantMerchantPermission(ctx, strangerSession, merchant.ID, karyawan.ID, "seller.catalog.manage"); !errors.Is(err, domain.ErrMerchantPermissionDenied) {
		t.Fatalf("stranger grant = %v, want ErrMerchantPermissionDenied", err)
	}

	// Revoking the grant removes product authority.
	if err := svc.RevokeMerchantPermission(ctx, ownerSession, merchant.ID, karyawan.ID, "seller.catalog.manage"); err != nil {
		t.Fatalf("RevokeMerchantPermission: %v", err)
	}
	allowed, err = svc.CanManageProduct(ctx, karyawanSession, merchant.ID)
	if err != nil {
		t.Fatalf("CanManageProduct(revoked): %v", err)
	}
	if allowed {
		t.Fatal("karyawan with a revoked grant must not manage products")
	}
}

func TestInvitationExpires(t *testing.T) {
	store := infrastructure.NewStore()
	svc := application.NewService(store, store, 0, time.Hour, store)
	ctx := context.Background()

	_ = seedAccount(t, svc, "default", "Penjual", "penjual@toko.com", "123456")
	ownerSession := loginSession(t, svc, "default", "penjual@toko.com", "123456")
	merchant, err := svc.OpenMerchant(ctx, ownerSession, "Toko Penjual")
	if err != nil {
		t.Fatalf("OpenMerchant: %v", err)
	}

	invitation, err := svc.InviteMember(ctx, ownerSession, merchant.ID, "karyawan@toko.com", domain.MerchantRoleEmployee)
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	invitation.ExpiresAt = time.Now().Add(-time.Minute)
	if err := store.UpdateInvitation(ctx, invitation); err != nil {
		t.Fatalf("UpdateInvitation: %v", err)
	}

	_ = seedAccount(t, svc, "default", "Karyawan", "karyawan@toko.com", "123456")
	karyawanSession := loginSession(t, svc, "default", "karyawan@toko.com", "123456")
	if _, err := svc.AcceptInvitation(ctx, karyawanSession, invitation.Token); !errors.Is(err, domain.ErrInvitationExpired) {
		t.Fatalf("accept expired = %v, want ErrInvitationExpired", err)
	}
}
