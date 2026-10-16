// Command versioncheck exists only for internal/shared/version's test. It
// prints the three ldflags-injected build identity values, so the test can
// prove the symbol paths the Makefile and CI pass to -X match the package's
// variables.
package main

import (
	"fmt"

	"gogo/internal/shared/version"
)

func main() {
	fmt.Printf("%s %s %s\n", version.Version, version.Commit, version.BuildDate)
}
