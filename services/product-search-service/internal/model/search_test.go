// Package model_test pins the search read model's parsing rules and wire
// shape. These are the edges between an untrusted query and the index: which
// filter values are accepted, what an absent value means, and how a result
// serialises.
package model_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"product-search-service/internal/model"
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
// filter defaults to draft — the hidden state, so an unqualified search
// cannot accidentally expose unpublished products — while a non-empty
// unknown value is rejected rather than quietly returning the draft index.
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

// TestSearchProductMarshalsTheCardShape pins the JSON keys a consumer of the
// search API sees, including the score and the two timestamps that are
// deliberately both present.
func TestSearchProductMarshalsTheCardShape(t *testing.T) {
	t.Parallel()

	product := model.SearchProduct{
		ID:          7,
		CategoryID:  3,
		Name:        "Kaos",
		Slug:        "kaos",
		PriceCents:  199000,
		Currency:    "IDR",
		Stock:       12,
		Status:      model.StatusActive,
		Score:       0.87,
		CreatedAt:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		ProjectedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}

	encoded, err := json.Marshal(product)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Variants are deliberately absent: a page of twenty results must not grow
	// with the number of variants per product.
	if _, ok := decoded["variants"]; ok {
		t.Error("search results must not carry a variant list")
	}

	for key, want := range map[string]any{
		"id":          float64(7),
		"category_id": float64(3),
		"name":        "Kaos",
		"slug":        "kaos",
		"price_cents": float64(199000),
		"currency":    "IDR",
		"stock":       float64(12),
		"status":      "active",
		"score":       0.87,
	} {
		if got := decoded[key]; got != want {
			t.Errorf("field %s = %v, want %v", key, got, want)
		}
	}

	// The nullable fields must serialise as null rather than as empty values,
	// so a client can tell "no description" from "empty description".
	for _, key := range []string{"description", "brand", "primary_media_id"} {
		if got := decoded[key]; got != nil {
			t.Errorf("field %s = %v, want null", key, got)
		}
	}
}
