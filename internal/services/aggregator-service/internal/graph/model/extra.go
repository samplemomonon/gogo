package model

import (
	"fmt"
	"io"
	"strconv"
	"time"
)

// Time is the scalar type behind the schema's Time scalar.
//
// It marshals as RFC 3339 in UTC. The zero time marshals as null rather than as
// "0001-01-01T00:00:00Z": a timestamp nobody set is not a timestamp, and a client
// parsing the year 1 as a real date is a bug worth avoiding. Every field that can
// legitimately be absent is declared nullable in the schema for the same reason.
type Time struct{ time.Time }

// MarshalGQL renders the time for the GraphQL executor.
//
// The zero time becomes null rather than the year 1. Every service here carries
// timestamps as protobuf Timestamp, and a field that was never set arrives as the
// zero value; rendering "0001-01-01T00:00:00Z" would present a missing timestamp as
// a real one, and a client sorting or filtering on it would be misled. Nullable
// fields in the schema carry the same meaning for the same reason.
func (t Time) MarshalGQL(w io.Writer) {
	if t.IsZero() {
		_, _ = io.WriteString(w, "null")
		return
	}
	_, _ = io.WriteString(w, strconv.Quote(t.UTC().Format(time.RFC3339Nano)))
}

// UnmarshalGQL parses a timestamp from a GraphQL variable or literal.
func (t *Time) UnmarshalGQL(v any) error {
	switch value := v.(type) {
	case nil:
		t.Time = time.Time{}
		return nil
	case string:
		if value == "" {
			t.Time = time.Time{}
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return fmt.Errorf("parse time %q: expected RFC 3339", value)
		}
		t.Time = parsed
		return nil
	case time.Time:
		t.Time = value
		return nil
	default:
		return fmt.Errorf("cannot parse %T as a Time", v)
	}
}

// MarshalJSON renders the time, or null when it is unset. Used by tests and by
// anything that serialises a model outside the GraphQL executor.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + t.UTC().Format(time.RFC3339Nano) + `"`), nil
}

// UnmarshalJSON accepts an RFC 3339 string. gqlgen decodes scalars through the
// generated unmarshaler, so this exists for symmetry and for tests.
func (t *Time) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := jsonUnmarshalString(data, &s); err != nil {
		return err
	}
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("parse time %q: %w", s, err)
	}
	t.Time = parsed
	return nil
}

// TimeFrom builds a scalar from a stdlib time.
func TimeFrom(t time.Time) Time { return Time{Time: t} }

// TimePtr converts a nullable timestamp to a scalar pointer. A nil input becomes
// a nil output, which gqlgen renders as null.
func TimePtr(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	return &Time{Time: *t}
}

// Money renders an int64 amount of minor units as a string.
//
// GraphQL's Int is a signed 32-bit integer, so the maximum exactly representable
// price would be about 21 million minor units: roughly IDR 210,000 at two
// decimals, which a single SKU can exceed. Serialising money as a string is the
// standard answer, and the alternative, Int64, is deprecated in the spec for
// exactly this reason.
func Money(cents int64) string { return strconv.FormatInt(cents, 10) }

// WriteProductResult is the outcome of a product write.
type WriteProductResult struct {
	Product Product
	// Projected reports whether a read model has caught up with this write yet.
	// False is the normal answer immediately after a create.
	Projected bool
	// AppliedEvents is the listing projection's event count when it could be read.
	AppliedEvents *int64
}

func jsonUnmarshalString(data []byte, dst *string) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("expected a JSON string, got %s", string(data))
	}
	*dst = string(data[1 : len(data)-1])
	return nil
}
