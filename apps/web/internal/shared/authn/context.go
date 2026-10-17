// Package authn carries authenticated request identity across internal RPC
// boundaries. Values are set by the HTTP authentication middleware and module
// adapters, never copied from protobuf actor fields.
package authn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
)

const (
	SessionHeader          = "x-gogo-session"
	CallerHeader           = "x-gogo-caller"
	CallerCredentialHeader = "x-gogo-caller-credential"
	SalesCaller            = "sales"
)

type key uint8

const (
	sessionKey key = iota
	callerKey
	credentialKey
)

// Capability is a process-local, unguessable credential used only across
// private in-process module transports. It is not a network service token.
type Capability struct{ value string }

func NewCapability() (Capability, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return Capability{}, err
	}
	return Capability{value: base64.RawURLEncoding.EncodeToString(value)}, nil
}

func WithService(ctx context.Context, service string, capability Capability) context.Context {
	ctx = context.WithValue(ctx, callerKey, service)
	return context.WithValue(ctx, credentialKey, capability.value)
}

func HasServiceCapability(ctx context.Context, service string, capability Capability) bool {
	caller, _ := ctx.Value(callerKey).(string)
	credential, _ := ctx.Value(credentialKey).(string)
	return capability.value != "" && caller == service && credential == capability.value
}

func ServiceCredential(ctx context.Context) string {
	credential, _ := ctx.Value(credentialKey).(string)
	return credential
}

func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey, id)
}

func Session(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey).(string)
	return id
}

func WithCaller(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, callerKey, caller)
}

func WithCallerCredential(ctx context.Context, credential string) context.Context {
	return context.WithValue(ctx, credentialKey, credential)
}

func Caller(ctx context.Context) string {
	caller, _ := ctx.Value(callerKey).(string)
	return caller
}
