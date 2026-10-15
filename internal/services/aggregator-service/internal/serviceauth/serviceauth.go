// Package serviceauth presents this service's credential to the platform
// services it calls.
//
// # Why it exists
//
// The platform services refuse unauthenticated callers. That makes the
// aggregator's own authentication meaningful — "one point closes all APIs" is
// only true if the APIs behind it are actually closed — and it means this service
// has to introduce itself.
//
// # What this credential does and does not protect against
//
// It protects against *reachability*. A caller who can reach a service's port but
// did not come through the composition layer is refused, which is the actual
// failure this closes: the platform services listen on host ports in development,
// and an unauthenticated POST /api/v1/products writes to the catalogue.
//
// It does **not** protect against an on-path attacker. The connection to the
// platform services uses insecure transport credentials, so this bearer token
// travels in the clear. A network observer who can read the segment can read the
// token and replay it.
//
// Closing that requires TLS, and on a service mesh, mTLS so that the peer is
// authenticated by certificate rather than by a shared secret. That is a separate
// decision from this one and is not taken here; what is taken here is the
// admission control, which is what the host port makes necessary today.
//
// The token is attached per call rather than per connection so that it cannot be
// captured from a long-lived stream, and it is never logged: see OutgoingHeader.
package serviceauth

import (
	"context"
	"strings"

	"google.golang.org/grpc/credentials"
)

// bearerPrefix is the scheme both transports use.
const bearerPrefix = "Bearer "

// Outgoing is the credential this service presents to its peers.
type Outgoing struct {
	token string
}

// NewOutgoing wraps the configured token.
func NewOutgoing(token string) Outgoing {
	return Outgoing{token: strings.TrimSpace(token)}
}

// Header returns the value for an Authorization header or gRPC authorization
// metadata entry.
func (o Outgoing) Header() string {
	if o.token == "" {
		return ""
	}
	return bearerPrefix + o.token
}

// Present reports whether a credential is configured.
//
// A transport must not silently proceed when it is not: sending no credential to
// a service that requires one produces an Unauthenticated error that looks like a
// wiring problem, when it is a configuration one.
func (o Outgoing) Present() bool { return o.token != "" }

// perRPCCredentials adapts Outgoing to gRPC's per-call credential interface.
type perRPCCredentials struct{ outgoing Outgoing }

// GRPCCredentials returns the gRPC adapter.
func (o Outgoing) GRPCCredentials() credentials.PerRPCCredentials {
	return perRPCCredentials{outgoing: o}
}

// GetRequestMetadata attaches the credential to an outgoing call.
func (c perRPCCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	header := c.outgoing.Header()
	if header == "" {
		return map[string]string{}, nil
	}
	return map[string]string{"authorization": header}, nil
}

// RequireTransportSecurity reports false.
//
// gRPC refuses to attach per-call credentials over an insecure connection unless
// this returns false, because its default assumes the bearer would otherwise
// travel in the clear. Returning false is a deliberate acceptance of that, for
// the reason in the package comment: these connections are insecure today, and
// the alternative is a service that cannot call anything at all. It is the one
// place in this repository where a credential is knowingly sent unprotected, and
// it is why the package comment says what it says.
func (c perRPCCredentials) RequireTransportSecurity() bool { return false }
