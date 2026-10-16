package idgen

import (
	"regexp"
	"strings"
	"testing"
)

// idShape is the URL-safe alphabet base64.RawURLEncoding produces. IDs travel
// in URLs and file names, so a stray '+' or '=' would be a routing bug, not a
// cosmetic one.
var idShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func TestGenerate(t *testing.T) {
	t.Parallel()

	t.Run("returns a URL-safe token with no prefix", func(t *testing.T) {
		t.Parallel()

		id := New().Generate()
		if id == "" {
			t.Fatal("Generate returned an empty ID")
		}
		if !idShape.MatchString(id) {
			t.Errorf("Generate = %q, which is not URL-safe", id)
		}
		// 16 random bytes under RawURLEncoding are exactly 22 characters.
		if len(id) != 22 {
			t.Errorf("Generate length = %d, want 22", len(id))
		}
	})

	t.Run("puts the prefix before a separator", func(t *testing.T) {
		t.Parallel()

		id := New("ord").Generate()
		if !strings.HasPrefix(id, "ord-") {
			t.Fatalf("Generate with prefix = %q, want the ord- prefix", id)
		}
		suffix := strings.TrimPrefix(id, "ord-")
		if !idShape.MatchString(suffix) || len(suffix) != 22 {
			t.Errorf("Generate suffix = %q, want a 22-character URL-safe token", suffix)
		}
	})

	t.Run("takes at most the first prefix", func(t *testing.T) {
		t.Parallel()

		id := New("a", "b").Generate()
		if !strings.HasPrefix(id, "a-") {
			t.Errorf("Generate = %q, want the first prefix to win", id)
		}
	})

	t.Run("generates distinct IDs", func(t *testing.T) {
		t.Parallel()

		const n = 1000
		gen := New()
		seen := make(map[string]bool, n)
		for i := 0; i < n; i++ {
			id := gen.Generate()
			if seen[id] {
				t.Fatalf("Generate produced a duplicate ID after %d draws: %q", i, id)
			}
			seen[id] = true
		}
	})
}
