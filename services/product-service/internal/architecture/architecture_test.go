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

// architecture_test pins the dependency boundaries of product-service.
//
//   1. The composition root (cmd/server) may depend on everything.
//   2. Internal packages must not import the composition root.
//   3. The domain layer (internal/domain) must not import any internal package
//      except ports (which define its interfaces).
//   4. The use case layer (internal/usecase) must only depend on domain and ports.
//   5. No cycles between internal packages.

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
	switch {
	case strings.HasPrefix(pkgPath, "product-service/cmd/"):
		return "composition"
	case strings.HasPrefix(pkgPath, "product-service/internal/domain"):
		return "domain"
	case strings.HasPrefix(pkgPath, "product-service/internal/usecase"):
		return "usecase"
	case strings.HasPrefix(pkgPath, "product-service/internal/ports"):
		return "ports"
	case strings.HasPrefix(pkgPath, "product-service/internal/config"):
		return "config"
	case strings.HasPrefix(pkgPath, "product-service/internal/database"),
		strings.HasPrefix(pkgPath, "product-service/internal/outbox"),
		strings.HasPrefix(pkgPath, "product-service/internal/messaging"),
		strings.HasPrefix(pkgPath, "product-service/internal/adapter"),
		strings.HasPrefix(pkgPath, "product-service/internal/di"),
		strings.HasPrefix(pkgPath, "product-service/internal/version"),
		strings.HasPrefix(pkgPath, "product-service/internal/event"):
		return "infrastructure"
	default:
		return "external"
	}
}

func TestImportBoundaries(t *testing.T) {
	graph := loadGraph(t)

	var violations []string
	for importer, deps := range graph {
		importerClass := classify(importer)
		for _, dep := range deps {
			depClass := classify(dep)

			if importerClass == "composition" {
				continue
			}

			if depClass == "composition" {
				violations = append(violations,
					importer+" -> "+dep+" (internal imports composition root)")
				continue
			}

			if importerClass == "domain" && depClass != "external" && depClass != "ports" {
				violations = append(violations,
					importer+" -> "+dep+" (domain imports internal package)")
				continue
			}

			if importerClass == "usecase" && depClass != "domain" && depClass != "ports" && depClass != "external" {
				violations = append(violations,
					importer+" -> "+dep+" (usecase imports non-domain/non-port)")
				continue
			}

			if importerClass == "ports" && depClass != "domain" && depClass != "external" {
				violations = append(violations,
					importer+" -> "+dep+" (ports imports non-domain)")
				continue
			}
		}
	}

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
	visited := make(map[string]int)
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
	graph := loadGraph(t)
	if len(graph) < 10 {
		t.Errorf("only %d packages in graph, expected at least 10", len(graph))
	}
}
