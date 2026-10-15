// Package session owns the browser session cookie: its name, its attributes, and
// the two operations every surface performs on it — writing one after a
// successful sign-in and clearing one on sign-out.
//
// It exists because the cookie was previously written by hand in each handler
// that needed it. The login page set it with one attribute set and the account
// page read it by a hard-coded name, so the two could disagree: a rename that
// reached only one of them produced a page that signed a user in and then
// rendered them as signed out, with no error anywhere. One name, one attribute
// set, and one read path removes that class of bug rather than documenting it.
package session

import (
	"net/http"
	"time"
)

// CookieName is the single definition of the session cookie's name. Any surface
// that reads or clears the session must use it rather than repeating the
// literal.
const CookieName = "session_id"

// Policy carries the deployment-dependent half of the cookie's attributes, which
// the composition root decides and the handlers cannot know: whether the
// deployment terminates TLS. It is passed in rather than read from the
// environment so this package stays free of configuration and remains a pure
// HTTP concern.
type Policy struct {
	// Secure marks the cookie Secure, so the browser sends it only over HTTPS.
	// It must be true wherever the app is served over TLS; leaving it false in
	// production is what turns a stolen session into a replayable credential.
	Secure bool
}

// Write sets the session cookie for a session that runs from createdAt until
// expiresAt, giving the browser exactly the session's own lifetime: the cookie
// dies when the session does, rather than outliving it as a stale credential or
// expiring early and signing the user out mid-visit.
//
// A session whose window has already closed is cleared instead of written, so a
// response never hands the browser a cookie that is already dead.
func (p Policy) Write(w http.ResponseWriter, id string, createdAt, expiresAt time.Time) {
	lifetime := int(expiresAt.Sub(createdAt).Seconds())
	if id == "" || lifetime <= 0 {
		p.Clear(w)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   p.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   lifetime,
	})
}

// Clear expires the session cookie. It is idempotent: clearing an absent cookie
// is how sign-out is expressed to a browser that never had one.
func (p Policy) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   p.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Read returns the session ID carried by the request, or "" when the request
// carries none. A missing cookie is an ordinary signed-out state, not an error,
// so callers get one value to branch on instead of a nil check per call site.
func Read(r *http.Request) string {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}
