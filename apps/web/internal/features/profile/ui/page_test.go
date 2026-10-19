// Package profile_test covers the profile page contract: it serves a public
// account page that shows the signed-in employee when a valid session cookie is
// present and the signed-out state otherwise, and always links into the seller
// console. It consumes the auth module over the auth.v1 gRPC contract.
package ui_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	authmod "gogo/internal/features/auth"
	authdomain "gogo/internal/features/auth/domain"
	authinfra "gogo/internal/features/auth/infrastructure"
	"gogo/internal/features/profile/ui"
	profileauthclient "gogo/internal/features/profile/ui/infrastructure/authclient"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/web/navigation"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRouter wires the profile page over the auth module in-process, exactly as
// the gateway does. It seeds one employee and a live session, returning the
// router, the session cookie value, and the seeded employee's name.
func newRouter(t *testing.T) (*chi.Mux, string, string) {
	t.Helper()

	store := authinfra.NewStore()
	module := authmod.NewModule(store, store, store, nil, discardLogger())

	employee, err := authdomain.NewEmployee("default", "emp-1", "Budi Santoso", "budi@toko.com", "123456", authdomain.RoleSeller)
	if err != nil {
		t.Fatalf("NewEmployee: %v", err)
	}
	if err := store.Create(context.Background(), "default", employee); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	session, err := authdomain.NewSession("default", "sess-1", "emp-1", time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := store.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	conn, closeServer, err := grpcx.InProcess(module.RegisterGRPC)
	if err != nil {
		t.Fatalf("grpcx.InProcess: %v", err)
	}
	t.Cleanup(closeServer)

	page := ui.New(profileauthclient.New(authconsumer.New(conn, breaker.NewDefault())), discardLogger())
	router := chi.NewRouter()
	page.RegisterRoutes(router)
	return router, session.ID, employee.Name
}

func TestProfileSignedOut(t *testing.T) {
	t.Parallel()

	router, _, _ := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/profile", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<!doctype html>", "Belum masuk", "Masuk", `href="/login"`} {
		if !strings.Contains(body, want) {
			t.Errorf("signed-out profile missing %q", want)
		}
	}
	// The seller console is always reachable from the account page.
	if !strings.Contains(body, `href="/seller"`) {
		t.Error("profile page must link into the seller console")
	}
}

func TestProfileSignedIn(t *testing.T) {
	t.Parallel()

	router, session, name := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/profile", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: session})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{name, "budi@toko.com", "Seller", `href="/seller"`} {
		if !strings.Contains(body, want) {
			t.Errorf("signed-in profile missing %q", want)
		}
	}
	if strings.Contains(body, "Belum masuk") {
		t.Error("signed-in profile must not show the signed-out state")
	}
}

// TestNav pins the profile page's shell contribution: it owns the account
// destination, which keeps "Profile" in the mobile tab bar now that the seller
// console no longer contributes to the nav.
func TestNav(t *testing.T) {
	t.Parallel()

	nav := ui.New(nil, discardLogger()).Nav()
	if len(nav) != 1 {
		t.Fatalf("len(Nav) = %d, want 1: %+v", len(nav), nav)
	}
	if nav[0].Label != navigation.Profile.Label || nav[0].Path != navigation.Profile.Path {
		t.Errorf("Nav[0] = %+v, want navigation.Profile", nav[0])
	}
}

// TestSectionRoutes pins the account sidebar: each section is its own route
// rendering the console layout (a sidebar beside the section content), with the
// seller console reachable from the sidebar footer.
func TestSectionRoutes(t *testing.T) {
	t.Parallel()

	router, _, _ := newRouter(t)

	sections := []struct {
		path  string
		title string
	}{
		{"/profile", "Ringkasan"},
		{"/profile/pesanan", "Pesanan saya"},
		{"/profile/alamat", "Alamat saya"},
		{"/profile/pengaturan", "Pengaturan akun"},
	}
	for _, s := range sections {
		req := httptest.NewRequest(http.MethodGet, s.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", s.path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, s.title) {
			t.Errorf("%s missing section title %q", s.path, s.title)
		}
		// The console layout: a sidebar landmark beside the section.
		if !strings.Contains(body, "drawer") || !strings.Contains(body, `aria-label="Akun Saya"`) {
			t.Errorf("%s should render the account sidebar", s.path)
		}
		if !strings.Contains(body, `href="/seller"`) {
			t.Errorf("%s should keep the seller console link", s.path)
		}
	}
}

// TestOrdersSectionActive pins the sidebar's active rule: the orders route
// marks its own menu item current, not the overview.
func TestOrdersSectionActive(t *testing.T) {
	t.Parallel()

	router, _, _ := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/profile/pesanan", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="/profile/pesanan"`) {
		t.Error("orders route should render its sidebar entry")
	}
	if !strings.Contains(rec.Body.String(), "Pesanan saya") {
		t.Error("orders route should render the orders section")
	}
}
