// Command teamcatalog validates the architecture ownership catalog without
// adding a YAML or schema-validation dependency to the application.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type catalog struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Scope      string     `json:"scope"`
	Teams      []team     `json:"teams"`
	Units      []unit     `json:"units"`
	Contracts  []contract `json:"contracts"`
}

type team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type unit struct {
	Path       string          `json:"path"`
	Kind       string          `json:"kind"`
	Team       string          `json:"team"`
	Tier       string          `json:"tier"`
	Context    json.RawMessage `json:"context"`
	Complexity string          `json:"complexity"`
	Rung       int             `json:"rung"`
}

type contract struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Proto       string   `json:"proto"`
	Service     string   `json:"service"`
	Provider    string   `json:"provider"`
	Consumers   []string `json:"consumers"`
	Breaking    string   `json:"breaking"`
	Interaction string   `json:"interaction"`
}

var serviceDeclaration = regexp.MustCompile(`(?m)^\s*service\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
var goPackageOption = regexp.MustCompile(`(?m)^\s*option\s+go_package\s*=\s*"([^"]+)"\s*;`)
var teamIDPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_-]*$`)

func main() {
	catalogPath := flag.String("catalog", "internal/architecture/team_catalog.json", "path to the team catalog")
	repoRoot := flag.String("repo-root", "../..", "repository root, relative to the current directory")
	flag.Parse()

	if err := validate(*repoRoot, *catalogPath); err != nil {
		fmt.Fprintln(os.Stderr, "teamcatalog:", err)
		os.Exit(1)
	}
	fmt.Println("team catalog is valid")
}

func validate(repoRoot, catalogPath string) error {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", catalogPath, err)
	}

	var c catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return fmt.Errorf("decode %s: %w", catalogPath, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: multiple JSON values", catalogPath)
		}
		return fmt.Errorf("decode %s: trailing data: %w", catalogPath, err)
	}
	if c.APIVersion != "gogo.dev/v1" || c.Kind != "TeamCatalog" || strings.TrimSpace(c.Scope) == "" {
		return errors.New("apiVersion, kind, or scope is missing or invalid")
	}

	teams := make(map[string]team, len(c.Teams))
	for _, item := range c.Teams {
		if !teamIDPattern.MatchString(item.ID) || strings.TrimSpace(item.Name) == "" || !oneOf(item.Type, "stream-aligned", "platform", "enabling", "complicated-subsystem") {
			return fmt.Errorf("team %q has missing or invalid metadata", item.ID)
		}
		if _, exists := teams[item.ID]; exists {
			return fmt.Errorf("duplicate team id %q", item.ID)
		}
		teams[item.ID] = item
	}
	if len(teams) == 0 {
		return errors.New("catalog has no teams")
	}

	units := make(map[string]unit, len(c.Units))
	unitPrefixes := make(map[string]string, len(c.Units))
	for _, item := range c.Units {
		if item.Path == "" || !oneOf(item.Kind, "module", "page", "composition", "service") ||
			!oneOf(item.Tier, "composition", "business", "platform", "foundation") ||
			!oneOf(item.Complexity, "simple", "complicated", "complex") || item.Rung < 0 || item.Rung > 4 {
			return fmt.Errorf("unit %q has missing or invalid metadata", item.Path)
		}
		if _, exists := teams[item.Team]; !exists {
			return fmt.Errorf("unit %q refers to unknown team %q", item.Path, item.Team)
		}
		if item.Context == nil {
			return fmt.Errorf("unit %q is missing context metadata", item.Path)
		}
		var contextName string
		contextIsNull := bytes.Equal(bytes.TrimSpace(item.Context), []byte("null"))
		if !contextIsNull {
			if err := json.Unmarshal(item.Context, &contextName); err != nil {
				return fmt.Errorf("unit %q context must be a string or null", item.Path)
			}
		}
		if (item.Tier == "business") != (!contextIsNull && strings.TrimSpace(contextName) != "") {
			return fmt.Errorf("unit %q must have a bounded context exactly when tier is business", item.Path)
		}
		if _, exists := units[item.Path]; exists {
			return fmt.Errorf("duplicate unit path %q", item.Path)
		}
		if err := requirePath(root, item.Path, true); err != nil {
			return err
		}
		units[item.Path] = item
		prefix, err := importPrefix(root, item.Path)
		if err != nil {
			return err
		}
		unitPrefixes[item.Path] = prefix
	}
	if len(units) == 0 {
		return errors.New("catalog has no units")
	}

	graph, err := readImportGraph(filepath.Join(root, "apps/web/internal/architecture/import_graph.txt"))
	if err != nil {
		return err
	}
	seenContracts := make(map[string]struct{}, len(c.Contracts))
	for _, item := range c.Contracts {
		if item.ID == "" || item.Kind != "grpc" || item.Service == "" || item.Proto == "" ||
			!oneOf(item.Breaking, "FILE", "PACKAGE", "WIRE_JSON", "WIRE") ||
			!oneOf(item.Interaction, "collaboration", "x-as-a-service", "facilitation") {
			return fmt.Errorf("contract %q has missing or invalid metadata", item.ID)
		}
		if _, exists := seenContracts[item.ID]; exists {
			return fmt.Errorf("duplicate contract id %q", item.ID)
		}
		seenContracts[item.ID] = struct{}{}
		if _, exists := units[item.Provider]; !exists {
			return fmt.Errorf("contract %q refers to unknown provider unit %q", item.ID, item.Provider)
		}
		if len(item.Consumers) == 0 {
			return fmt.Errorf("contract %q has no consumer", item.ID)
		}
		for _, consumer := range item.Consumers {
			if _, exists := units[consumer]; !exists {
				return fmt.Errorf("contract %q refers to unknown consumer unit %q", item.ID, consumer)
			}
		}
		protoPath, err := resolvePath(root, item.Proto)
		if err != nil {
			return err
		}
		proto, err := os.ReadFile(protoPath)
		if err != nil {
			return fmt.Errorf("contract %q proto %q: %w", item.ID, item.Proto, err)
		}
		found := false
		for _, match := range serviceDeclaration.FindAllSubmatch(proto, -1) {
			if string(match[1]) == item.Service {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("contract %q declares service %q absent from %s", item.ID, item.Service, item.Proto)
		}
		goPackageMatch := goPackageOption.FindSubmatch(proto)
		if len(goPackageMatch) != 2 {
			return fmt.Errorf("contract %q has no go_package option in %s", item.ID, item.Proto)
		}
		goPackage := strings.SplitN(string(goPackageMatch[1]), ";", 2)[0]
		if err := validateContractEdges(item, goPackage, unitPrefixes, graph); err != nil {
			return err
		}
	}
	if len(seenContracts) == 0 {
		return errors.New("catalog has no contracts")
	}
	return nil
}

func validateContractEdges(item contract, goPackage string, prefixes map[string]string, graph map[string][]string) error {
	declared := make(map[string]struct{}, len(item.Consumers))
	for _, consumer := range item.Consumers {
		if consumer == item.Provider {
			return fmt.Errorf("contract %q lists its provider as a consumer", item.ID)
		}
		if _, duplicate := declared[consumer]; duplicate {
			return fmt.Errorf("contract %q lists consumer %q more than once", item.ID, consumer)
		}
		declared[consumer] = struct{}{}
	}

	derived := make(map[string]struct{})
	for pkg, imports := range graph {
		usesContract := false
		for _, imported := range imports {
			if imported == goPackage {
				usesContract = true
				break
			}
		}
		if !usesContract {
			continue
		}
		owner := ""
		longest := 0
		for path, prefix := range prefixes {
			if (pkg == prefix || strings.HasPrefix(pkg, prefix+"/")) && len(prefix) > longest {
				owner, longest = path, len(prefix)
			}
		}
		if owner == "" {
			return fmt.Errorf("contract %q is imported by %s, which is outside this catalog scope", item.ID, pkg)
		}
		derived[owner] = struct{}{}
	}
	if _, found := derived[item.Provider]; !found {
		return fmt.Errorf("contract %q provider %q does not import generated package %s", item.ID, item.Provider, goPackage)
	}
	delete(derived, item.Provider)
	for consumer := range derived {
		if _, found := declared[consumer]; !found {
			return fmt.Errorf("contract %q has undeclared consumer %q derived from import_graph.txt", item.ID, consumer)
		}
	}
	for consumer := range declared {
		if _, found := derived[consumer]; !found {
			return fmt.Errorf("contract %q declares consumer %q with no matching edge in import_graph.txt", item.ID, consumer)
		}
	}
	return nil
}

func importPrefix(root, unitPath string) (string, error) {
	unitDir := filepath.Join(root, filepath.FromSlash(unitPath))
	for dir := unitDir; ; dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "module" {
					rel, err := filepath.Rel(dir, unitDir)
					if err != nil {
						return "", fmt.Errorf("resolve import prefix for %q: %w", unitPath, err)
					}
					return strings.TrimSuffix(fields[1]+"/"+filepath.ToSlash(rel), "/."), nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read go.mod while resolving %q: %w", unitPath, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir || (parent != root && !strings.HasPrefix(parent, root+string(filepath.Separator))) {
			break
		}
	}
	return "", fmt.Errorf("no go.mod found for unit %q", unitPath)
}

func readImportGraph(filename string) (map[string][]string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read import graph %s: %w", filename, err)
	}
	graph := make(map[string][]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pkg, imports, found := strings.Cut(line, "\t")
		if !found {
			graph[line] = nil
			continue
		}
		graph[pkg] = strings.Split(imports, ",")
	}
	if len(graph) == 0 {
		return nil, errors.New("import graph is empty")
	}
	return graph, nil
}

func requirePath(root, rel string, directory bool) error {
	full, err := resolvePath(root, rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("catalog path %q: %w", rel, err)
	}
	if directory && !info.IsDir() {
		return fmt.Errorf("catalog unit %q is not a directory", rel)
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("catalog path %q is not a file", rel)
	}
	return nil
}

func resolvePath(root, rel string) (string, error) {
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("catalog path %q must be a clean repository-relative path", rel)
	}
	full := filepath.Join(root, rel)
	if !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("catalog path %q escapes the repository root", rel)
	}
	return full, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
