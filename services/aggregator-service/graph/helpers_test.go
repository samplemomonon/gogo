package graph

import (
	"context"
	"errors"
	"testing"
)

// collectErrors runs fn with an error collector installed and returns what was
// collected.
//
// gqlgen's AddError writes to the operation context, which only exists during real
// execution. Substituting the collector is what makes the degradation policy
// testable without building a full server: the alternative is asserting on log
// output, which passes whether or not the error was attached to the response.
func collectErrors(t *testing.T, fn func(ctx context.Context)) []error {
	t.Helper()

	original := addError
	t.Cleanup(func() { addError = original })

	var collected []error
	addError = func(_ context.Context, err error) {
		collected = append(collected, err)
	}

	fn(context.Background())
	return collected
}

// extensionOf reads a key from a fieldError's extensions.
func extensionOf(t *testing.T, err error, key string) any {
	t.Helper()

	var field *fieldError
	if !errors.As(err, &field) {
		t.Fatalf("error %v is %T, want *fieldError", err, err)
	}
	value, ok := field.Extensions()[key]
	if !ok {
		t.Fatalf("extensions %v have no %q", field.Extensions(), key)
	}
	return value
}
