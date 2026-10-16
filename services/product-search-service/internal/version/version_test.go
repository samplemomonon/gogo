// Package version_test pins the build-identity contract: a local build reports
// the documented placeholders, and the container image really stamps this
// package's variables. A renamed variable or package would otherwise ship as
// "dev (commit unknown)" in production with nothing failing.
package version_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"product-search-service/internal/version"
)

// TestDefaultsAreTheDocumentedFallbacks pins the local-build identity: all
// three values are the documented placeholders and none is empty, so version
// reporting can render them unconditionally.
func TestDefaultsAreTheDocumentedFallbacks(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		got  string
		want string
	}{
		{"Version", version.Version, "dev"},
		{"Commit", version.Commit, "unknown"},
		{"Date", version.Date, "unknown"},
	} {
		if tt.got == "" {
			t.Errorf("version.%s is empty; it must fall back to a placeholder", tt.name)
		}
		if tt.got != tt.want {
			t.Errorf("version.%s = %q, want the documented fallback %q", tt.name, tt.got, tt.want)
		}
	}
}

// TestShortAndStringRenderTheBuildIdentity pins both renderings: Short is the
// version alone, and String carries all three fields plus the toolchain.
func TestShortAndStringRenderTheBuildIdentity(t *testing.T) {
	t.Parallel()

	if got := version.Short(); got != version.Version {
		t.Errorf("Short() = %q, want Version %q", got, version.Version)
	}

	got := version.String()
	for _, want := range []string{
		version.Version,
		"commit " + version.Commit,
		"built " + version.Date,
		runtime.Version(),
		runtime.GOOS + "/" + runtime.GOARCH,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}

// TestDockerfileStampsTheBuildMetadata pins the other half of the contract:
// the Dockerfile's -X flags name exactly the package under test. The linker
// ignores an -X that matches no symbol, so a reworded path would silently
// degrade every image to the fallbacks.
func TestDockerfileStampsTheBuildMetadata(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	for _, symbol := range []string{"Version", "Commit", "Date"} {
		want := fmt.Sprintf("-X product-search-service/internal/version.%s=$%s", symbol, strings.ToUpper(symbol))
		if !strings.Contains(string(data), want) {
			t.Errorf("Dockerfile does not pass %q; the image would report the fallbacks", want)
		}
	}
}
