// Package auth_test covers the login page's rendered contract. Sign-in is the
// one surface a reader cannot route around, and it is the surface the shell
// does not wrap: it renders its own full-height panel, so anything the other
// pages inherit from the layout has to be asserted here or not at all.
package ui_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/auth/ui"
	"gogo/internal/features/auth/ui/application/ports"
	"gogo/internal/features/auth/ui/application/results"
	"gogo/internal/shared/web/session"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeAuth is an in-memory Auth.
type fakeAuth struct {
	session *results.Session
	err     error

	gotEmail string
	gotPIN   string
}

var _ ports.Auth = (*fakeAuth)(nil)

func (f *fakeAuth) Login(_ context.Context, _, email, pin string) (*results.Session, error) {
	f.gotEmail, f.gotPIN = email, pin
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}

func (f *fakeAuth) Logout(context.Context, string) error { return nil }

func newRouter(t *testing.T, auth *fakeAuth) *chi.Mux {
	t.Helper()
	page := authpage(auth)
	router := chi.NewRouter()
	page.RegisterRoutes(router)
	return router
}

func authpage(f *fakeAuth) *ui.Page {
	return ui.New(f, discardLogger(), session.Policy{Secure: false})
}

func get(t *testing.T, router *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestLoginPageHasAHeadingAndLabelledFields pins the two things that make the
// page navigable at all. The heading was an <h2>, so a screen reader entered
// sign-in with no level-one heading; and a form whose fields are placeholders
// rather than labels announces only "text field".
func TestLoginPageHasAHeadingAndLabelledFields(t *testing.T) {
	t.Parallel()

	router := newRouter(t, &fakeAuth{})
	rec := get(t, router, "/login")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if strings.Count(body, "<h1") != 1 {
		t.Errorf("the page needs exactly one level-one heading, got %d", strings.Count(body, "<h1"))
	}
	if !strings.Contains(body, "<h1") || !strings.Contains(body, "Masuk") {
		t.Error("the heading should name the page")
	}
	for _, want := range []string{"Email", "PIN (6 digit)", `name="email"`, `name="pin"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login form missing %q", want)
		}
	}
	// The submit control must actually submit: the page's whole function is
	// posting credentials, and a button missing type=submit only works by
	// accident of HTML's default.
	if !strings.Contains(body, `type="submit"`) {
		t.Error("the sign-in control must be a submit button")
	}
	if !strings.Contains(body, `method="post"`) || !strings.Contains(body, `action="/login"`) {
		t.Error("the form must post to the login route")
	}
}

// TestLoginPageFillsTheViewport pins the mobile fix: min-h-screen left the
// panel a mobile-viewport-height shorter than the body around it, so on a phone
// the card sat below the fold and the reader saw an empty screen.
func TestLoginPageFillsTheViewport(t *testing.T) {
	t.Parallel()

	body := get(t, newRouter(t, &fakeAuth{}), "/login").Body.String()
	if !strings.Contains(body, "min-h-dvh") {
		t.Error("the panel should fill the dynamic viewport")
	}
	if strings.Contains(body, "min-h-screen") {
		t.Error("min-h-screen ignores the mobile browser's collapsing toolbar")
	}
}

// TestLoginFailureIsExplainedAndTheFormIsKept pins the failed-attempt contract:
// the reason is shown, the address is kept so the reader does not retype it, and
// the fields are marked invalid so the error is tied to the form rather than
// floating above it.
func TestLoginFailureIsExplainedAndTheFormIsKept(t *testing.T) {
	t.Parallel()

	router := newRouter(t, &fakeAuth{err: errors.New("PIN salah")})

	form := url.Values{"email": {"budi@toko.com"}, "pin": {"000000"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// The status reports the failure while the body is still the form: a
	// 200 would claim the sign-in worked, and a bare error page would make the
	// reader start over with no form to start over with.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PIN salah") {
		t.Error("the reason should be shown to the reader")
	}
	if !strings.Contains(body, `value="budi@toko.com"`) {
		t.Error("a failed attempt should not make the reader retype their email")
	}
	if !strings.Contains(body, `aria-invalid="true"`) {
		t.Error("the fields should be marked invalid alongside the error")
	}
	if !strings.Contains(body, `type="password"`) {
		t.Error("the PIN must not come back filled in after a failure")
	}
}

// TestLoginSuccessEstablishesTheSession pins the happy path end to end: the
// session cookie is written and the reader is sent somewhere rather than left
// on the form.
func TestLoginSuccessEstablishesTheSession(t *testing.T) {
	t.Parallel()

	now := time.Now()
	router := newRouter(t, &fakeAuth{session: &results.Session{
		ID:        "sess-1",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
		Employee:  results.Employee{ID: "emp-1", Name: "Budi Santoso", Email: "budi@toko.com", Role: "seller"},
	}})

	form := url.Values{"email": {"budi@toko.com"}, "pin": {"123456"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (post/redirect/get, so a reload cannot repost the PIN)", rec.Code)
	}
	var wrote bool
	for _, c := range rec.Result().Cookies() {
		if c.Name != "" && c.Value == "sess-1" {
			wrote = true
		}
	}
	if !wrote {
		t.Error("a successful login should establish the session cookie")
	}
}
