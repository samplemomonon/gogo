// Package serviceauth_test pins the outgoing-credential contract: the header
// shape both transports read, the emptiness rule, and the gRPC adapter's
// deliberate insecure-transport decision.
package serviceauth_test

import (
	"context"
	"testing"

	"aggregator-service/internal/serviceauth"
)

// TestHeaderRendersTheBearerScheme pins the wire format. Both the HTTP
// round-tripper and the gRPC metadata adapter read this one method, so a
// changed scheme would be one failure instead of two.
func TestHeaderRendersTheBearerScheme(t *testing.T) {
	t.Parallel()

	if got := serviceauth.NewOutgoing("s3cret").Header(); got != "Bearer s3cret" {
		t.Errorf("Header() = %q, want %q", got, "Bearer s3cret")
	}
}

// TestTokenIsTrimmed pins that a token from environment configuration is
// usable despite surrounding whitespace, and that a whitespace-only value is
// empty rather than a credential made of spaces.
func TestTokenIsTrimmed(t *testing.T) {
	t.Parallel()

	if got := serviceauth.NewOutgoing("  s3cret\n").Header(); got != "Bearer s3cret" {
		t.Errorf("Header() = %q, want the trimmed %q", got, "Bearer s3cret")
	}

	blank := serviceauth.NewOutgoing("   ")
	if blank.Present() {
		t.Error("Present() reported a whitespace-only token as configured")
	}
	if got := blank.Header(); got != "" {
		t.Errorf("Header() = %q for an empty token, want no header at all", got)
	}
}

// TestPresentReportsConfiguration pins the flag callers gate on: it must be
// false for the unconfigured case, because a transport that silently proceeds
// without a credential turns a configuration mistake into a confusing
// Unauthenticated error at call time.
func TestPresentReportsConfiguration(t *testing.T) {
	t.Parallel()

	if !serviceauth.NewOutgoing("s3cret").Present() {
		t.Error("Present() = false for a configured token")
	}
	if serviceauth.NewOutgoing("").Present() {
		t.Error("Present() = true for an empty token")
	}
}

// TestGRPCMetadataCarriesTheSameHeader pins the adapter: the metadata entry
// wraps exactly the Header() value, and an unconfigured credential sends no
// entry rather than an empty one.
func TestGRPCMetadataCarriesTheSameHeader(t *testing.T) {
	t.Parallel()

	creds := serviceauth.NewOutgoing("s3cret").GRPCCredentials()

	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if got := md["authorization"]; got != "Bearer s3cret" {
		t.Errorf("authorization metadata = %q, want %q", got, "Bearer s3cret")
	}

	empty := serviceauth.NewOutgoing("").GRPCCredentials()
	md, err = empty.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata with no token: %v", err)
	}
	if len(md) != 0 {
		t.Errorf("metadata = %v, want none for an unconfigured credential", md)
	}
}

// TestRequireTransportSecurityIsDeliberatelyFalse pins the documented
// trade-off. gRPC refuses to attach per-call credentials over an insecure
// connection when this returns true, so flipping it would break every call to
// the platform services — which is exactly why it deserves a pinned value and
// not a default.
func TestRequireTransportSecurityIsDeliberatelyFalse(t *testing.T) {
	t.Parallel()

	creds := serviceauth.NewOutgoing("s3cret").GRPCCredentials()
	if creds.RequireTransportSecurity() {
		t.Error("RequireTransportSecurity() = true; over the current insecure connections gRPC would then refuse to send the credential")
	}
}
