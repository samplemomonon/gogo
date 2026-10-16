package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamePattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid bool
	}{
		{"billing", true},
		{"a", true},
		{"billing2", true},
		{"", false},
		{"Billing", false},
		{"bil-ling", false},
		{"bil_ling", false},
		{"2billing", false},
		{"bil ling", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := namePattern.MatchString(tt.name); got != tt.valid {
				t.Errorf("namePattern.MatchString(%q) = %v, want %v", tt.name, got, tt.valid)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	t.Parallel()

	if got := displayName("billing"); got != "Billing" {
		t.Errorf("displayName(billing) = %q, want Billing", got)
	}
	if got := displayName(""); got != "" {
		t.Errorf("displayName(\"\") = %q, want the empty name back", got)
	}
}

// TestRenderToFileExercisesEveryEmbeddedTemplate is the test that earns its
// keep: it parses and executes the real templates against real data, so a
// broken template fails here instead of in a scaffolded module.
func TestRenderToFileExercisesEveryEmbeddedTemplate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := moduleData{Name: "billing", Title: "Billing", Description: "Tagihan"}

	for templateName, target := range outputs {
		templateName, target := templateName, target
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(dir, target)
			if err := renderToFile(templateName, path, data); err != nil {
				t.Fatalf("renderToFile(%s): %v", templateName, err)
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			content := string(body)
			if !strings.Contains(content, "billing") {
				t.Errorf("rendered %s does not mention the module name", target)
			}
			if strings.Contains(content, "{{") {
				t.Errorf("rendered %s still contains template actions", target)
			}
		})
	}
	// The handlers/handler.go subtest passing is itself the MkdirAll check:
	// renderToFile had to create the handlers/ parent to write the file.
}
