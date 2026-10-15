package grpcclient

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"aggregator-service/gen/categorypb"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/graph/model"
)

// The error classification is the most consequential thing in this package. Every
// downstream code maps into one of two buckets that a client acts on oppositely:
// retryable, or don't retry. A mapping error in the "don't retry" direction is
// worse than one in the other, because it teaches a client to give up on a fault
// that would have cleared.

func TestClassifyMapsRetryableCodes(t *testing.T) {
	for _, code := range []codes.Code{
		codes.Unavailable,
		codes.DeadlineExceeded,
		codes.ResourceExhausted,
		codes.Aborted,
	} {
		err := Classify(status.Error(code, "boom"))
		if !errors.Is(err, apperror.ErrUnavailable) {
			t.Fatalf("%s: got %v, want ErrUnavailable", code, err)
		}
	}
}

func TestClassifyMapsClientCodes(t *testing.T) {
	// These are decisions the service made about what it was sent. Retrying the same
	// payload gets the same answer, so they must not be reported as retryable.
	for _, code := range []codes.Code{
		codes.InvalidArgument,
		codes.OutOfRange,
		codes.FailedPrecondition,
		codes.AlreadyExists,
		codes.PermissionDenied,
		codes.Unauthenticated,
	} {
		err := Classify(status.Error(code, "boom"))
		if !errors.Is(err, apperror.ErrInvalidInput) {
			t.Fatalf("%s: got %v, want ErrInvalidInput", code, err)
		}
		if errors.Is(err, apperror.ErrUnavailable) {
			t.Fatalf("%s must not be retryable", code)
		}
	}
}

func TestClassifyMapsNotFound(t *testing.T) {
	err := Classify(status.Error(codes.NotFound, "product 5"))
	if !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestUnknownCodeDefaultsToRetryable(t *testing.T) {
	// The direction of this default is the point. An unrecognised code could be
	// anything, and guessing "client error" would suppress retries for a fault
	// nobody has characterised yet. Guessing "retryable" costs at most a few wasted
	// attempts.
	for _, code := range []codes.Code{codes.Internal, codes.Unknown, codes.DataLoss} {
		err := Classify(status.Error(code, "boom"))
		if !errors.Is(err, apperror.ErrUnavailable) {
			t.Fatalf("%s: got %v, want ErrUnavailable", code, err)
		}
	}
}

func TestClassifyPreservesAnAlreadyClassifiedError(t *testing.T) {
	// Re-wrapping would still satisfy errors.Is, but the message would gain a layer
	// and a caller checking the original sentinel with a type assertion would miss.
	original := &apperror.InvalidInput{Message: "price must be positive", Field: "priceCents"}
	if got := Classify(original); got != error(original) {
		t.Fatalf("got %v, want the original error returned unchanged", got)
	}
}

func TestClassifyNilIsNil(t *testing.T) {
	if err := Classify(nil); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestIsNotFoundRecognisesBothForms(t *testing.T) {
	// A wrapped sentinel from a stub, and a raw gRPC status from a real call.
	if !IsNotFound(status.Error(codes.NotFound, "gone")) {
		t.Fatal("a raw NotFound status should be recognised")
	}
	if !IsNotFound(errors.Join(apperror.ErrNotFound, errors.New("context"))) {
		t.Fatal("a wrapped sentinel should be recognised")
	}
	if IsNotFound(status.Error(codes.Unavailable, "down")) {
		t.Fatal("Unavailable must not be read as not-found")
	}
}

func TestParseIDRejectsNonNumeric(t *testing.T) {
	// Silently coercing to 0 would either fetch the wrong entity or return a
	// confusing not-found for a record that was never addressed.
	for _, id := range []string{"abc", "1.5", "-1", "0", ""} {
		if _, err := parseID(id); err == nil {
			t.Fatalf("parseID(%q) should have failed", id)
		} else if !errors.Is(err, apperror.ErrInvalidInput) {
			t.Fatalf("parseID(%q) got %v, want ErrInvalidInput", id, err)
		}
	}
}

func TestParseIDAcceptsPositiveIntegers(t *testing.T) {
	got, err := parseID("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Fatalf("got %d", got)
	}
}

func TestParseOptionalIDMapsEmptyToZero(t *testing.T) {
	// Every contract documents 0 as "unset", so an absent GraphQL reference has to
	// become 0 rather than an error or a guess.
	got, err := parseOptionalID("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestStatusConversionRoundTrips(t *testing.T) {
	// Each contract declares its own enum, so a value has to survive the trip through
	// whichever mapper belongs to the contract being called.
	for _, statusValue := range []model.Status{model.StatusDraft, model.StatusActive, model.StatusArchived} {
		writeProto, err := statusToProto(statusValue)
		if err != nil {
			t.Fatalf("statusToProto(%q): %v", statusValue, err)
		}
		if got := statusFromProto(writeProto); got != statusValue {
			t.Fatalf("write contract: %q became %q", statusValue, got)
		}

		listingProto, err := listingStatusToProto(statusValue)
		if err != nil {
			t.Fatalf("listingStatusToProto(%q): %v", statusValue, err)
		}
		if got := listingStatusFromProto(listingProto); got != statusValue {
			t.Fatalf("listing contract: %q became %q", statusValue, got)
		}

		searchProto, err := searchStatusToProto(statusValue)
		if err != nil {
			t.Fatalf("searchStatusToProto(%q): %v", statusValue, err)
		}
		if got := searchStatusFromProto(searchProto); got != statusValue {
			t.Fatalf("search contract: %q became %q", statusValue, got)
		}
	}
}

func TestUnspecifiedStatusMapsToEmpty(t *testing.T) {
	// Rendering null is honest; inventing "DRAFT" would report a status the service
	// never sent.
	proto, err := statusToProto(model.StatusUnknown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := statusFromProto(proto); got != model.StatusUnknown {
		t.Fatalf("got %q, want the empty status", got)
	}
}

func TestMediaKindConversion(t *testing.T) {
	for _, kind := range []model.MediaKind{
		model.MediaKindImage, model.MediaKindVideo, model.MediaKindAudio,
		model.MediaKindDocument, model.MediaKindOther,
	} {
		proto, err := mediaKindToProto(kind)
		if err != nil {
			t.Fatalf("mediaKindToProto(%q): %v", kind, err)
		}
		if got := mediaKindFromProto(proto); got != kind {
			t.Fatalf("%q became %q", kind, got)
		}
	}
}

func TestMediaStatusConversion(t *testing.T) {
	for _, s := range []model.MediaStatus{model.MediaStatusPending, model.MediaStatusReady} {
		proto, err := mediaStatusToProto(s)
		if err != nil {
			t.Fatalf("mediaStatusToProto(%q): %v", s, err)
		}
		if got := mediaStatusFromProto(proto); got != s {
			t.Fatalf("%q became %q", s, got)
		}
	}
}

func TestFlattenTreePreservesTraversalOrder(t *testing.T) {
	// Depth-first, and the resolver sorts afterwards. Preserving order here means the
	// sort has something deterministic to work from.
	ts := timeAt("2026-01-01T00:00:00Z")
	roots := []*categorypb.CategoryNode{
		{
			Category: &categorypb.Category{Id: 1, Name: "Root"},
			Children: []*categorypb.CategoryNode{
				{Category: &categorypb.Category{Id: 2, Name: "Zebra", ParentId: 1, CreatedAt: ts}},
				{Category: &categorypb.Category{Id: 3, Name: "Apple", ParentId: 1, CreatedAt: ts}},
			},
		},
		{Category: &categorypb.Category{Id: 4, Name: "Second root"}},
	}

	got := flattenTree(roots)
	if len(got) != 4 {
		t.Fatalf("got %d categories, want 4", len(got))
	}
	if got[0].ID != "1" || got[1].ID != "2" || got[2].ID != "3" || got[3].ID != "4" {
		t.Fatalf("traversal order not preserved: %s, %s, %s, %s", got[0].ID, got[1].ID, got[2].ID, got[3].ID)
	}
	// The child order is the service's, not sorted here: sorting is the resolver's
	// decision and belongs with the fields that promise a stable order.
	if got[1].Name != "Zebra" || got[2].Name != "Apple" {
		t.Fatalf("the adapter reordered children: %s then %s", got[1].Name, got[2].Name)
	}
}

func TestFlattenTreeSkipsNilCategories(t *testing.T) {
	// A node with no category is a malformed response. Skipping it keeps the
	// hierarchy usable rather than yielding an empty-string id that matches nothing.
	got := flattenTree([]*categorypb.CategoryNode{
		{Category: nil},
		{Category: &categorypb.Category{Id: 7, Name: "Real"}},
	})
	if len(got) != 1 || got[0].ID != "7" {
		t.Fatalf("got %+v", got)
	}
}

func TestCountDescendantsExcludesTheRoot(t *testing.T) {
	// The question is how many would be affected by deleting this category, which is
	// the root's descendants, not the root itself.
	roots := []*categorypb.CategoryNode{
		{
			Category: &categorypb.Category{Id: 1},
			Children: []*categorypb.CategoryNode{
				{
					Category: &categorypb.Category{Id: 2, ParentId: 1},
					Children: []*categorypb.CategoryNode{
						{Category: &categorypb.Category{Id: 3, ParentId: 2}},
					},
				},
			},
		},
		{Category: &categorypb.Category{Id: 9}},
	}

	if got := countDescendants(roots, 1); got != 2 {
		t.Fatalf("got %d, want 2 (ids 2 and 3)", got)
	}
	if got := countDescendants(roots, 9); got != 0 {
		t.Fatalf("a leaf has %d descendants, want 0", got)
	}
	if got := countDescendants(roots, 404); got != 0 {
		t.Fatalf("an unknown id reported %d descendants", got)
	}
}

func TestCountDescendantsTerminatesOnCycle(t *testing.T) {
	roots := []*categorypb.CategoryNode{
		{
			Category: &categorypb.Category{Id: 1},
			Children: []*categorypb.CategoryNode{
				{
					Category: &categorypb.Category{Id: 2, ParentId: 1},
					Children: []*categorypb.CategoryNode{
						// Points back at its own ancestor. Only possible if data was
						// restored from before the write-side cycle check existed.
						{Category: &categorypb.Category{Id: 3, ParentId: 2}},
						{Category: &categorypb.Category{Id: 1, ParentId: 3}},
					},
				},
			},
		},
	}

	// Returning at all is most of the assertion: without the seen-set this recurses
	// until the stack is exhausted, which fails the test rather than hanging it.
	got := countDescendants(roots, 2)
	if got != 2 {
		t.Fatalf("got %d; want 2 (id 3, then id 1 before the cycle is detected)", got)
	}
}

func TestMoneyKeepsInt64Precision(t *testing.T) {
	// Above the GraphQL Int ceiling. An int32 round trip would silently truncate a
	// price, which is a wrong price rather than an error.
	const large = int64(9_007_199_254_740_993)
	if got := model.Money(large); got != "9007199254740993" {
		t.Fatalf("got %q", got)
	}
}

// timeAt builds a protobuf timestamp for test fixtures.
func timeAt(rfc3339 string) *timestamppb.Timestamp {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return timestamppb.New(ts)
}
