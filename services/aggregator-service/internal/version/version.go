// Package version exposes build metadata. The values are overridden at link
// time by the build system, for example:
//
//	go build -ldflags "-X aggregator-service/internal/version.Version=1.2.3"
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version of the build.
	Version = "dev"

	// Commit is the source revision the binary was built from.
	Commit = "unknown"

	// Date is the build timestamp in RFC 3339 format.
	Date = "unknown"
)

// Short returns just the version number.
func Short() string {
	return Version
}

// String returns a human readable description of the build.
func String() string {
	return fmt.Sprintf(
		"%s (commit %s, built %s, %s, %s/%s)",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH,
	)
}
