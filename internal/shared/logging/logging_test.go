package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"gogo/internal/shared/config"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// the logger wrote. Not parallel-safe: it swaps the process's stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe failed: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe failed: %v", err)
	}
	return string(data)
}

func TestNewSelectsJSONHandlerInProductionFormat(t *testing.T) {
	cfg := &config.AppConfig{
		AppEnv:         "production",
		LogFormat:      "json",
		LogLevel:       slog.LevelInfo,
		ServiceName:    "gogo",
		ServiceVersion: "1.2.3",
	}

	out := captureStdout(t, func() {
		New(cfg).Info("hello")
	})

	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("json handler should emit a JSON object, got %q", out)
	}
	for _, want := range []string{`"service":"gogo"`, `"version":"1.2.3"`, `"env":"production"`, `"msg":"hello"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json log missing %q: %s", want, out)
		}
	}
}

func TestNewSelectsTextHandlerInDevelopmentFormat(t *testing.T) {
	cfg := &config.AppConfig{
		AppEnv:         "development",
		LogFormat:      "text",
		LogLevel:       slog.LevelInfo,
		ServiceName:    "gogo",
		ServiceVersion: "dev",
	}

	out := captureStdout(t, func() {
		New(cfg).Info("hello")
	})

	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("text handler should emit key=value log lines, got %q", out)
	}
	for _, want := range []string{"service=gogo", "version=dev", "env=development", "msg=hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("text log missing %q: %s", want, out)
		}
	}
}

func TestNewDefaultsToTextWhenFormatUnset(t *testing.T) {
	cfg := &config.AppConfig{
		AppEnv:      "production",
		LogFormat:   "",
		LogLevel:    slog.LevelInfo,
		ServiceName: "gogo",
	}

	out := captureStdout(t, func() {
		New(cfg).Info("hello")
	})

	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("unset format should default to the text handler, got %q", out)
	}
}
