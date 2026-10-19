// Package main_test is the authzbootstrap binary's fail-closed contract: the
// one-time policy-administrator ceremony must refuse to continue unless a
// database URL, all three identifiers, and a reachable database are present —
// and it must never print a success that did not happen. Every case here
// points at a closed port; no live database is touched.
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

// unreachableDSN points at a closed port so the connect attempt fails
// immediately instead of hanging; connect_timeout bounds it even if the port
// answers.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1"

func TestAuthzBootstrapFailsClosed(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not in PATH; cannot build the authzbootstrap binary")
	}

	binary := filepath.Join(t.TempDir(), "authzbootstrap")
	build := exec.Command(goTool, "build", "-o", binary, "gogo/cmd/authzbootstrap")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build gogo/cmd/authzbootstrap: %v\n%s", err, out)
	}

	tests := []struct {
		name       string
		env        []string
		args       []string
		wantStderr string
	}{
		{
			name:       "missing database URL",
			env:        nil,
			args:       []string{"-tenant", "tenant-1", "-employee", "emp-1", "-change-reference", "CHG-1"},
			wantStderr: "AUTHZ_DATABASE_URL is required",
		},
		{
			name:       "missing identifiers",
			env:        []string{"AUTHZ_DATABASE_URL=" + unreachableDSN},
			args:       nil,
			wantStderr: "-tenant, -employee, and -change-reference are required",
		},
		{
			name:       "blank identifiers",
			env:        []string{"AUTHZ_DATABASE_URL=" + unreachableDSN},
			args:       []string{"-tenant", " ", "-employee", " ", "-change-reference", " "},
			wantStderr: "-tenant, -employee, and -change-reference are required",
		},
		{
			name:       "unreachable database",
			env:        []string{"AUTHZ_DATABASE_URL=" + unreachableDSN},
			args:       []string{"-tenant", "tenant-1", "-employee", "emp-1", "-change-reference", "CHG-1"},
			wantStderr: "connect to authorization database",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, binary, tt.args...)
			// A scratch working directory and the minimal environment: nothing
			// in this repository can rescue the missing input.
			cmd.Dir = t.TempDir()
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, tt.env...)

			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr

			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("authzbootstrap did not exit with a failure (%v): a refused bootstrap must stop the ceremony\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
			if code := exitErr.ExitCode(); code != 1 {
				t.Errorf("exit code = %d, want 1\nstderr: %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr missing %q; an operator cannot find the misconfiguration:\n%s", tt.wantStderr, stderr.String())
			}
			if strings.Contains(stdout.String(), "bootstrapped") {
				t.Errorf("stdout claims a bootstrap that did not happen:\n%s", stdout.String())
			}
		})
	}
}

// moduleRoot walks up from the test's working directory (the
// cmd/authzbootstrap package directory) to the directory holding go.mod,
// verifying the layout the build command's relative paths rely on rather than
// hard-coding a depth.
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
