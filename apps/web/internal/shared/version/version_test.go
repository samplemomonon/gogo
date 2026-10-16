// Package version_test pins the build-identity contract: a local build reports
// the documented placeholders, and the -X ldflags symbol paths the Makefile and
// the container build pass really target this package's variables.
package version_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gogo/internal/shared/version"
)

// ldflags is the -X contract the Makefile passes as $(LDFLAGS). The probe
// binary is compiled with exactly these flags, so a renamed variable or
// package shows up here instead of as "dev/unknown" in a release.
const ldflags = "-X gogo/internal/shared/version.Version=1.2.3 " +
	"-X gogo/internal/shared/version.Commit=abc1234 " +
	"-X gogo/internal/shared/version.BuildDate=2026-01-01T00:00:00Z"

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
		{"BuildDate", version.BuildDate, "unknown"},
	} {
		if tt.got == "" {
			t.Errorf("version.%s is empty; it must fall back to a placeholder", tt.name)
		}
		if tt.got != tt.want {
			t.Errorf("version.%s = %q, want the documented fallback %q", tt.name, tt.got, tt.want)
		}
	}
}

// TestLDFlagsOverrideTheFallbacks proves the linker contract end to end: the
// testdata probe prints the three variables, is built with the same -X flags
// the Makefile uses, and must report the injected values — not the fallbacks.
func TestLDFlagsOverrideTheFallbacks(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH; cannot compile the ldflags probe")
	}

	binary := filepath.Join(t.TempDir(), "versioncheck")
	build := exec.Command(goTool, "build", "-ldflags", ldflags, "-o", binary, "./testdata/versioncheck")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./testdata/versioncheck: %v\n%s", err, out)
	}

	out, err := exec.Command(binary).Output()
	if err != nil {
		t.Fatalf("run versioncheck: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "1.2.3 abc1234 2026-01-01T00:00:00Z" {
		t.Errorf("versioncheck printed %q, want the ldflags-injected build identity", got)
	}
}
