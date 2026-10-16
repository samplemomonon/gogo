package events

import (
	"encoding/json"
	"fmt"
	"time"
)

// Wire is the cross-service envelope for domain events — the JSON contract
// a service would write when relaying an event to another service over any
// transport (the current split deployment streams events over the catalog.v1
// gRPC WatchProducts contract; a broker would reuse this envelope). The
// envelope is deliberately minimal and stable: it never carries transport or
// delivery metadata, so a future broker-backed bus (NATS/Kafka) can reuse
// the exact same bytes.
type Wire struct {
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	PublishedAt time.Time       `json:"published_at"`
}

// MarshalEvent serializes an in-process event into the wire format. The
// payload struct is JSON-marshalled as-is, so contract structs must expose
// a stable JSON shape (use explicit json tags — see the catalog events
// package).
func MarshalEvent(event Event) ([]byte, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return nil, fmt.Errorf("events: marshal payload for %q: %w", event.Type, err)
	}
	return json.Marshal(Wire{
		Type:        event.Type,
		Payload:     payload,
		PublishedAt: time.Now().UTC(),
	})
}

// DecodeFunc is how a receiver turns a wire payload back into the typed and
// locally meaningful payload struct. Every service hands its own decode
// implementation (mapping the contract types it knows) to UnmarshalEvent —
// the same pattern as the Subscribe type contracts.
type DecodeFunc func(typ string, payload json.RawMessage) (any, error)

// UnmarshalEvent parses a wire envelope and rebuilds the typed in-process
// event using decode. An envelope whose type decode does not know is an
// error: relaying data we cannot interpret would fabricate events.
func UnmarshalEvent(data []byte, decode DecodeFunc) (Event, error) {
	var wire Wire
	if err := json.Unmarshal(data, &wire); err != nil {
		return Event{}, fmt.Errorf("events: decode wire envelope: %w", err)
	}
	if wire.Type == "" {
		return Event{}, fmt.Errorf("events: wire envelope has empty type")
	}
	payload, err := decode(wire.Type, wire.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("events: decode payload for %q: %w", wire.Type, err)
	}
	return Event{Type: wire.Type, Payload: payload}, nil
}
