// Package handlers_test covers the profile page's handler contract: a valid
// session renders the port's read model, anything else renders the signed-out
// state without a redirect, an unreachable auth service degrades to that same
// signed-out page (and is logged), and account data reaches the document only
// escaped.
package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/profile/ui/application/ports"
	"gogo/internal/features/profile/ui/application/results"
	"gogo/internal/features/profile/ui/handlers"
	"gogo/internal/shared/web/session"
)

// fakeAccounts is a port stub: it answers with the read model the test seeds
// and records every session ID the handler passes through.
type fakeAccounts struct {
	account *results.Account
	err     error

	sessions []string
}

var _ ports.Accounts = (*fakeAccounts)(nil)

func (f *fakeAccounts) Current(_ context.Context, sessionID string) (*results.Account, error) {
	f.sessions = append(f.sessions, sessionID)
	if f.err != nil {
		return nil, f.err
	}
	return f.account, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRouter mounts the handler at the same four routes page.RegisterRoutes
// registers, so SectionFor resolves the request path exactly as it does in the
// gateway.
func newRouter(accounts ports.Accounts, logger *slog.Logger) *chi.Mux {
	handler := handlers.NewHandler(accounts, logger)
	router := chi.NewRouter()
	router.Get("/profile", handler.Section)
	router.Get("/profile/pesanan", handler.Section)
	router.Get("/profile/alamat", handler.Section)
	router.Get("/profile/pengaturan", handler.Section)
	return router
}

// get serves one request, attaching the session cookie only when cookie is
// non-empty. The cookie name comes from the session package, so a rename there
// is what makes this request signed out — the same way it would in the gateway.
func get(t *testing.T, router http.Handler, path, cookie string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestHandlerRendersTheSignedInAccount pins the happy path: the cookie's value
// is the session the port is asked about, and the read model it answers with is
// what the document renders.
func TestHandlerRendersTheSignedInAccount(t *testing.T) {
	t.Parallel()

	fake := &fakeAccounts{account: &results.Account{
		ID: "emp-1", Name: "Budi Santoso", Email: "budi@toko.com", Role: "seller",
	}}

	rec := get(t, newRouter(fake, discardLogger()), "/profile", "sess-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"Budi Santoso", "budi@toko.com", "Seller", `href="/profile/pesanan"`} {
		if !strings.Contains(body, want) {
			t.Errorf("signed-in profile missing %q", want)
		}
	}
	if len(fake.sessions) != 1 || fake.sessions[0] != "sess-1" {
		t.Errorf("port was asked about sessions %v, want [sess-1] from the cookie", fake.sessions)
	}
}

// TestHandlerShowsTheSignedOutStateWithoutASession pins the anonymous visit:
// the handler still calls the port (with the empty session), and the port's
// nil answer renders the sign-in prompt instead of failing.
func TestHandlerShowsTheSignedOutStateWithoutASession(t *testing.T) {
	t.Parallel()

	fake := &fakeAccounts{}

	rec := get(t, newRouter(fake, discardLogger()), "/profile", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a visitor without a session", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Belum masuk") || !strings.Contains(body, `href="/login"`) {
		t.Error("a visitor without a session should get the signed-out state with a sign-in link")
	}
	if len(fake.sessions) != 1 || fake.sessions[0] != "" {
		t.Errorf("port was asked about sessions %v, want one empty session", fake.sessions)
	}
}

// TestHandlerTreatsAnUnknownSessionAsSignedOut pins the port's refusal answer
// (nil account, nil error): the handler must render the signed-out page — not
// a redirect, and not an error page.
func TestHandlerTreatsAnUnknownSessionAsSignedOut(t *testing.T) {
	t.Parallel()

	fake := &fakeAccounts{}

	rec := get(t, newRouter(fake, discardLogger()), "/profile", "revoked-session")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an unknown session", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "" {
		t.Errorf("unknown session redirected to %q, want the signed-out page rendered in place", location)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Belum masuk") {
		t.Error("an unknown session should render the signed-out state")
	}
	if len(fake.sessions) != 1 || fake.sessions[0] != "revoked-session" {
		t.Errorf("port was asked about sessions %v, want [revoked-session]", fake.sessions)
	}
}

// TestHandlerDegradesWhenAuthIsUnreachable pins the outage contract: the read
// fails, so the page still renders the signed-out state — keeping the seller
// console link reachable — and the degraded read is logged with its cause.
func TestHandlerDegradesWhenAuthIsUnreachable(t *testing.T) {
	t.Parallel()

	const cause = "auth: unavailable: validate session: connection refused"
	fake := &fakeAccounts{err: errors.New(cause)}

	var logs bytes.Buffer
	rec := get(t, newRouter(fake, slog.New(slog.NewTextHandler(&logs, nil))), "/profile", "sess-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an outage still renders a page", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Belum masuk") {
		t.Error("an unreachable auth service should render the signed-out state")
	}
	if !strings.Contains(body, `href="/seller"`) {
		t.Error("an outage must not strand the seller console link")
	}
	for _, want := range []string{"profile account lookup failed", cause} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log output missing %q; the degraded read must be recorded:\n%s", want, logs.String())
		}
	}
}

// TestHandlerEscapesAccountData pins the escaping boundary: account fields are
// untrusted text from a remote service, so hostile markup must arrive as text,
// never as document structure.
func TestHandlerEscapesAccountData(t *testing.T) {
	t.Parallel()

	fake := &fakeAccounts{account: &results.Account{
		ID:    "emp-1",
		Name:  `Budi <script>alert(1)</script>`,
		Email: `budi@toko.com"><img src=x onerror=alert(1)>`,
		Role:  "seller",
	}}

	rec := get(t, newRouter(fake, discardLogger()), "/profile", "sess-1")
	body := rec.Body.String()

	for _, raw := range []string{"<script>alert(1)</script>", "<img src=x"} {
		if strings.Contains(body, raw) {
			t.Errorf("hostile account data reached the document unescaped: %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;", "&lt;img src=x"} {
		if !strings.Contains(body, escaped) {
			t.Errorf("document missing the escaped account text %q", escaped)
		}
	}
}
