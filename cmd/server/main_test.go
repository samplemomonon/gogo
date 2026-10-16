// Package main_test is the gateway binary's black-box smoke test: it builds the
// real cmd/server binary and proves it refuses to boot on invalid
// configuration, exiting 1 with the validator's diagnostic on stderr. It
// deliberately never starts a healthy server — no ports are bound and no
// upstreams are dialed.
package main_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServerExitsOneOnInvalidConfiguration pins the startup gate main.go
// defines: config.LoadValidated's error is logged as "invalid configuration"
// and exits 1 before any logger or gateway is built. PORT is set to a value
// config.Validate rejects through the same code path an operator's typo takes.
func TestServerExitsOneOnInvalidConfiguration(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH; cannot build the gateway binary")
	}

	binary := filepath.Join(t.TempDir(), "server")
	build := exec.Command(goTool, "build", "-o", binary, "gogo/cmd/server")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build gogo/cmd/server: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary)
	// A scratch working directory: config.Load's optional .env read resolves
	// against the process's working directory, and the repository ships a .env
	// whose values would otherwise rescue this invalid PORT.
	cmd.Dir = t.TempDir()
	// The minimal environment the process needs to reach validation. HOME and
	// PATH are supplied; everything else, including every required product
	// address, is deliberately absent.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"PORT=not-a-port",
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("server did not exit with a failure (%v); it must refuse an invalid PORT\nstderr: %s", err, stderr.String())
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Errorf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
	}

	// The validator's joined error is one slog attribute, so the text handler
	// escapes the quotes around the offending value; assert on the fragments
	// that survive that quoting.
	diagnostic := stderr.String()
	for _, want := range []string{"invalid configuration", "PORT", "not-a-port", "must be a valid TCP port"} {
		if !strings.Contains(diagnostic, want) {
			t.Errorf("stderr missing %q; an operator cannot find the misconfiguration:\n%s", want, diagnostic)
		}
	}
}

// moduleRoot walks up from the test's working directory (the cmd/server
// package directory) to the directory holding go.mod, verifying the layout the
// build command's relative paths rely on rather than hard-coding a depth.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test working directory; cannot locate the module root")
		}
		dir = parent
	}
}
