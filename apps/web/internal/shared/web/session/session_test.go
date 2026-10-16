package session_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gogo/internal/shared/web/session"
)

func TestWriteGivesTheCookieTheSessionsOwnLifetime(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	rec := httptest.NewRecorder()

	session.Policy{}.Write(rec, "s-1", created, created.Add(8*time.Hour))

	cookie := onlyCookie(t, rec.Result())
	if got := cookie.MaxAge; got != int((8 * time.Hour).Seconds()) {
		t.Errorf("MaxAge = %d, want the session's own lifetime", got)
	}
	if !cookie.HttpOnly {
		t.Error("HttpOnly = false, want the cookie hidden from scripts")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want the whole site so every page sees the session", cookie.Path)
	}
}

func TestWriteMarksTheCookieSecureWhenTheDeploymentIs(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	created := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

	session.Policy{Secure: true}.Write(rec, "s-1", created, created.Add(time.Hour))

	if cookie := onlyCookie(t, rec.Result()); !cookie.Secure {
		t.Error("Secure = false, want the cookie restricted to HTTPS")
	}
}

func TestWriteClearsRatherThanHandingOutADeadCookie(t *testing.T) {
	t.Parallel()

	// A session whose window already closed must not produce a cookie that
	// looks valid: the browser would carry a session id the service will reject,
	// and every later request would pay for a round trip to learn that.
	created := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		id       string
		expires  time.Time
		wantGone bool
	}{
		{"empty session id", "", created.Add(time.Hour), true},
		{"window already closed", "s-1", created.Add(-time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			session.Policy{}.Write(rec, tc.id, created, tc.expires)

			cookie := onlyCookie(t, rec.Result())
			if cookie.MaxAge >= 0 {
				t.Errorf("MaxAge = %d, want a cleared cookie", cookie.MaxAge)
			}
			if cookie.Value != "" {
				t.Errorf("Value = %q, want empty", cookie.Value)
			}
		})
	}
}

func TestReadReturnsTheSessionID(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/profile", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "s-1"})

	if got := session.Read(req); got != "s-1" {
		t.Errorf("Read = %q, want %q", got, "s-1")
	}
}

func TestReadTreatsAnAbsentCookieAsSignedOut(t *testing.T) {
	t.Parallel()

	if got := session.Read(httptest.NewRequest(http.MethodGet, "/profile", nil)); got != "" {
		t.Errorf("Read = %q, want empty for a request with no session", got)
	}
}

// The whole point of the package: the writer and the reader must agree. If they
// ever disagreed on the name, a user would be signed in by one surface and
// rendered signed out by another, with no error raised anywhere.
func TestTheWriterAndTheReaderAgreeOnTheName(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	rec := httptest.NewRecorder()
	session.Policy{}.Write(rec, "s-1", created, created.Add(time.Hour))

	written := onlyCookie(t, rec.Result())

	req := httptest.NewRequest(http.MethodGet, "/profile", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	if written.Name != session.CookieName {
		t.Fatalf("wrote cookie %q, but the reader looks for %q", written.Name, session.CookieName)
	}
	if got := session.Read(req); got != "s-1" {
		t.Errorf("Read = %q, want the value the writer set", got)
	}
}

func onlyCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want exactly 1", len(cookies))
	}
	return cookies[0]
}
