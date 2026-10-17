// Command authseed seeds the developer environment with the two dummy
// identities the role model demo needs:
//
//	pembeli@toko.com  PIN 123456  → a pembeli (buyer), no merchant
//	penjual@toko.com  PIN 123456  → a pembeli AND penjual, owns "Toko Bunda"
//
// It is idempotent: re-running it only creates what is missing. It targets the
// same Postgres the gateway uses (DATABASE_URL, falling back to
// AUTHZ_DATABASE_URL) and applies the auth migrations first.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"gogo/internal/modules/auth/application"
	"gogo/internal/modules/auth/domain"
	authpostgres "gogo/internal/modules/auth/infrastructure/postgres"
)

func main() {
	tenant := flag.String("tenant", "default", "tenant identifier to seed")
	merchantName := flag.String("merchant", "Toko Bunda", "merchant the penjual user owns")
	flag.Parse()

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("AUTHZ_DATABASE_URL"))
	}
	if dsn == "" {
		fatal("DATABASE_URL (or AUTHZ_DATABASE_URL) is required to seed the shared Postgres store")
	}

	// This command provisions a known-credential account and a policy
	// administrator against the database it is pointed at. Refusing a
	// production environment is the same posture cmd/authzbootstrap takes, and
	// it is here because a seed has no approval ceremony to slow it down.
	if appEnv := strings.TrimSpace(os.Getenv("APP_ENV")); appEnv == "production" {
		fatal("refusing to seed with APP_ENV=production: this creates a known-PIN account and a policy administrator")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := authpostgres.Connect(ctx, dsn, 1, 1)
	if err != nil {
		fatal("connect to database: %v", err)
	}
	defer pool.Close()
	if err := authpostgres.Migrate(ctx, pool); err != nil {
		fatal("apply auth migrations: %v", err)
	}

	store := authpostgres.NewMerchantStore(pool)
	authorizationStore := authpostgres.NewAuthorizationRepository(pool)
	svc := application.NewService(
		authpostgres.NewEmployeeStore(pool),
		authpostgres.NewSessionStore(pool),
		bcrypt.MinCost,
		24*time.Hour,
		store,
		authorizationStore,
	)

	t := domain.Tenant(*tenant)

	// 1. The pembeli account.
	buyer, err := ensureAccount(ctx, svc, t, "Pembeli", "pembeli@toko.com", "123456")
	if err != nil {
		fatal("seed pembeli: %v", err)
	}

	// 2. The penjual account, which also owns a merchant.
	penjual, err := ensureAccount(ctx, svc, t, "Penjual", "penjual@toko.com", "123456")
	if err != nil {
		fatal("seed penjual: %v", err)
	}

	sessionID, err := login(ctx, svc, t, penjual.Email, "123456")
	if err != nil {
		fatal("login penjual for merchant open: %v", err)
	}

	merchantID, err := ensureMerchant(ctx, svc, store, t, penjual.ID, sessionID, *merchantName)
	if err != nil {
		fatal("ensure merchant: %v", err)
	}

	// 3. The seller console is gated by the tenant RBAC catalog as well as the
	// merchant PDP, so the demo penjual must hold the seller capabilities. This
	// is the development equivalent of the production policy-administrator
	// ceremony, made idempotent.
	if err := ensureSellerGrants(ctx, pool, string(t), penjual.ID, *merchantName); err != nil {
		fatal("grant seller capabilities: %v", err)
	}

	fmt.Println("seeded identities:")
	fmt.Printf("  %-26s PIN %-6s roles=%s\n", buyer.Email, "123456", roleNames(domain.ReservedRoles(false, 0)))
	fmt.Printf("  %-26s PIN %-6s roles=%s\n", penjual.Email, "123456", roleNames(domain.ReservedRoles(merchantID != "", 0)))
	fmt.Printf("merchant: %s (owner %s, id %s)\n", *merchantName, penjual.Email, merchantID)
}

// ensureAccount creates a pembeli account when the email is not registered yet.
func ensureAccount(ctx context.Context, svc *application.Service, tenant domain.Tenant, name, email, pin string) (*domain.Employee, error) {
	existing, err := svc.GetEmployeeByEmail(ctx, tenant, email)
	if err == nil && existing != nil {
		return existing, nil
	}
	if !errors.Is(err, domain.ErrEmployeeNotFound) {
		return nil, err
	}
	return svc.RegisterAccount(ctx, tenant, name, email, pin)
}

// login returns a live session for the employee.
func login(ctx context.Context, svc *application.Service, tenant domain.Tenant, email, pin string) (string, error) {
	session, _, err := svc.Login(ctx, tenant, email, pin)
	if err != nil {
		return "", err
	}
	return session.ID, nil
}

// ensureMerchant opens the merchant for the penjual when they own none yet.
func ensureMerchant(ctx context.Context, svc *application.Service, store *authpostgres.MerchantStore, tenant domain.Tenant, employeeID, sessionID, name string) (string, error) {
	merchants, err := store.ListMerchantsForEmployee(ctx, tenant, employeeID)
	if err != nil {
		return "", err
	}
	for _, m := range merchants {
		if m.OwnerEmployeeID == employeeID {
			return m.ID, nil
		}
	}
	merchant, err := svc.OpenMerchant(ctx, sessionID, name)
	if err != nil {
		return "", err
	}
	return merchant.ID, nil
}

func roleNames(roles []domain.ReservedRole) string {
	names := make([]string, 0, len(roles))
	for _, role := range roles {
		names = append(names, string(role))
	}
	return strings.Join(names, ", ")
}

// seedSellerRoleID is the tenant role the seed provisions for the console. It is
// a fixed id so re-running the seed finds the same role instead of creating a
// second one (roles are unique by tenant + name).
const seedSellerRoleID = "seed:seller"

// seedSellerPermissions is the seller-console capability catalog from the auth
// module's own migration. It is spelled out here rather than read from the
// database so the seed fails loudly if the catalog is renamed.
var seedSellerPermissions = []string{
	"seller.dashboard.read",
	"seller.catalog.read",
	"seller.catalog.manage",
	"seller.category.read",
	"seller.category.manage",
	"seller.inventory.read",
	"seller.inventory.manage",
	"seller.sales.read",
	"seller.sales.checkout",
	"seller.sales.void",
}

// ensureSellerGrants gives the demo Penjual the tenant RBAC capabilities the
// seller console requires, in addition to owning its merchant.
//
// This is a development convenience, not a production ceremony. The runtime is
// default-deny: a role name grants nothing, and the production equivalent is a
// reviewed policy-administrator bootstrap (see cmd/authzbootstrap and OD-5 in
// docs/OPEN_DECISIONS.md). Here the seller IS the bootstrap identity, so this
// function bootstraps a policy admin first and then grants through it.
//
// Idempotent by construction: BootstrapPolicyAdmin refuses a second bootstrap,
// and every grant/assignment that already exists is reported as
// ErrPolicyConflict, which is treated as success.
func ensureSellerGrants(ctx context.Context, pool *pgxpool.Pool, tenantID, actorEmployeeID, roleName string) error {
	const changeReference = "authseed:development"

	if err := authpostgres.BootstrapPolicyAdmin(ctx, pool, tenantID, actorEmployeeID, changeReference); err != nil &&
		!errors.Is(err, authpostgres.ErrPolicyAdminAlreadyBootstrapped) {
		return fmt.Errorf("bootstrap policy administrator: %w", err)
	}

	authz := authpostgres.NewAuthorizationRepository(pool)

	if err := authz.CreateRole(ctx, tenantID, actorEmployeeID, seedSellerRoleID, roleName,
		"Development seed: seller console capabilities", changeReference); err != nil &&
		!errors.Is(err, domain.ErrPolicyConflict) {
		return fmt.Errorf("create seller role: %w", err)
	}

	for _, permissionKey := range seedSellerPermissions {
		if _, err := authz.GrantPermission(ctx, tenantID, actorEmployeeID, seedSellerRoleID,
			permissionKey, nil, "development seed", changeReference); err != nil &&
			!errors.Is(err, domain.ErrPolicyConflict) {
			return fmt.Errorf("grant %s: %w", permissionKey, err)
		}
	}

	if _, err := authz.AssignRole(ctx, tenantID, actorEmployeeID, actorEmployeeID, seedSellerRoleID,
		nil, nil, "development seed", changeReference); err != nil &&
		!errors.Is(err, domain.ErrPolicyConflict) {
		return fmt.Errorf("assign seller role: %w", err)
	}
	return nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "authseed: "+format+"\n", args...)
	os.Exit(1)
}
