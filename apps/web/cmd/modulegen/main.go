// Command modulegen scaffolds a new feature module that satisfies the
// gateway's module contract (module.Interface: manifest + routes) and
// contributes itself to the DI container (Register(*di.Builder)). It is the
// "add a new in-process unit" entry point:
//
//	go run ./cmd/modulegen billing "Subscription billing, invoices and payments"
//
// creates internal/modules/billing with a manifest, a shell-rendered landing
// page, an HTMX fragment endpoint, a container registration and a smoke test —
// all compiling without the templ CLI (the page shell is composed in Go).
//
// The generated module is flat by design: no domain package, because a module
// only earns one once it owns a rule. See the generated file's header.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"text/template"
	"unicode"
)

//go:embed templates/*.tmpl
var templates embed.FS

// namePattern keeps module names Go-package friendly: lowercase, no dashes.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// outputs maps each embedded template to the file it generates.
var outputs = map[string]string{
	"templates/module.go.tmpl":      "module.go",
	"templates/handler.go.tmpl":     "handlers/handler.go",
	"templates/module_test.go.tmpl": "module_test.go",
}

// moduleData is the render data shared by every template.
type moduleData struct {
	Name        string
	Title       string
	Description string
}

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: modulegen <name> [description] [-out <dir>]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Scaffolds a contract-compliant feature module under <out>/<name>.")
		flag.PrintDefaults()
	}

	outDir := flag.String("out", "internal/modules", "parent directory for the generated module")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	name := args[0]
	if !namePattern.MatchString(name) {
		fmt.Fprintf(os.Stderr,
			"modulegen: name %q invalid — use lowercase letters and digits, e.g. \"billing\"\n",
			name)
		os.Exit(2)
	}

	description := "Manage " + name + " records through this module."
	if len(args) > 1 {
		description = args[1]
	}

	dir := filepath.Join(*outDir, name)
	if _, err := os.Stat(dir); err == nil {
		fmt.Fprintf(os.Stderr, "modulegen: %s already exists — refusing to overwrite\n", dir)
		os.Exit(1)
	}

	data := moduleData{Name: name, Title: displayName(name), Description: description}

	// Generate in a deterministic order.
	for _, entry := range []string{
		"templates/module.go.tmpl",
		"templates/handler.go.tmpl",
		"templates/module_test.go.tmpl",
	} {
		target := filepath.Join(dir, outputs[entry])
		if err := renderToFile(entry, target, data); err != nil {
			fmt.Fprintf(os.Stderr, "modulegen: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("modulegen: scaffolded module %q at %s\n", name, dir)
	fmt.Println("Next steps:")
	fmt.Println("  1. Wire it into the composition root (internal/bootstrap/di.go), three lines:")
	fmt.Println()
	fmt.Printf("       %s.Register(b)                                  // in the unit list\n", name)
	fmt.Printf("       %sModule, err := fdi.ResolveType[*%s.Module](c)\n", name, name)
	fmt.Println("       registry.AddAll(" + name + "Module)")
	fmt.Println()
	fmt.Println("  2. Run `make check`, then `make dev` and open:")
	fmt.Printf("       /%s            (module landing page)\n", name)
	fmt.Println("       /module-registry   (federated manifest)")
	fmt.Println()
	fmt.Println("  3. Add templ views under a views/ package and reference them in handlers/handler.go.")
	fmt.Println("  4. This is a flat module. Grow it into the hexagon shape — domain/,")
	fmt.Println("     application/, ports/, infrastructure/, presentation/, compose/ — only")
	fmt.Printf("     once %s owns a rule it alone must enforce.\n", name)
}

// renderToFile executes the named template into target, creating parents.
func renderToFile(templateName, target string, data moduleData) error {
	content, err := fs.ReadFile(templates, templateName)
	if err != nil {
		return fmt.Errorf("read template %s: %w", templateName, err)
	}

	tmpl, err := template.New(templateName).Parse(string(content))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", templateName, err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}

	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("render %s: %w", target, err)
	}
	return nil
}

// displayName turns a snake-like name into a title ("billing" → "Billing").
func displayName(name string) string {
	runes := []rune(name)
	if len(runes) == 0 {
		return name
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
