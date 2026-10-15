// Package version carries the build-time service version. It is injected by
// the linker at container build time (`-X gogo/internal/shared/version.Version=...`)
// and falls back to a dev placeholder for local builds.
package version

// Version is the built service version, set via ldflags. Keep it vars so it
// can be overwritten by `go build -ldflags "-X"`.
var (
	// Version is the semantic version of this build.
	Version = "dev"

	// Commit is the git revision the binary was built from.
	Commit = "unknown"

	// BuildDate is the RFC3339 timestamp of the build.
	BuildDate = "unknown"
)
