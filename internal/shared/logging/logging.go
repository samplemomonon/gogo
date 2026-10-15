// Package logging builds the process-wide slog logger from configuration.
// Production logs are JSON (one line per record, scrapable by Kubernetes
// log aggregators); development logs are human-readable text. Every record
// carries the service identity so log lines from many pods/routes stay
// attributable in a cluster.
package logging

import (
	"log/slog"
	"os"

	"gogo/internal/shared/config"
)

// New returns the structured logger for the process. Handler selection is
// keyed off cfg.LogFormat: "json" for machine consumption, "text" for local
// development. The service name, version and environment ride along on
// every record.
func New(cfg *config.AppConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}

	var handler slog.Handler
	switch cfg.LogFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		"service", cfg.ServiceName,
		"version", cfg.ServiceVersion,
		"env", cfg.AppEnv,
	)
}
