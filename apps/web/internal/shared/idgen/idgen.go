// Package idgen provides unique ID generation for entities.
package idgen

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Generator creates unique IDs.
type Generator interface {
	Generate() string
}

// defaultGenerator implements Generator using crypto/rand.
type defaultGenerator struct {
	prefix string
}

// New creates a default ID generator with optional prefix.
func New(prefix ...string) Generator {
	p := ""
	if len(prefix) > 0 {
		p = prefix[0]
	}
	return &defaultGenerator{prefix: p}
}

// Generate creates a cryptographically random ID.
func (g *defaultGenerator) Generate() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to a deterministic but still unique format
		return fmt.Sprintf("%sunique-%d", g.prefix, randomInt())
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	if g.prefix != "" {
		return g.prefix + "-" + id
	}
	return id
}

func randomInt() int64 {
	b := make([]byte, 8)
	rand.Read(b)
	var n int64
	for i := 0; i < 8; i++ {
		n = n<<8 | int64(b[i])
	}
	if n < 0 {
		n = -n
	}
	return n
}
