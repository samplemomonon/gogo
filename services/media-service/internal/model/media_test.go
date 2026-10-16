// Package model_test pins the parsing and inference rules clients depend on:
// which inputs are accepted, which are defaulted, and which are rejected.
// These functions sit at the edge between an untrusted request and the
// database, so the difference between "defaulted" and "rejected" is the
// difference between a typo being tolerated and a typo silently changing what
// is stored.
package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"media-service/internal/model"
)

// TestParseKindAcceptsTheDocumentedSet pins the accepted values, including the
// case and whitespace normalisation a client might rely on.
func TestParseKindAcceptsTheDocumentedSet(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want model.Kind
	}{
		{"image", model.KindImage},
		{"video", model.KindVideo},
		{"audio", model.KindAudio},
		{"document", model.KindDocument},
		{"other", model.KindOther},
		{"  IMAGE\n", model.KindImage},
	} {
		got, err := model.ParseKind(tt.in)
		if err != nil {
			t.Errorf("ParseKind(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseKind(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestParseKindDefaultsOnlyTheEmptyString pins the asymmetry: an absent kind
// defaults to other (the client may simply not know), but a non-empty unknown
// value is rejected rather than quietly becoming other — that is the typo
// case, and storing a typo's default is worse than refusing the request.
func TestParseKindDefaultsOnlyTheEmptyString(t *testing.T) {
	t.Parallel()

	got, err := model.ParseKind("")
	if err != nil {
		t.Fatalf("ParseKind(\"\"): %v", err)
	}
	if got != model.KindOther {
		t.Errorf("ParseKind(\"\") = %q, want %q", got, model.KindOther)
	}

	_, err = model.ParseKind("photo")
	if err == nil {
		t.Fatal("ParseKind accepted an unknown kind")
	}
	if !strings.Contains(err.Error(), "photo") || !strings.Contains(err.Error(), "image|video|audio|document|other") {
		t.Errorf("error %q should name the value and the allowed set", err)
	}
}

// TestParseStatus pins the two lifecycle values, the empty-string default
// (which means "leave the status alone", so it is not the zero value), and
// the rejection of anything else.
func TestParseStatus(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want model.Status
	}{
		{"pending", model.StatusPending},
		{"ready", model.StatusReady},
		{"  READY ", model.StatusReady},
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

	got, err := model.ParseStatus("")
	if err != nil || got != "" {
		t.Errorf("ParseStatus(\"\") = (%q, %v), want (\"\", nil) so callers can distinguish unset from pending", got, err)
	}

	if _, err := model.ParseStatus("done"); err == nil {
		t.Error("ParseStatus accepted an unknown status")
	}
}

// TestKindForContentTypeInfersFromTheMediaType is the fallback path for a
// client that did not state a kind: standard media types must map to the
// documented families.
func TestKindForContentTypeInfersFromTheMediaType(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want model.Kind
	}{
		{"image/png", model.KindImage},
		{"image/svg+xml", model.KindImage},
		{"video/mp4", model.KindVideo},
		{"audio/mpeg", model.KindAudio},
		{"application/pdf", model.KindDocument},
		{"text/plain; charset=utf-8", model.KindDocument},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", model.KindDocument},
		{"application/vnd.ms-excel", model.KindDocument},
		{"application/octet-stream", model.KindOther},
		{"application/zip", model.KindOther},
	} {
		if got := model.KindForContentType(tt.in); got != tt.want {
			t.Errorf("KindForContentType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestKindForContentTypeFallsBackToTheExtension pins the malformed-header
// path: when the content type is not parseable as a media type — a client
// sending a filename or a value with a space is common — the file extension
// decides. It is a best effort; an unrecognised one becomes other rather than
// an error, because the service has already decided the request is acceptable.
func TestKindForContentTypeFallsBackToTheExtension(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want model.Kind
	}{
		{"my video.mp4", model.KindVideo},
		{"pho to.jpg", model.KindImage},
		{"photo?.jpg", model.KindImage},
		{"my song.flac", model.KindAudio},
		{"report copy.pdf", model.KindDocument},
		{"archive v2.xyz", model.KindOther},
		{"not a type!", model.KindOther},
	} {
		if got := model.KindForContentType(tt.in); got != tt.want {
			t.Errorf("KindForContentType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestKindForContentTypeTreatsBareTagsAsUnknown pins the other side of the
// parser's tolerance: a slash-less value such as "photo.JPEG" parses as a
// media type (the Go parser accepts bare tokens), so it is not malformed and
// misses the extension fallback — an unrecognised type becomes other. The
// service's normaliser is what turns an omitted header into a real MIME type
// before inference runs.
func TestKindForContentTypeTreatsBareTagsAsUnknown(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"photo.JPEG", "jpg", "octet-stream"} {
		if got := model.KindForContentType(in); got != model.KindOther {
			t.Errorf("KindForContentType(%q) = %q, want %q", in, got, model.KindOther)
		}
	}
}

// TestExtensionFor pins the object-key extension rule: lowercased, no leading
// dot, and empty when there is nothing usable to append.
func TestExtensionFor(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want string
	}{
		{"photo.JPG", "jpg"},
		{"archive.tar.gz", "gz"},
		{"no-extension", ""},
		{"trailing.", ""},
		{"", ""},
	} {
		if got := model.ExtensionFor(tt.in); got != tt.want {
			t.Errorf("ExtensionFor(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestNamedStringTypesMarshalAsStrings pins the wire shape: Kind and Status
// are named string types, and both must come out as plain JSON strings rather
// than objects. Kind has an explicit MarshalJSON; Status relies on the
// underlying type, so both are worth pinning.
func TestNamedStringTypesMarshalAsStrings(t *testing.T) {
	t.Parallel()

	kindJSON, err := json.Marshal(model.KindImage)
	if err != nil {
		t.Fatalf("marshal kind: %v", err)
	}
	if string(kindJSON) != `"image"` {
		t.Errorf("Kind image marshals as %s, want %q", kindJSON, `"image"`)
	}

	statusJSON, err := json.Marshal(model.StatusPending)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if string(statusJSON) != `"pending"` {
		t.Errorf("Status pending marshals as %s, want %q", statusJSON, `"pending"`)
	}
}
