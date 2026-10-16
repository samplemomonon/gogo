package architecture_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// OD-1: the enforcement mechanism, decided.
//
// # What was decided, and what was rejected
//
// The open decision proposed replacing this service's one-item denylist with a
// fail-closed `depguard` strict allowlist plus a `forbidigo` rule on
// `^sql\.(Open|OpenDB)$`, imported through golangci-lint. The dependency is not
// taken. What replaces it is this file.
//
// The argument against the proposal is not that it is wrong — `depguard`'s strict
// mode genuinely is fail-closed, and golangci-lint does ship it — but that an
// allowlist is a hand-maintained list, and this repository has a documented
// record of what happens to hand-maintained lists here:
//
//   - A draft architecture test denied ~19 database and cache module roots,
//     written from memory. On audit, not one could be corroborated as present in
//     any published composition layer. None of them existed.
//   - 44 BUILD.bazel files outlived the build system that read them. Nothing
//     invoked bazel; there is no WORKSPACE or MODULE.bazel.
//   - The import graph derived from those files had rotted to 43 of 119 packages
//     while its own staleness gate was failing.
//
// Three lists, three failures, one cause: a list that is not derived from
// something cannot stay true. `depguard`'s allowlist is not derived from
// anything — someone edits YAML when a dependency is added. So it would not
// fail the way the previous three did, exactly; it would fail by becoming a
// permanent pardon nobody revokes.
//
// # The mechanism: a ratchet, not an allowlist
//
// Craig Johnston's formulation is the one this adopts: an allowlist entry has to
// expire, and equality has to run in *both* directions. A checked-in mirror of
// the graph where the test fails on additions **and** on stale entries — "an
// allowlist that only grows is how a coupling you deliberately removed gets
// pre-approved the next time an agent reaches for it."
//
// That is what this is. `import_ratchet.txt` is a mirror of this module's
// first-party import edges. It is not an allowlist permitting things; it is a
// record of what the architecture currently is, in a form whose diff is the
// review.
//
// Why this beats the status quo it replaces: `forbiddenImports` in
// architecture_test.go is a denylist of one. It cannot notice a new *first-party*
// edge at all — `graph/` importing a store package would pass every rule in this
// package, because none of them are about first-party coupling. The ratchet
// notices, because the new edge is simply not in the file.
//
// Why it beats a strict third-party allowlist for the risk that actually
// mattered: OD-1 was raised about a new database driver arriving unchallenged.
// That risk is already closed, and closed better, by the `database/sql` choke
// point in architecture_test.go — every pure-Go SQL path funnels through
// `sql.Open`, present and future, so a new driver has nowhere to enter that a
// third-party denylist would have had to name. `forbidigo` on the same two
// function names would have re-implemented, with a new toolchain dependency, a
// check that one import line already performs.
//
// # What this does not do
//
// It does not describe the architecture. It freezes it. A rule that says "domain
// must not import adapters" is a judgement; this file makes no judgement at all,
// it only notices change. The judgement rules stay where they are, in
// architecture_test.go. If this file is ever the only thing protecting a
// boundary, that boundary was never really being enforced — someone should read
// the diff and notice that the new edge was recorded instead of designed.

// ratchetFile is the committed mirror of this module's first-party import edges.
//
// It lives beside this file rather than at the module root, matching the root
// repository's import_graph.txt. It resolves relative to the test's working
// directory, which `go test` sets to the package's source directory.
const ratchetFile = "import_ratchet.txt"

// ratchetPath returns the mirror's location.
func ratchetPath(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Join(dir, ratchetFile)
}

// updateRatchet rewrites the mirror instead of comparing against it. Registered
// at package scope because testing.Main parses flags before any test runs.
var updateRatchet = flag.Bool("update-ratchet", false,
	"rewrite "+ratchetFile+" from the working tree instead of failing on differences")

// ratchetEdge is one first-party import, rendered "importer -> importee".
type ratchetEdge struct{ from, to string }

func (e ratchetEdge) String() string { return e.from + " -> " + e.to }

// deriveRatchet returns every direct import this module's production code makes
// to another of its own packages, sorted.
//
// Direct edges, not the transitive closure: the transitive set is dominated by
// `gen/…`, which changes whenever a contract is regenerated, and an architecture
// mirror that churns on codegen stops being read.
//
// Production code only, for the same reason the root repository's graph excludes
// test imports: tests may bridge worlds, production code may not.
func deriveRatchet(t *testing.T) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleRoot(t)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("piping go list: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting go list: %v", err)
	}

	var edges []string
	dec := json.NewDecoder(stdout)
	for {
		var pkg struct {
			ImportPath string
			Imports    []string
			// ForTest marks the synthetic variant `go list` emits for a package,
			// which carries the test files' imports under the same ImportPath.
			// Without this filter a test-only import would be ratcheted as if
			// production code depended on it.
			ForTest string
		}
		if err := dec.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			cmd.Wait()
			t.Fatalf("decoding go list output: %v", err)
		}
		if pkg.ForTest != "" || !strings.HasPrefix(pkg.ImportPath, modulePrefix) {
			continue
		}
		for _, dep := range pkg.Imports {
			if strings.HasPrefix(dep, modulePrefix) {
				edges = append(edges, ratchetEdge{from: pkg.ImportPath, to: dep}.String())
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("go list: %v", err)
	}

	sort.Strings(edges)

	// A guard on the guard, for the reason given in TestAggregatorOwnsNoDatastore:
	// every assertion here is phrased "this must not appear", so a walk that
	// found nothing would make the test unable to fail.
	if len(edges) == 0 {
		t.Fatal("the walk found no first-party import edges, so the ratchet asserts nothing — " +
			"either this module genuinely has no internal coupling, or the derivation is broken")
	}
	return edges
}

// TestImportRatchetIsCurrent compares the committed mirror against the graph.
//
// Fails on two conditions, and the second is the one that matters most:
//
//   - an edge exists that the mirror does not record → a new coupling;
//   - the mirror records an edge that no longer exists → a stale entry, which
//     pre-approves reintroducing coupling somebody deliberately removed.
//
// Either way the remedy is the same and it is cheap: regenerate and let the
// reviewer read the diff.
func TestImportRatchetIsCurrent(t *testing.T) {
	derived := deriveRatchet(t)
	path := ratchetPath(t)

	if *updateRatchet {
		body := "# First-party import edges of this module, as `importer -> importee`.\n" +
			"# Generated by: go test ./internal/architecture -args -update-ratchet\n" +
			"#\n" +
			"# A mirror, not an allowlist. TestImportRatchetIsCurrent fails on edges added\n" +
			"# AND on edges removed, so a stale entry cannot pre-approve a coupling that was\n" +
			"# deliberately undone. See ratchet_test.go for why a ratchet was chosen over a\n" +
			"# depguard strict allowlist (OD-1).\n"
		for _, edge := range derived {
			body += edge + "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", ratchetFile, err)
		}
		t.Logf("wrote %d edges to %s", len(derived), ratchetFile)
		return
	}

	committed, err := readRatchet(t, path)
	if err != nil {
		t.Fatalf("%v\n  regenerate with: go test ./internal/architecture -args -update-ratchet", err)
	}

	recorded := make(map[string]bool, len(committed))
	for _, edge := range committed {
		recorded[edge] = true
	}

	present := make(map[string]bool, len(derived))
	for _, edge := range derived {
		present[edge] = true
	}

	var added, stale []string
	for _, edge := range derived {
		if !recorded[edge] {
			added = append(added, edge)
		}
	}
	for _, edge := range committed {
		if !present[edge] {
			stale = append(stale, edge)
		}
	}
	sort.Strings(added)
	sort.Strings(stale)

	if len(added) > 0 {
		t.Errorf("new first-party import edge(s) absent from %s:\n  %s\n\n"+
			"  This service is a composition layer. A new edge between these two packages is an "+
			"architectural change, not a refactor, and it is recorded here so a reviewer reads it "+
			"as one.\n"+
			"  If it is intended: regenerate with\n"+
			"    go test ./internal/architecture -args -update-ratchet\n"+
			"  and say in the PR what the edge means.",
			ratchetFile, strings.Join(added, "\n  "))
	}

	if len(stale) > 0 {
		t.Errorf("edge(s) recorded in %s no longer exist:\n  %s\n\n"+
			"  A stale entry is not harmless bookkeeping. It pre-approves reintroducing a coupling "+
			"that was deliberately removed — the next change that reaches for it passes without a "+
			"decision being made. This is the failure mode that makes allowlists decay.\n"+
			"  Regenerate with\n"+
			"    go test ./internal/architecture -args -update-ratchet",
			ratchetFile, strings.Join(stale, "\n  "))
	}
}

// readRatchet parses the committed mirror, ignoring comments and blank lines.
func readRatchet(t *testing.T, path string) ([]string, error) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s is missing", ratchetFile)
		}
		return nil, err
	}

	var edges []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		edges = append(edges, line)
	}
	if len(edges) == 0 {
		return nil, fmt.Errorf("%s records no edges, so it asserts nothing", ratchetFile)
	}
	return edges, nil
}
