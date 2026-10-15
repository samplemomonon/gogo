package architecture_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// architecture_test pins the dependency boundaries of category-service so they
// cannot silently decay. Every rule here is stated, with its rationale, in the
// workspace's ARCHITECTURE.md.
//
// This test derives the import graph by running `go list -json` on all
// packages in the module, then asserts the Clean Architecture dependency rule:
//
//   1. The composition root (cmd/server) may depend on everything.
//   2. Internal packages must not import the composition root.
//   3. The application layer (internal/service) must not import infrastructure
//      (internal/repository, internal/database, internal/grpcsrv, internal/handler).
//   4. The domain layer (internal/model) must not import any internal package.
//   5. No cycles between internal packages.
//
// If a rule is violated, the test fails with the specific edge.

type pkg struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
	Dir        string   `json:"Dir"`
}

func loadGraph(t *testing.T) map[string][]string {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = findModuleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -json: %v\n%s", err, out)
	}

	graph := make(map[string][]string)
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list: %v", err)
		}
		if p.ImportPath == "" {
			continue
		}
		graph[p.ImportPath] = p.Imports
	}
	return graph
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root")
		}
		dir = parent
	}
}

func classify(pkgPath string) string {
	if strings.HasPrefix(pkgPath, "category-service/cmd/") {
		return "composition"
	}
	if strings.HasPrefix(pkgPath, "category-service/internal/service") ||
		strings.HasPrefix(pkgPath, "category-service/internal/apperror") {
		return "application"
	}
	if strings.HasPrefix(pkgPath, "category-service/internal/model") ||
		strings.HasPrefix(pkgPath, "category-service/internal/version") {
		return "domain"
	}
	if strings.HasPrefix(pkgPath, "category-service/internal/repository") ||
		strings.HasPrefix(pkgPath, "category-service/internal/database") ||
		strings.HasPrefix(pkgPath, "category-service/internal/grpcsrv") ||
		strings.HasPrefix(pkgPath, "category-service/internal/handler") ||
		strings.HasPrefix(pkgPath, "category-service/internal/httpx") ||
		strings.HasPrefix(pkgPath, "category-service/internal/config") {
		return "infrastructure"
	}
	if strings.HasPrefix(pkgPath, "category-service/internal/") {
		return "internal"
	}
	return "external"
}

func TestImportBoundaries(t *testing.T) {
	graph := loadGraph(t)

	var violations []string
	for importer, deps := range graph {
		importerClass := classify(importer)
		for _, dep := range deps {
			depClass := classify(dep)

			// Composition root may import anything.
			if importerClass == "composition" {
				continue
			}

			// Internal packages must not import composition root.
			if depClass == "composition" {
				violations = append(violations,
					importer+" -> "+dep+" (internal imports composition root)")
				continue
			}

			// Application must not import infrastructure.
			if importerClass == "application" && depClass == "infrastructure" {
				violations = append(violations,
					importer+" -> "+dep+" (application imports infrastructure)")
				continue
			}

			// Domain must not import any internal package.
			if importerClass == "domain" && depClass != "external" {
				violations = append(violations,
					importer+" -> "+dep+" (domain imports internal package)")
				continue
			}

			// Internal packages must not import other internal packages in a cycle.
			// We catch cycles with a separate check.
		}
	}

	// Cycle detection.
	cycles := findCycles(graph)
	for _, cycle := range cycles {
		violations = append(violations,
			"cycle: "+strings.Join(cycle, " -> "))
	}

	if len(violations) > 0 {
		t.Errorf("architecture boundary violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func findCycles(graph map[string][]string) [][]string {
	visited := make(map[string]int) // 0=unvisited, 1=visiting, 2=done
	path := []string{}
	var cycles [][]string

	var dfs func(string)
	dfs = func(u string) {
		visited[u] = 1
		path = append(path, u)
		for _, v := range graph[u] {
			state := visited[v]
			if state == 0 {
				dfs(v)
			} else if state == 1 {
				// Found a cycle.
				idx := -1
				for i, p := range path {
					if p == v {
						idx = i
						break
					}
				}
				if idx >= 0 {
					cycle := append(path[idx:], v)
					cycles = append(cycles, cycle)
				}
			}
		}
		path = path[:len(path)-1]
		visited[u] = 2
	}

	for u := range graph {
		if visited[u] == 0 {
			dfs(u)
		}
	}
	return cycles
}

func TestParserIsHonest(t *testing.T) {
	// Spot-check: graph should contain the expected number of packages.
	graph := loadGraph(t)
	// gen/categorypb has no internal imports; internal/architecture is the test itself.
	if len(graph) < 13 {
		t.Errorf("only %d packages in graph, expected at least 13", len(graph))
	}
}
