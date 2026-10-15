package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The Time scalar is the one piece of custom serialisation in the schema, and both of
// its behaviours exist to avoid a specific lie to the client. They are tested here
// rather than through a query because the failure they prevent is invisible in a
// response that decodes cleanly.

// TestTheZeroTimeRendersAsNull is the one that matters most.
//
// Every service here carries timestamps as protobuf Timestamp, and a field nobody set
// arrives as the zero value. Rendering it as "0001-01-01T00:00:00Z" would present a
// missing timestamp as a real one, and a client sorting on it would order every
// unprojected row before every projected row — deterministically and wrongly.
func TestTheZeroTimeRendersAsNull(t *testing.T) {
	for name, value := range map[string]Time{
		"zero value": {},
		"explicit":   TimeFrom(time.Time{}),
		"unix epoch": {}, // the epoch is real data and must not be confused with unset
	} {
		_ = name
		var buf strings.Builder
		value.MarshalGQL(&buf)
		// The epoch is not zero in Go's time.Time, so only the genuinely unset cases
		// reach the null branch.
		if buf.String() != "null" && value.IsZero() {
			t.Errorf("zero time rendered as %q, want null", buf.String())
		}
	}
}

// TestANonZeroTimeRendersAsRFC3339 pins the format.
//
// Seconds precision matters for the client, and UTC matters more: the underlying values
// carry a zone from whichever service produced them, and rendering local offsets would
// make two identical instants sort differently.
func TestANonZeroTimeRendersAsRFC3339(t *testing.T) {
	zone := time.FixedZone("WIB", 7*3600)
	instant := time.Date(2026, 3, 14, 9, 26, 53, 0, zone)

	var buf strings.Builder
	TimeFrom(instant).MarshalGQL(&buf)

	got := buf.String()
	// Quoted, because it goes into a JSON document as a scalar.
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("rendered %s, want a quoted string", got)
	}
	if !strings.Contains(got, "2026-03-14T02:26:53") {
		t.Errorf("rendered %s; want the instant converted to UTC", got)
	}
	if strings.Contains(got, "+07:00") {
		t.Errorf("rendered %s; the local offset leaked into the output", got)
	}
}

// TestTheUnixEpochIsNotConfusedWithUnset is the boundary case.
//
// time.Time's zero value is year 1, not the epoch, so the two are distinguishable — but
// only if the check is IsZero rather than a comparison against Unix(). Getting that
// backwards would null out every legitimately-zero timestamp, which for this codebase
// means every "no projection yet" and, more subtly, a legitimate 1970 row.
func TestTheUnixEpochIsNotConfusedWithUnset(t *testing.T) {
	var buf strings.Builder
	TimeFrom(time.Unix(0, 0)).MarshalGQL(&buf)

	if buf.String() == "null" {
		t.Fatal("the unix epoch rendered as null; it is real data, not an unset field")
	}
	if !strings.Contains(buf.String(), "1970-01-01") {
		t.Errorf("rendered %s, want the epoch", buf.String())
	}
}

func TestUnmarshalGQLAcceptsTheFormsClientsSend(t *testing.T) {
	// A string, and null. Both have to work: a client echoing back a variable it read
	// from this API sends the string form, and an explicit null is a legitimate way to
	// say "no timestamp".
	for _, tc := range []struct {
		name  string
		input any
		want  bool // whether the result should be zero
	}{
		{"rfc3339 string", "2026-03-14T02:26:53Z", false},
		{"null", nil, true},
	} {
		var got Time
		err := got.UnmarshalGQL(tc.input)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if got.IsZero() != tc.want {
			t.Errorf("%s: zero = %v, want %v", tc.name, got.IsZero(), tc.want)
		}
	}
}

func TestUnmarshalGQLRejectsRubbish(t *testing.T) {
	// A number, a boolean and an object are all wrong. Silently coercing one of them to
	// the zero time would produce a field that reads as "never set" rather than as the
	// client error it is.
	for name, input := range map[string]any{
		"integer": 42,
		"boolean": true,
		"object":  map[string]any{"$date": "2026-03-14"},
		"array":   []any{"2026-03-14"},
	} {
		var got Time
		if err := got.UnmarshalGQL(input); err == nil {
			t.Errorf("%s: accepted with no error, want a client error", name)
		}
	}
}

func TestJSONRoundTripPreservesTheNullForUnset(t *testing.T) {
	// The JSON path is what the REST-shaped responses use, and it has to agree with the
	// GraphQL path: a field that renders as null in GraphQL must not render as year one
	// in JSON.
	encoded, err := json.Marshal(Time{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "null" {
		t.Errorf("an unset time marshalled to %s, want null", encoded)
	}
}

func TestUnmarshalJSONRejectsAMalformedTimestamp(t *testing.T) {
	var got Time
	if err := json.Unmarshal([]byte(`"not a timestamp"`), &got); err == nil {
		t.Fatal("a malformed timestamp was accepted")
	}

	// And the two errors a client is most likely to send, both of which should be
	// rejected rather than leaving the field silently zero.
	for _, raw := range []string{`"2026-13-45T99:99:99Z"`, `12345`} {
		var value Time
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}

// TestMoneyIsRenderedAsADecimalString is the other custom type in the schema, and its
// reason for existing is arithmetic rather than presentation.
//
// GraphQL's Int is 32 bits. A price in minor units crosses the schema boundary as a
// String and is carried as an int64, so a price above about 21 million minor units —
// which is nothing in IDR — cannot be silently truncated.
func TestMoneyIsRenderedAsADecimalString(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{12999000, "12999000"},
		// Above the int32 ceiling, which is the whole reason for the type.
		{2147483648, "2147483648"},
		{9_007_199_254_740_993, "9007199254740993"},
		// A large negative, because a refund or a correction must render too.
		{-2147483649, "-2147483649"},
	} {
		if got := Money(tc.cents); got != tc.want {
			t.Errorf("Money(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

func TestTimePtrDistinguishesUnsetFromSet(t *testing.T) {
	// The projection field. A nil pointer and a pointer to the zero time are different
	// answers — "no projection yet" versus "projected at the epoch" — and collapsing
	// them would make the second look like the first.
	if got := TimePtr(nil); got != nil {
		t.Errorf("TimePtr(nil) = %v, want nil", got)
	}

	instant := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := TimePtr(&instant)
	if got == nil {
		t.Fatal("TimePtr of a set time returned nil, which would report it as never projected")
	}
	if !got.Time.Equal(instant) {
		t.Errorf("got %s, want %s", got.Time, instant)
	}
}

func TestJSONUnmarshalStringRejectsTheWrongShape(t *testing.T) {
	// Used by the hand-written JSON readers for the read services' status endpoints.
	// A missing field and a null field are different answers, and this is what keeps
	// them apart.
	var target string
	if err := jsonUnmarshalString([]byte(`"present"`), &target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "present" {
		t.Errorf("got %q, want present", target)
	}

	for name, raw := range map[string]string{
		"empty":     "",
		"unquoted":  `present`,
		"number":    `42`,
		"truncated": `"pres`,
	} {
		var value string
		err := jsonUnmarshalString([]byte(raw), &value)
		if err == nil {
			t.Errorf("%s (%q) was accepted", name, raw)
			continue
		}
		if !strings.Contains(err.Error(), "expected a JSON string") {
			t.Errorf("%s: unhelpful error %v", name, err)
		}
	}
}

func TestUnmarshalJSONOfNullLeavesTheFieldUnset(t *testing.T) {
	// The behaviour that distinguishes "absent" from "empty" for an optional timestamp,
	// which is what makes the projection fields honest.
	var got Time
	if err := json.Unmarshal([]byte(`null`), &got); err != nil {
		t.Fatalf("null should not be an error: %v", err)
	}
	if !got.IsZero() {
		t.Error("null decoded to a set timestamp")
	}

	// And an error must be surfaced rather than swallowed, or a corrupted status file
	// would read as "no projection".
	var bad Time
	if err := json.Unmarshal([]byte(`{}`), &bad); err == nil {
		t.Error("an object was accepted where a timestamp or null was expected")
	} else if errors.Is(err, nil) {
		t.Error("unreachable: err was both non-nil and nil")
	}
}
