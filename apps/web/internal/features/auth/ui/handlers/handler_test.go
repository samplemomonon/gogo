// Package handlers_test covers the login page's HTTP contract: what the
// handler does with a valid sign-in, a refused one, an outage, and a logout.
// The handler owns the session cookie it writes, the status each failure
// answers with, and the tenant it resolves from the request context. It is
// tested through the page's real route table (auth.New + RegisterRoutes) with
// a fake ports.Auth, so no wire and no auth service are involved.
package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authpage "gogo/internal/features/auth/ui"
	"gogo/internal/features/auth/ui/application/ports"
	"gogo/internal/features/auth/ui/application/results"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/session"
)

// fakeAuth is an in-memory ports.Auth that records what the handler handed it.
type fakeAuth struct {
	loginResult *results.Session
	loginErr    error
	logoutErr   error

	logins    int
	gotTenant string
	gotEmail  string
	gotPIN    string
	logouts   []string
}

var _ ports.Auth = (*fakeAuth)(nil)

func (f *fakeAuth) Login(_ context.Context, tenantID, email, pin string) (*results.Session, error) {
	f.logins++
	f.gotTenant, f.gotEmail, f.gotPIN = tenantID, email, pin
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	return f.loginResult, nil
}

func (f *fakeAuth) Logout(_ context.Context, sessionID string) error {
	f.logouts = append(f.logouts, sessionID)
	return f.logoutErr
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRouter wires the page over the fake the way the composition root wires it
// over the remote adapter: the page's own route table, built by production code.
func newRouter(t *testing.T, auth *fakeAuth) *chi.Mux {
	t.Helper()

	page := authpage.New(auth, discardLogger(), session.Policy{Secure: false})
	router := chi.NewRouter()
	page.RegisterRoutes(router)
	return router
}

// signedInSession is a session the cookie policy can actually write: its
// window is open, so Write does not treat it as already expired.
func signedInSession() *results.Session {
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	return &results.Session{
		ID:        "sess-1",
		CreatedAt: created,
		ExpiresAt: created.Add(8 * time.Hour),
		Employee:  results.Employee{ID: "emp-1", Name: "Budi Santoso", Email: "budi@toko.com", Role: "seller"},
	}
}

func postLogin(t *testing.T, router http.Handler, form url.Values, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// refusal manufactures the error the real adapter produces for a refused
// sign-in: an error that matches a sentinel and still carries the service's own
// client-facing reason (grpcconsumer.businessError). The port doc makes that
// reason the handler's input, so the test has to produce a real one.
func refusal(t *testing.T, detail string) error {
	t.Helper()

	caller := grpcconsumer.New(breaker.NewDefault(), grpcconsumer.Vocabulary{
		Unauthenticated: authconsumer.ErrUnauthenticated,
		Unavailable:     authconsumer.ErrAuthUnavailable,
	})
	_, err := grpcconsumer.Do(caller, context.Background(), "log in", func(context.Context) (struct{}, error) {
		return struct{}{}, status.Error(codes.Unauthenticated, detail)
	})
	if err == nil {
		t.Fatal("manufacturing a refusal returned nil")
	}
	return err
}

func TestLoginGETRendersAFormWithoutAnError(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{}
	rec := httptest.NewRecorder()
	newRouter(t, auth).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	body := rec.Body.String()
	for _, want := range []string{`action="/login"`, `method="post"`, `name="email"`, `name="pin"`, `type="submit"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login form missing %q", want)
		}
	}
	// ui.Alert renders nothing for an empty message, so a fresh page carries
	// no alert at all.
	if strings.Contains(body, `role="alert"`) {
		t.Error("a pristine login page rendered an error alert")
	}
	if auth.logins != 0 {
		t.Errorf("rendering the form called the auth port %d times, want 0", auth.logins)
	}
}

// A submission missing either credential is rejected before the port is
// consulted: there is nothing to authenticate and no reason to spend a remote
// call on it.
func TestLoginPOSTRequiresBothFields(t *testing.T) {
	t.Parallel()

	for name, form := range map[string]url.Values{
		"missing pin":   {"email": {"budi@toko.com"}},
		"missing email": {"pin": {"123456"}},
	} {
		form := form
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuth{}
			rec := postLogin(t, newRouter(t, auth), form, nil)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Email dan PIN wajib diisi") {
				t.Error("a blank credential should be explained to the reader")
			}
			if auth.logins != 0 {
				t.Errorf("a blank credential reached the auth port %d times, want 0", auth.logins)
			}
			if got := rec.Result().Cookies(); len(got) != 0 {
				t.Errorf("a rejected submission wrote cookies: %v", got)
			}
		})
	}
}

// A refusal is the service working correctly: its reason is shown, the answer
// is 401, and no session is established. A refused attempt must never look like
// a successful one to the browser.
func TestLoginPOSTRefusalShowsTheServicesReason(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{loginErr: refusal(t, "PIN yang Anda masukkan salah")}
	form := url.Values{"email": {"budi@toko.com"}, "pin": {"000000"}}
	rec := postLogin(t, newRouter(t, auth), form, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PIN yang Anda masukkan salah") {
		t.Error("the refusal's reason must be visible to the reader")
	}
	if !strings.Contains(body, `value="budi@toko.com"`) {
		t.Error("a refused attempt should keep the email so the reader does not retype it")
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("a refused sign-in wrote a cookie: %q", got)
	}
}

// A refusal that arrives without a client-facing detail still has to explain
// itself: the fallback names the likely cause without leaking transport text.
func TestLoginPOSTRefusalWithoutADetailFallsBackToTheGenericReason(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{loginErr: errors.New("boom")}
	form := url.Values{"email": {"budi@toko.com"}, "pin": {"000000"}}
	rec := postLogin(t, newRouter(t, auth), form, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Email atau PIN salah.") {
		t.Error("a refusal without a detail should show the generic reason")
	}
}

// An outage is not a refusal: the reader is told sign-in is unavailable (503),
// and the transport's own text stays in the log rather than on the page.
func TestLoginPOSTOutageAnswers503WithoutTheTransportText(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{loginErr: fmt.Errorf("%w: log in: %w", authconsumer.ErrAuthUnavailable, errors.New("connection refused"))}
	form := url.Values{"email": {"budi@toko.com"}, "pin": {"123456"}}
	rec := postLogin(t, newRouter(t, auth), form, nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Masuk sedang tidak tersedia. Silakan coba lagi.") {
		t.Error("an outage should tell the reader to try again")
	}
	if strings.Contains(body, "connection refused") {
		t.Error("the transport's own text belongs in the log, not on the page")
	}
}

// A successful sign-in establishes the session cookie the rest of the app reads
// (session.Policy owns its attributes) and redirects with 303 so a reload cannot
// repost the PIN.
func TestLoginPOSTSuccessWritesTheSessionCookieAndRedirects(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{loginResult: signedInSession()}
	form := url.Values{"email": {"budi@toko.com"}, "pin": {"123456"}}
	rec := postLogin(t, newRouter(t, auth), form, nil)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/seller" {
		t.Errorf("Location = %q, want /seller", got)
	}
	if auth.gotEmail != "budi@toko.com" || auth.gotPIN != "123456" {
		t.Errorf("login credentials = (%q, %q), want the submitted form values", auth.gotEmail, auth.gotPIN)
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == session.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("no %s cookie was written", session.CookieName)
	}
	if cookie.Value != "sess-1" {
		t.Errorf("cookie value = %q, want the session id", cookie.Value)
	}
	if cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie attributes = %+v, want the session policy's attributes", cookie)
	}
	if want := int((8 * time.Hour).Seconds()); cookie.MaxAge != want {
		t.Errorf("cookie Max-Age = %d, want %d (the session's own lifetime)", cookie.MaxAge, want)
	}
	if strings.Contains(rec.Body.String(), "<form") {
		t.Error("a successful redirect should not keep rendering the form")
	}
}

// The handler owns resolving the tenant from the request context, exactly the
// way the HTTP middleware set it. A request without one resolves to the default
// tenant, never to an empty string.
func TestLoginPOSTPassesTheResolvedTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "resolved tenant", ctx: tenant.WithID(context.Background(), "acme"), want: "acme"},
		{name: "no tenant falls back to default", ctx: context.Background(), want: "default"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeAuth{loginResult: signedInSession()}
			form := url.Values{"email": {"budi@toko.com"}, "pin": {"123456"}}
			postLogin(t, newRouter(t, auth), form, tt.ctx)

			if auth.gotTenant != tt.want {
				t.Errorf("tenant passed to the port = %q, want %q", auth.gotTenant, tt.want)
			}
		})
	}
}

// Logout revokes the session it was given, clears the cookie whatever the
// revocation answers, and sends the reader back to sign-in.
func TestLogoutRevokesClearsAndRedirects(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{}
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "sess-1"})
	rec := httptest.NewRecorder()
	newRouter(t, auth).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want /login", got)
	}
	if len(auth.logouts) != 1 || auth.logouts[0] != "sess-1" {
		t.Errorf("revoked sessions = %v, want [sess-1]", auth.logouts)
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == session.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("logout did not clear the %s cookie", session.CookieName)
	}
	if cookie.Value != "" || cookie.MaxAge >= 0 {
		t.Errorf("cleared cookie = %+v, want an empty value and a negative Max-Age", cookie)
	}
}

// Signing out with no session is ordinary: nothing to revoke, but the cookie
// is still cleared and the reader is still sent to sign-in.
func TestLogoutWithoutASessionStillClearsAndRedirects(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{}
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	newRouter(t, auth).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(auth.logouts) != 0 {
		t.Errorf("logout revoked %v without a session cookie, want none", auth.logouts)
	}
	if got := rec.Header().Get("Set-Cookie"); !strings.Contains(got, session.CookieName+"=") {
		t.Errorf("Set-Cookie = %q, want the session cookie cleared", got)
	}
}

// A revocation that fails must not strand the reader inside the session they
// asked to leave: the failure is logged and the cookie is cleared anyway.
func TestLogoutClearsTheCookieWhenRevocationFails(t *testing.T) {
	t.Parallel()

	auth := &fakeAuth{logoutErr: errors.New("auth service down")}
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "sess-1"})
	rec := httptest.NewRecorder()
	newRouter(t, auth).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 despite the revocation failure", rec.Code)
	}
	if got := rec.Header().Get("Set-Cookie"); !strings.Contains(got, session.CookieName+"=") {
		t.Errorf("Set-Cookie = %q, want the session cookie cleared anyway", got)
	}
}

// Logout is a mutation and is declared as POST only; a GET must not be able to
// end a session through a link or a prefetch.
func TestLogoutIsPostOnly(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newRouter(t, &fakeAuth{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logout", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /logout = %d, want 405", rec.Code)
	}
}
