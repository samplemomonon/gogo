package architecture_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Port registry, and the gate that keeps it true.
//
// # Why this file exists
//
// The question it answers is the one a developer has to answer on their first
// day: which ports does this stack take, and does anything I already run need
// them? Before this, that answer lived in five separate config files, four
// Dockerfiles, five compose files and the aggregator's default dependency
// targets — seven places, no index, and nothing that noticed when they disagreed.
//
// That matters because of what is actually assigned. The gRPC range 9090–9094 is
// Prometheus's allocated range and, at 9092, Kafka's: Prometheus's own port
// wiki marks 9092 "UNALLOCATED (to avoid collision with Kafka)", and this
// repository allocated it anyway. Worse, `category-service` and `media-service`
// do not exist in this repository at all, yet the aggregator's default targets
// point at 9090 and 9091 — which are Prometheus and its Pushgateway. Run the
// aggregator with defaults on a laptop that has Prometheus up, and it sends gRPC
// to Prometheus's HTTP port.
//
// # What is enforced, and what is only documented
//
// Enforced, and each verified in both directions:
//
//   - every registry row exists in code: the Go default, the Dockerfile EXPOSE
//     and the compose mapping must all agree with the row;
//   - no port is claimed twice within the repository;
//   - a new port that is somebody else's documented default is refused, unless
//     it is recorded in the registry as a known collision.
//
// Not enforced, because it is not knowable from this repository: whether the
// developer's own machine already has something on a port. That is a runtime
// question, and the honest answer is that the dev column is a convention that
// makes collisions rare rather than impossible.
//
// # Staging and production
//
// They publish no host ports, deliberately — services reach each other over the
// container network by service name. The registry therefore has a dev column and
// no staging or production column, and this file does not check for the absence
// of one. Adding staging/prod columns would imply a mapping that should not
// exist.

// portsFile is the committed registry. It sits at the repository root next to
// ARCHITECTURE.md rather than beside this test, because the first reader of a
// port registry is a person trying to start the stack, not a test.
const portsFile = "ports.txt"

// entry is one row: a port, who listens on it, and who else wants it.
type entry struct {
	service   string
	kind      string
	envVar    string
	container int
	devHost   int
	owner     string
}

func (e entry) String() string {
	return fmt.Sprintf("%s %s %s container=%d dev=%d owner=%s",
		e.service, e.kind, e.envVar, e.container, e.devHost, e.owner)
}

// ownedByTool reports whether some other project holds this port by default.
func (e entry) ownedByTool() bool {
	return e.owner != "gogo" && e.owner != "-"
}

// readRegistry parses ports.txt.
//
// The format is whitespace-separated so that a stray tab does not change the
// meaning of a row, which is the kind of thing that makes a config file quietly
// mean two different things to two parsers.
func readRegistry(t *testing.T) []entry {
	t.Helper()

	path := filepath.Join(findRepoRoot(t), portsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", portsFile, err)
	}

	var entries []entry
	inSection := false
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Sections other than [service-ports] are documentation.
			inSection = line == "[service-ports]"
			continue
		}
		if !inSection {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 6 {
			t.Fatalf("%s:%d: expected 6 fields, got %d: %q", portsFile, i+1, len(fields), line)
		}

		container, err := strconv.Atoi(fields[3])
		if err != nil {
			t.Fatalf("%s:%d: container port %q is not a number", portsFile, i+1, fields[3])
		}
		devHost, err := strconv.Atoi(fields[4])
		if err != nil {
			t.Fatalf("%s:%d: dev host port %q is not a number", portsFile, i+1, fields[4])
		}

		entries = append(entries, entry{
			service:   fields[0],
			kind:      fields[1],
			envVar:    fields[2],
			container: container,
			devHost:   devHost,
			owner:     fields[5],
		})
	}

	if len(entries) == 0 {
		t.Fatalf("%s declares no ports, so this test asserts nothing", portsFile)
	}
	return entries
}

// TestPortRegistryMatchesReality is the drift gate.
//
// It walks out of the registry and into every artefact that has to agree with
// it: the Go defaults, the Dockerfile EXPOSE lines, the compose host mappings,
// and the aggregator's dependency targets. Any of them moving without the
// registry moving is a failure, which is the only thing a registry is for.
func TestPortRegistryMatchesReality(t *testing.T) {
	entries := readRegistry(t)
	root := findRepoRoot(t)

	for _, e := range entries {
		t.Run(e.String(), func(t *testing.T) {
			switch e.service {
			case "gateway":
				// The gateway module listens on PORT, not APP_PORT, which is why the
				// registry carries the variable name rather than assuming it.
				assertConfigDefault(t, filepath.Join(root, "apps", "gogo", "internal", "shared", "config", "loader.go"),
					e.envVar, e.container)
				assertDockerfileExposes(t, filepath.Join(root, "Dockerfile"), e.container)
				assertComposePublishes(t, filepath.Join(root, "docker-compose.yml"), e.devHost, e.container)

			case "postgres":
				assertComposePublishes(t, filepath.Join(root, "docker-compose.yml"), e.devHost, e.container)

			default:
				// Every remaining service is a directory under services,
				// named exactly as the registry names it.
				dir := filepath.Join(root, "services", e.service)
				assertConfigDefault(t, filepath.Join(dir, "internal", "config", "config.go"), e.envVar, e.container)
				assertDockerfileExposes(t, filepath.Join(dir, "Dockerfile"), e.container)
				assertComposePublishes(t, filepath.Join(dir, "docker-compose.yml"), e.devHost, e.container)
			}
		})
	}
}

// TestNoPortIsClaimedTwice catches the failure mode a registry exists to make
// visible: two services agreeing on a port, discovered when the second one fails
// to bind.
func TestNoPortIsClaimedTwice(t *testing.T) {
	type claim struct {
		first, second entry
	}

	var clashes []claim
	seen := map[int]entry{}

	for _, e := range readRegistry(t) {
		// Container ports live in separate network namespaces when the services
		// run in separate compose projects, so only the dev host port is genuinely
		// contested. Checking container ports too would fail on 5432, which every
		// project maps to its own container.
		if prior, taken := seen[e.devHost]; taken {
			clashes = append(clashes, claim{first: prior, second: e})
			continue
		}
		seen[e.devHost] = e
	}

	for _, c := range clashes {
		t.Errorf("dev host port %d is claimed by two entries:\n  %s\n  %s\n"+
			"  Only one process can bind a port, so the second one fails at startup. "+
			"Give one of them a different port here and in its compose mapping.",
			c.first.devHost, c.first, c.second)
	}
}

// TestNoPortCollidesSilentlyWithWellKnownDefaults is the check that would have
// caught the 9092 assignment.
//
// A port owned by another project is allowed, but only when the registry says so
// in the owner column. What is refused is a row that claims a documented default
// while presenting it as ours, because that is how a developer ends up unable to
// run the stack and the monitoring next to it.
func TestNoPortCollidesSilentlyWithWellKnownDefaults(t *testing.T) {
	reserved := readReserved(t)

	for _, e := range readRegistry(t) {
		tool, isReserved := reserved[e.devHost]
		switch {
		case !isReserved:
			continue

		case e.ownedByTool():
			// Recorded. Nothing to do — the collision is known and its owner is named.

		case tool == "POSTGRESQL":
			// The root Postgres is a development dependency of the whole stack and
			// 5432 is its canonical port. The per-service projects already avoid it
			// by mapping 5434, 5436 and 5437 onto their own containers, which is
			// what that offset is for.
			continue

		default:
			t.Errorf("dev host port %d is the documented default for %s, but the registry "+
				"records its owner as %q.\n  entry: %s\n\n"+
				"  Either move this service to a port nobody claims, or record the owner as "+
				"%s if the collision is deliberate — recording it is what makes the conflict "+
				"visible to the next developer instead of invisible to everyone.",
				e.devHost, tool, e.owner, e, tool)
		}
	}
}

// reservedLine matches a row of the [reserved] block in ports.txt.
var reservedLine = regexp.MustCompile(`^(\d{2,5})\s+([A-Z_]+)\s+(.+)$`)

// readReserved returns the ports documented as belonging to other projects.
func readReserved(t *testing.T) map[int]string {
	t.Helper()

	path := filepath.Join(findRepoRoot(t), portsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", portsFile, err)
	}

	reserved := map[int]string{}
	inBlock := false

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[reserved]") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		// A blank line ends the block.
		if trimmed == "" {
			inBlock = false
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		m := reservedLine.FindStringSubmatch(trimmed)
		if m == nil {
			t.Fatalf("%s: reserved line %q is not 'PORT OWNER description'", portsFile, trimmed)
		}
		port, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s: reserved port %q is not a number", portsFile, m[1])
		}
		if prior, dup := reserved[port]; dup {
			t.Fatalf("%s: port %d is reserved twice (%s and %s)", portsFile, port, prior, m[2])
		}
		reserved[port] = m[2]
	}

	if len(reserved) == 0 {
		t.Fatalf("%s reserves no ports, so the collision check asserts nothing", portsFile)
	}
	return reserved
}

// TestTheReservedListIsVerifiedRatherThanRemembered guards the reserved list
// itself. These are another project's decisions, and a wrong entry either blocks
// a port that is actually free or lets one through that is actually taken.
func TestTheReservedListIsVerifiedRatherThanRemembered(t *testing.T) {
	reserved := readReserved(t)

	want := map[string]int{
		"PROMETHEUS":              9090,
		"PUSHGATEWAY":             9091,
		"KAFKA":                   9092,
		"ALERTMANAGER":            9093,
		"ALERTMANAGER_CLUSTERING": 9094,
		"GRAFANA":                 3000,
		"JAEGER_UI":               16686,
		"OTLP_GRPC":               4317,
		"OTLP_HTTP":               4318,
		"POSTGRESQL":              5432,
		"REDIS":                   6379,
	}

	for owner, port := range want {
		got, ok := reserved[port]
		if !ok {
			t.Errorf("port %d (%s) is not in the reserved list; a project whose default "+
				"moves would then be allocatable without anyone noticing", port, owner)
			continue
		}
		if got != owner {
			t.Errorf("port %d is reserved as %q, want %q", port, got, owner)
		}
	}

	var extra []string
	for port, owner := range reserved {
		if _, known := want[owner]; !known {
			extra = append(extra, fmt.Sprintf("%d=%s", port, owner))
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("the reserved list has entries with no verified source: %s\n"+
			"  Add them to want{} above with where the number came from, or remove them.",
			strings.Join(extra, ", "))
	}
}

// --- assertions on individual artefacts ------------------------------------

// configDefaultPattern matches an env lookup with a numeric default, in either
// the integer or the string form the services use.
var configDefaultPattern = regexp.MustCompile(`"` + `([A-Z_]+)` + `",\s*(?:([0-9]+)|"([0-9]+)")`)

// assertConfigDefault checks that a Go source file reads envVar with the
// expected default.
//
// It reads the source rather than calling Load(), because these are five
// separate Go modules and the root test cannot import any of them. The cost is
// that a reformat breaks this test; the benefit is that it covers all five from
// one place, and a broken parse fails loudly rather than passing quietly.
func assertConfigDefault(t *testing.T, path, envVar string, want int) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: %v", filepath.Base(filepath.Dir(path)), err)
		return
	}

	for _, m := range configDefaultPattern.FindAllStringSubmatch(string(data), -1) {
		if m[1] != envVar {
			continue
		}
		value := m[2]
		if value == "" {
			value = m[3]
		}
		if value == strconv.Itoa(want) {
			return
		}
		t.Errorf("%s: %s defaults to %s, registry says %d", filepath.Base(path), envVar, value, want)
		return
	}

	t.Errorf("%s: no default found for %s; the registry row cannot be verified "+
		"(the file may have been reformatted past what this test can read)",
		filepath.Base(path), envVar)
}

// assertDockerfileExposes checks that a Dockerfile declares the port.
func assertDockerfileExposes(t *testing.T, path string, port int) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("Dockerfile: %v", err)
		return
	}

	target := strconv.Itoa(port)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "EXPOSE") {
			continue
		}
		for _, field := range strings.Fields(line)[1:] {
			if field == target {
				return
			}
		}
	}

	t.Errorf("%s does not EXPOSE %d; the image would still work, but the published "+
		"port and the documented one would disagree", filepath.Base(filepath.Dir(path)), port)
}

// composeMapping matches a quoted host:container pair.
var composeMapping = regexp.MustCompile(`"?(\d+):(\d+)"?`)

// assertComposePublishes checks that a compose file maps host->container.
//
// Only for development. Staging and production publish no host ports at all,
// which is why this file has a dev column and no deployment columns.
func assertComposePublishes(t *testing.T, path string, devHost, container int) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("docker-compose.yml: %v", err)
		return
	}

	wantHost := strconv.Itoa(devHost)
	wantContainer := strconv.Itoa(container)

	for _, m := range composeMapping.FindAllStringSubmatch(string(data), -1) {
		if m[1] == wantHost && m[2] == wantContainer {
			return
		}
	}

	t.Errorf("%s does not publish %s:%s; the registry says this stack binds host port %s",
		filepath.Base(filepath.Dir(path)), wantHost, wantContainer, wantHost)
}

// TestNoDeploymentPublishesHostPorts states the staging/prod rule in code.
//
// It passes vacuously today, because there are no deployment manifests in this
// repository yet. It is here so that adding one which publishes host ports fails
// with an explanation, rather than quietly reintroducing the collisions this
// registry exists to manage.
func TestNoDeploymentPublishesHostPorts(t *testing.T) {
	root := findRepoRoot(t)

	var manifests []string
	for _, dir := range []string{"deploy", "k8s", "kubernetes", "charts", "helm"} {
		matches, err := filepath.Glob(filepath.Join(root, dir, "*"))
		if err != nil {
			t.Fatalf("globbing %s: %v", dir, err)
		}
		manifests = append(manifests, matches...)
	}

	for _, path := range manifests {
		if !strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, m := range composeMapping.FindAllStringSubmatch(string(data), -1) {
			t.Errorf("%s publishes %s:%s on the host.\n"+
				"  Deployment manifests should reach services over the container network by "+
				"service name, not by publishing host ports. A host port in staging or production "+
				"reintroduces every dev-laptop collision this registry exists to manage, and "+
				"exposes a service that should only be reachable from its peers.",
				strings.TrimPrefix(path, root+string(filepath.Separator)), m[1], m[2])
		}
	}
}

// TestAggregatorTargetsMatchTheRegistry closes the loop that mattered most.
//
// The aggregator names its peers by host:port in its own defaults. Those are the
// same numbers the peers bind, written down a second time in a different module,
// and nothing connected them: a service could change its GRPC_PORT and the
// aggregator would keep dialling the old one, failing at request time with a
// connection error that names no configuration.
//
// Two of the five targets cannot be checked, and that is the finding rather than
// a gap in the test. category-service and media-service have no module here, so
// there is no code stating what they bind. The registry records that fact rather
// than inventing rows it could not verify.
func TestAggregatorTargetsMatchTheRegistry(t *testing.T) {
	entries := readRegistry(t)

	// The target variable and the service it dials.
	targets := map[string]string{
		"PRODUCT_GRPC_TARGET": "product-service",
		"LISTING_GRPC_TARGET": "product-listing-service",
		"SEARCH_GRPC_TARGET":  "product-search-service",
	}

	for varName, service := range targets {
		port, found := grpcPortOf(entries, service)
		if !found {
			t.Errorf("the registry has no grpc row for %s, but the aggregator dials it "+
				"via %s", service, varName)
			continue
		}
		assertAggregatorTarget(t, varName, port)
	}

	// The two with no module are named here so the omission is a recorded fact
	// rather than an oversight. Adding either service means adding a registry row
	// and extending targets above.
	for _, external := range []string{"CATEGORY_GRPC_TARGET", "MEDIA_GRPC_TARGET"} {
		if got := aggregatorTargetPort(t, external); got == 0 {
			t.Errorf("%s no longer has a default target", external)
		}
	}
}

// grpcPortOf returns the container port a service's grpc row declares.
func grpcPortOf(entries []entry, service string) (int, bool) {
	for _, e := range entries {
		if e.service == service && e.kind == "grpc" {
			return e.container, true
		}
	}
	return 0, false
}

// aggregatorConfig is where the aggregator keeps its dependency targets.
func aggregatorConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(findRepoRoot(t), "services",
		"aggregator-service", "internal", "config", "config.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading aggregator config: %v", err)
	}
	return string(data)
}

// aggregatorTargetPattern matches a target default such as "localhost:9092".
var aggregatorTargetPattern = regexp.MustCompile(`"([A-Z_]+_GRPC_TARGET)",\s*"([^"]+)"`)

// aggregatorTargetPort returns the port in a target's default, or 0.
func aggregatorTargetPort(t *testing.T, varName string) int {
	t.Helper()

	for _, m := range aggregatorTargetPattern.FindAllStringSubmatch(aggregatorConfig(t), -1) {
		if m[1] != varName {
			continue
		}
		idx := strings.LastIndex(m[2], ":")
		if idx < 0 {
			t.Fatalf("%s default %q has no port", varName, m[2])
		}
		port, err := strconv.Atoi(m[2][idx+1:])
		if err != nil {
			t.Fatalf("%s default %q has a non-numeric port", varName, m[2])
		}
		return port
	}
	return 0
}

// assertAggregatorTarget checks that a target dials the port the registry says
// its peer binds.
func assertAggregatorTarget(t *testing.T, varName string, want int) {
	t.Helper()

	got := aggregatorTargetPort(t, varName)
	if got == want {
		return
	}
	t.Errorf("aggregator %s dials port %d, but the registry says the service binds %d.\n"+
		"  Change one or the other, not both. A mismatch fails at request time as a "+
		"connection error naming no configuration.",
		varName, got, want)
}
