package keyset

import (
	"errors"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	pos := Pos{CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), ID: "p-1:2/3+4"}
	got, err := Decode(Encode(pos))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !got.CreatedAt.Equal(pos.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, pos.CreatedAt)
	}
	if got.ID != pos.ID {
		t.Errorf("ID = %q, want %q", got.ID, pos.ID)
	}
}

func TestPreservesMicrosecondOrdering(t *testing.T) {
	older := Pos{CreatedAt: time.UnixMicro(1_700_000_000_000_000).UTC(), ID: "p-1"}
	newer := Pos{CreatedAt: time.UnixMicro(1_700_000_000_000_001).UTC(), ID: "p-1"}

	gotOlder, err := Decode(Encode(older))
	if err != nil {
		t.Fatal(err)
	}
	gotNewer, err := Decode(Encode(newer))
	if err != nil {
		t.Fatal(err)
	}
	if gotNewer.CreatedAt.Equal(gotOlder.CreatedAt) {
		t.Error("decoded positions compare equal, want the microsecond difference to survive")
	}
	if !gotOlder.After(gotNewer) || gotNewer.After(gotOlder) {
		t.Error("created_at DESC ordering not preserved through the round trip")
	}
}

func TestAfterOrdering(t *testing.T) {
	older := time.UnixMicro(1_000).UTC()
	newer := time.UnixMicro(2_000).UTC()

	table := []struct {
		name string
		pos  Pos
		base Pos
		want bool
	}{
		{"older created_at sorts after in a DESC listing", Pos{CreatedAt: older, ID: "b"}, Pos{CreatedAt: newer, ID: "a"}, true},
		{"newer created_at does not", Pos{CreatedAt: newer, ID: "a"}, Pos{CreatedAt: older, ID: "a"}, false},
		{"equal created_at breaks on a greater id", Pos{CreatedAt: newer, ID: "b"}, Pos{CreatedAt: newer, ID: "a"}, true},
		{"equal position is not after itself", Pos{CreatedAt: newer, ID: "a"}, Pos{CreatedAt: newer, ID: "a"}, false},
	}
	for _, tt := range table {
		if got := tt.pos.After(tt.base); got != tt.want {
			t.Errorf("%s: After = %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestRejectsMalformed(t *testing.T) {
	for _, cursor := range []string{
		"",
		"not-a-cursor",
		":abc",
		"12",
		"abc:abc",
		"123:",
		"123:###",
	} {
		if _, err := Decode(cursor); !errors.Is(err, ErrInvalid) {
			t.Errorf("Decode(%q) error = %v, want ErrInvalid", cursor, err)
		}
	}
}
