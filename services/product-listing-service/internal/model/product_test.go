// Package model_test pins the read model's parsing and derivation rules.
// These are the edges between an untrusted query and the database: which
// inputs are accepted, what an absent value means, and how a slug is formed.
package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// TestParseStatusAcceptsTheDocumentedSet pins the accepted publication states,
// including the case and whitespace normalisation a client might rely on.
func TestParseStatusAcceptsTheDocumentedSet(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want model.Status
	}{
		{"draft", model.StatusDraft},
		{"active", model.StatusActive},
		{"archived", model.StatusArchived},
		{"  ACTIVE\n", model.StatusActive},
	} {
		got, err := model.ParseStatus(tt.in)
		if err != nil {
			t.Errorf("ParseStatus(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestParseStatusDefaultsOnlyTheEmptyString pins the asymmetry: an absent
// filter defaults to draft — the hidden state, so an unqualified browse
// cannot accidentally expose unpublished products — while a non-empty unknown
// value is rejected rather than quietly returning the draft catalogue.
func TestParseStatusDefaultsOnlyTheEmptyString(t *testing.T) {
	t.Parallel()

	got, err := model.ParseStatus("")
	if err != nil {
		t.Fatalf("ParseStatus(\"\"): %v", err)
	}
	if got != model.StatusDraft {
		t.Errorf("ParseStatus(\"\") = %q, want %q", got, model.StatusDraft)
	}

	_, err = model.ParseStatus("live")
	if err == nil {
		t.Fatal("ParseStatus accepted an unknown status")
	}
	if !strings.Contains(err.Error(), "live") || !strings.Contains(err.Error(), "draft|active|archived") {
		t.Errorf("error %q should name the value and the allowed set", err)
	}
}

// TestSlugify pins the URL shape: lowercased, separators collapsed to single
// hyphens, and never a leading or trailing hyphen. Empty input yields an empty
// slug rather than a lone hyphen.
func TestSlugify(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want string
	}{
		{"Kaos Polos Hitam", "kaos-polos-hitam"},
		{"  Kaos   Polos  ", "kaos-polos"},
		{"Kaos--Polos", "kaos-polos"},
		{"--Leading and trailing--", "leading-and-trailing"},
		{"Kaos Polos 2 XL!", "kaos-polos-2-xl"},
		{"", ""},
		{"!!!", ""},
	} {
		if got := model.Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestStatusMarshalsAsAString pins the wire shape: Status is a named string
// type and must come out as a plain JSON string, so the read API's filter and
// any fixture comparisons stay in the documented vocabulary.
func TestStatusMarshalsAsAString(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(model.StatusActive)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if string(got) != `"active"` {
		t.Errorf("Status active marshals as %s, want %q", got, `"active"`)
	}
}
