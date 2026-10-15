package kernel

import "time"

// BaseEvent provides a reusable implementation of the Event interface.
type BaseEvent struct {
	EventType    string
	EventPayload interface{}
	EventTime    int64
}

// NewBaseEvent creates a new event with the current timestamp.
func NewBaseEvent(eventType string, payload interface{}) *BaseEvent {
	return &BaseEvent{
		EventType:    eventType,
		EventPayload: payload,
		EventTime:    time.Now().Unix(),
	}
}

// Type returns the event type identifier.
func (e *BaseEvent) Type() string {
	return e.EventType
}

// Payload returns the event data.
func (e *BaseEvent) Payload() interface{} {
	return e.EventPayload
}

// Timestamp returns when the event occurred.
func (e *BaseEvent) Timestamp() int64 {
	return e.EventTime
}
