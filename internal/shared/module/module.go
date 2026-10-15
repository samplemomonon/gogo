// Package module defines the contract every feature module of gogo must
// implement to join the gateway — the Go + HTMX take on module federation.
//
// Instead of bundling JavaScript remotes, each module federates *HTML*:
// it declares a public manifest (name, version, entry path, routes) and
// registers its own routes. The shell/gateway never reaches into a module's
// internals; it only knows the manifest and the routes the module mounts.
package module

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"gogo/internal/shared/web/navigation"
)

// Descriptor is the public manifest of a module — the only thing the shell
// and other modules may rely on. Entry is the module's "remote entry": the
// HTML fragment contract everything else loads from (the equivalent of
// remoteEntry.js, but served as hypermedia instead of JavaScript). Nav is
// the module's contribution to the shell top bar; the shared navigation
// model lives in shared/web/navigation so this registry never owns the
// pages' navigation.
type Descriptor struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Entry       string            `json:"entry"`
	Description string            `json:"description,omitempty"`
	Routes      map[string]string `json:"routes,omitempty"`
	Nav         []navigation.Item `json:"nav,omitempty"`
}

// Interface is what a feature module must implement to be registered with
// the gateway: a manifest plus its own route registration. There is no
// shared application service — the shell only orchestrates manifests.
type Interface interface {
	Manifest() Descriptor
	RegisterRoutes(r chi.Router)
}

// Registry collects the gateway's modules and serves their manifests as
// the federated module registry, matching the "Shell → GET /module-registry"
// topology. Registration order is preserved so the manifest is stable.
type Registry struct {
	mu      sync.RWMutex
	service string
	version string
	order   []string
	modules map[string]Interface
}

// NewRegistry returns an empty registry for the given service identity.
func NewRegistry(service, version string) *Registry {
	return &Registry{
		service: service,
		version: version,
		modules: make(map[string]Interface),
	}
}

// New builds a registry pre-filled with the given modules.
func New(service, version string, modules ...Interface) *Registry {
	registry := NewRegistry(service, version)
	for _, module := range modules {
		registry.Add(module)
	}
	return registry
}

// Add registers a module. Panics on a duplicate name: duplicate manifests
// are a composition-root mistake and should fail loudly at startup.
func (r *Registry) Add(mod Interface) {
	descriptor := mod.Manifest()
	if descriptor.Name == "" {
		panic("module: cannot register a module without a name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modules[descriptor.Name]; exists {
		panic(fmt.Sprintf("module: duplicate registration of %q", descriptor.Name))
	}

	r.modules[descriptor.Name] = mod
	r.order = append(r.order, descriptor.Name)
}

// AddAll registers several modules at once, in order. See Add for the
// duplicate-name behaviour.
func (r *Registry) AddAll(modules ...Interface) {
	for _, mod := range modules {
		r.Add(mod)
	}
}

// Get returns the manifest of the named module.
func (r *Registry) Get(name string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	mod, ok := r.modules[name]
	if !ok {
		return Descriptor{}, false
	}
	return mod.Manifest(), true
}

// Manifests returns every registered manifest in registration order.
func (r *Registry) Manifests() []Descriptor {
	manifests := make([]Descriptor, 0, len(r.order))
	for _, mod := range r.snapshot() {
		manifests = append(manifests, mod.Manifest())
	}
	return manifests
}

// Modules returns the registered module interfaces in registration order.
func (r *Registry) Modules() []Interface {
	return r.snapshot()
}

// snapshot returns the registered modules in registration order under a
// single RLock acquisition, so the registry's polled views (manifests,
// lifecycle, health) never observe a torn registration set.
func (r *Registry) snapshot() []Interface {
	r.mu.RLock()
	defer r.mu.RUnlock()

	modules := make([]Interface, 0, len(r.order))
	for _, name := range r.order {
		modules = append(modules, r.modules[name])
	}
	return modules
}

// Nav returns the aggregated navigation of every registered module, in
// registration order. The gateway composes it with its pages' own items and
// injects the full menu into every request so the shell renders a module's
// nav items the moment it is registered — no shell edits beyond
// registration.
func (r *Registry) Nav() []navigation.Item {
	var nav []navigation.Item
	for _, mod := range r.snapshot() {
		nav = append(nav, mod.Manifest().Nav...)
	}
	return nav
}

// Service returns the registry's service identity.
func (r *Registry) Service() string { return r.service }

// Version returns the registry's service version.
func (r *Registry) Version() string { return r.version }

type registryResponse struct {
	Service string       `json:"service"`
	Version string       `json:"service_version"`
	Modules []Descriptor `json:"modules"`
}

// Handler returns the registry's JSON endpoint — the federation contract
// for API clients. The gateway augments it with a human-readable HTML view
// for browsers (both share the same shell).
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.renderJSON(w)
	})
}

func (r *Registry) renderJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(registryResponse{
		Service: r.service,
		Version: r.version,
		Modules: r.Manifests(),
	})
}
