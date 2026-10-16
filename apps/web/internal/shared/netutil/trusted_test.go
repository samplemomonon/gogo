package netutil

import (
	"net/netip"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	t.Parallel()

	t.Run("parses CIDRs and ignores blanks", func(t *testing.T) {
		t.Parallel()
		proxies, err := ParseTrustedProxies([]string{"10.0.0.0/8", " ", "192.168.0.0/16"})
		if err != nil {
			t.Fatalf("ParseTrustedProxies: %v", err)
		}
		if !proxies.Contains(netip.MustParseAddr("10.1.2.3")) {
			t.Error("10.1.2.3 should be trusted")
		}
		if !proxies.Contains(netip.MustParseAddr("192.168.5.5")) {
			t.Error("192.168.5.5 should be trusted")
		}
		if proxies.Contains(netip.MustParseAddr("8.8.8.8")) {
			t.Error("8.8.8.8 should not be trusted")
		}
	})

	t.Run("all trusts every valid address", func(t *testing.T) {
		t.Parallel()
		proxies, err := ParseTrustedProxies([]string{"all"})
		if err != nil {
			t.Fatalf("ParseTrustedProxies: %v", err)
		}
		if !proxies.Contains(netip.MustParseAddr("8.8.8.8")) {
			t.Error("all should trust a public address")
		}
		if proxies.Contains(netip.Addr{}) {
			t.Error("an invalid address must never be trusted")
		}
	})

	t.Run("malformed CIDR is an error", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseTrustedProxies([]string{"10.0.0.0/33"}); err == nil {
			t.Fatal("expected an error for an out-of-range prefix")
		}
	})

	t.Run("empty set trusts nothing", func(t *testing.T) {
		t.Parallel()
		proxies, err := ParseTrustedProxies(nil)
		if err != nil {
			t.Fatalf("ParseTrustedProxies: %v", err)
		}
		if proxies.Contains(netip.MustParseAddr("10.0.0.1")) {
			t.Error("an empty set must not trust anything")
		}
	})
}
