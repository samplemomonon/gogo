package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type wireTestPayload struct {
	ID   string `json:"id"`
	Slot int    `json:"slot"`
}

const wireTestType = "test.item.done"

func wireTestDecode(typ string, raw json.RawMessage) (any, error) {
	if typ != wireTestType {
		return nil, fmt.Errorf("unknown test event type %q", typ)
	}
	var p wireTestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return p, nil
}

func TestWireRoundTrip(t *testing.T) {
	t.Parallel()

	data, err := MarshalEvent(Event{Type: wireTestType, Payload: wireTestPayload{ID: "p-1", Slot: 7}})
	if err != nil {
		t.Fatalf("MarshalEvent returned error: %v", err)
	}

	event, err := UnmarshalEvent(data, wireTestDecode)
	if err != nil {
		t.Fatalf("UnmarshalEvent returned error: %v", err)
	}
	if event.Type != wireTestType {
		t.Errorf("Type = %q, want %q", event.Type, wireTestType)
	}
	payload, ok := event.Payload.(wireTestPayload)
	if !ok {
		t.Fatalf("Payload = %T, want wireTestPayload", event.Payload)
	}
	if payload.ID != "p-1" || payload.Slot != 7 {
		t.Errorf("Payload = %+v, want p-1/7", payload)
	}
}

func TestUnmarshalEventRejectsUnknownType(t *testing.T) {
	t.Parallel()

	data, err := MarshalEvent(Event{Type: "other.foo", Payload: wireTestPayload{ID: "x"}})
	if err != nil {
		t.Fatalf("MarshalEvent returned error: %v", err)
	}
	if _, err := UnmarshalEvent(data, wireTestDecode); err == nil {
		t.Fatal("UnmarshalEvent should reject an event whose type the decode does not know")
	}
}

func TestUnmarshalEventRejectsGarbage(t *testing.T) {
	t.Parallel()

	cases := []string{
		`not json at all`,
		`{"type":""}`,
		`{"type":"` + wireTestType + `","payload":7}`,
	}
	for _, in := range cases {
		if _, err := UnmarshalEvent([]byte(in), wireTestDecode); err == nil {
			t.Errorf("UnmarshalEvent(%q) should error, got nil", in)
		}
	}
}

func TestWireEnvelopeFields(t *testing.T) {
	t.Parallel()

	data, err := MarshalEvent(Event{Type: wireTestType, Payload: wireTestPayload{ID: "p-2"}})
	if err != nil {
		t.Fatalf("MarshalEvent returned error: %v", err)
	}
	var wire Wire
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("wire envelope is not JSON: %v", err)
	}
	if wire.Type != wireTestType {
		t.Errorf("Type = %q, want %q", wire.Type, wireTestType)
	}
	if wire.PublishedAt.IsZero() {
		t.Error("PublishedAt should be set on the wire envelope")
	}
	if !strings.Contains(string(wire.Payload), `"id"`) {
		t.Errorf("payload should use the portable field name, got %s", wire.Payload)
	}
}
