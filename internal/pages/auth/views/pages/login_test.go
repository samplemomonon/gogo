// Package pages_test covers the login view's rendered contract, the component
// the handler renders for every answer: the form the reader posts, the alert
// that appears only on a failure, and the escaping of the values the view
// interpolates. The view is where hostile input would become markup, so that
// is asserted here rather than left to a handler integration test.
package pages_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gogo/internal/pages/auth/views/pages"
)

func render(t *testing.T, email, errMsg string) string {
	t.Helper()

	var buf bytes.Buffer
	if err := pages.Login(email, errMsg).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Login.Render: %v", err)
	}
	return buf.String()
}

// TestLoginRendersTheForm pins the controls the page's whole function depends
// on: a POST to /login with an email field, a PIN field and a real submit
// control. A pristine page carries no alert and marks no field invalid.
func TestLoginRendersTheForm(t *testing.T) {
	t.Parallel()

	body := render(t, "", "")
	for _, want := range []string{
		"Masuk",
		`action="/login"`,
		`method="post"`,
		`name="email"`,
		`name="pin"`,
		`type="password"`,
		`type="submit"`,
		"Email",
		"PIN (6 digit)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login form missing %q", want)
		}
	}
	if strings.Contains(body, `role="alert"`) {
		t.Error("a pristine login view rendered an alert")
	}
	if strings.Contains(body, `aria-invalid="true"`) {
		t.Error("a pristine login view marked a field invalid")
	}
}

// TestLoginRendersTheErrorMessageWhenGiven pins the failure rendering: the
// message is visible, and both fields are marked invalid so the error is tied
// to the form rather than floating above it. The PIN is never rendered back.
func TestLoginRendersTheErrorMessageWhenGiven(t *testing.T) {
	t.Parallel()

	body := render(t, "budi@toko.com", "PIN yang Anda masukkan salah")
	if !strings.Contains(body, `role="alert"`) {
		t.Error("a failed sign-in must render an alert")
	}
	if !strings.Contains(body, "PIN yang Anda masukkan salah") {
		t.Error("the failure reason must be visible")
	}
	if got := strings.Count(body, `aria-invalid="true"`); got != 2 {
		t.Errorf("aria-invalid appears %d times, want both fields marked", got)
	}
	if !strings.Contains(body, `value="budi@toko.com"`) {
		t.Error("a failed attempt should keep the email so the reader does not retype it")
	}
	// The PIN field is a password input and carries no value: a failed PIN
	// must not come back filled in.
	if strings.Contains(body, `name="pin" value=`) {
		t.Error("the PIN was rendered back into the form")
	}
}

// TestLoginEscapesHostileInput pins the view's escaping contract: both the
// email value and the error message are attacker-influenced (the email comes
// from the form, the message from the upstream service), and neither may become
// markup.
func TestLoginEscapesHostileInput(t *testing.T) {
	t.Parallel()

	body := render(t, `"><script>alert(1)</script>`, `<img src=x onerror=alert(1)>`)

	for _, forbidden := range []string{"<script", "<img"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("hostile input produced raw markup %q", forbidden)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;img src=x onerror=alert(1)&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("escaped input missing %q", want)
		}
	}
}
