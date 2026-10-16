// Package netutil holds small networking helpers shared by the config loader
// and the HTTP middleware, so the trusted-proxy rules live in exactly one
// place: config validates what it parses, and the middleware trusts what it
// resolves, against the same definition.
package netutil

import (
	"fmt"
	"net/netip"
	"strings"
)

// trustAllToken is the sentinel that trusts every peer.
const trustAllToken = "all"

// TrustedProxies is a parsed set of trusted-proxy CIDR blocks (plus the
// "all" wildcard). It is built once from the raw configuration values and
// then queried per request, so parsing never happens on the hot path.
type TrustedProxies struct {
	all  bool
	nets []netip.Prefix
}

// ParseTrustedProxies parses raw proxy entries. A malformed CIDR is an error
// (callers should surface it at startup); empty entries are ignored. The
// "all" token trusts every peer.
func ParseTrustedProxies(raw []string) (TrustedProxies, error) {
	var proxies TrustedProxies
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		switch entry {
		case "":
			continue
		case trustAllToken:
			proxies.all = true
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return TrustedProxies{}, fmt.Errorf("invalid CIDR %q: %w", entry, err)
		}
		proxies.nets = append(proxies.nets, prefix)
	}
	return proxies, nil
}

// Contains reports whether addr is a trusted proxy. An invalid address is
// never trusted.
func (p TrustedProxies) Contains(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if p.all {
		return true
	}
	for _, prefix := range p.nets {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
