// Package keyset implements the opaque keyset-pagination cursor shared by the
// catalog write model and the listing read model, so both encode the
// (created_at DESC, id ASC) position identically on the wire.
package keyset

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Pos is a position in the (created_at DESC, id ASC) listing order. The id is
// the unique tiebreak, so the pair pins a single row.
type Pos struct {
	CreatedAt time.Time
	ID        string
}

// ErrInvalid signals a malformed or tampered cursor value.
var ErrInvalid = errors.New("invalid pagination cursor")

// After reports whether pos sorts strictly after base in the listing order
// (created_at DESC, id ASC): a row that would appear later in the page stream.
func (pos Pos) After(base Pos) bool {
	if pos.CreatedAt.Before(base.CreatedAt) {
		return true
	}
	if pos.CreatedAt.After(base.CreatedAt) {
		return false
	}
	return pos.ID > base.ID
}

// Encode renders a position as an opaque, transport-safe string. The value is
// not meant to be read by clients — it round-trips through the wire contract.
func Encode(pos Pos) string {
	micros := pos.CreatedAt.UnixMicro()
	id := base64.RawURLEncoding.EncodeToString([]byte(pos.ID))
	return strconv.FormatInt(micros, 10) + ":" + id
}

// Decode parses a cursor produced by Encode. A value that does not match the
// expected shape signals ErrInvalid rather than panicking.
func Decode(cursor string) (Pos, error) {
	if cursor == "" {
		return Pos{}, ErrInvalid
	}

	sep := strings.IndexByte(cursor, ':')
	if sep < 1 {
		return Pos{}, ErrInvalid
	}

	micros, err := strconv.ParseInt(cursor[:sep], 10, 64)
	if err != nil {
		return Pos{}, fmt.Errorf("%w: bad timestamp", ErrInvalid)
	}

	idBytes, err := base64.RawURLEncoding.DecodeString(cursor[sep+1:])
	if err != nil || len(idBytes) == 0 {
		return Pos{}, fmt.Errorf("%w: bad id", ErrInvalid)
	}

	return Pos{CreatedAt: time.UnixMicro(micros).UTC(), ID: string(idBytes)}, nil
}
