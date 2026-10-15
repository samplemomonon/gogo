// Package handler serves the HTTP surface: the GraphQL endpoint, the health
// probes, and the hardening middleware in front of both.
//
// The middleware is the substance here. A GraphQL endpoint is a different threat
// shape from a REST one, because a single URL accepts arbitrary queries and the
// cost of an expensive query is chosen by whoever writes it. Every limit in this
// file exists because the equivalent REST endpoint does not need it.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/config"
	"aggregator-service/internal/version"
)

// Server holds the dependencies the HTTP surface needs.
type Server struct {
	// GraphQL is the gqlgen handler. It is an http.Handler because gqlgen does its
	// own method and content-type negotiation, and wrapping it here would have to
	// replicate that.
	GraphQL http.Handler
	// Playground is the interactive schema browser, or nil when disabled.
	Playground http.Handler
	// ReadyCheck reports whether the aggregator can serve traffic.
	ReadyCheck func(ctx context.Context) error
	// Config is the resolved runtime configuration.
	Config *config.Config
	// Log is the structured logger.
	Log *slog.Logger
}

// Handler builds the router.
//
// Routes are deliberately few: one GraphQL endpoint, the probes, and the version
// document. There is no REST surface here at all, because a composition layer
// that also exposed REST would have two answers to every question and clients
// would use whichever one they found first.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// POST is the GraphQL endpoint. GET is registered separately below rather than
	// by pattern, because a GET query has to be opted into explicitly.
	//
	// The nil fallback exists because a Server built without a GraphQL handler would
	// otherwise panic inside ServeMux registration at startup, which turns a wiring
	// mistake into a crash rather than a 503. The probes still work in that state,
	// which is what lets an operator diagnose it.
	graphqlHandler := s.GraphQL
	if graphqlHandler == nil {
		graphqlHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable,
				errorResponse(http.StatusServiceUnavailable, "the query endpoint is not configured", "UNAVAILABLE"))
		})
	}
	mux.Handle("/query", graphqlHandler)

	if s.Playground != nil {
		mux.Handle("/playground", s.Playground)
	}

	mux.Handle("/health", s.liveness())
	mux.Handle("/healthz", s.liveness())
	mux.Handle("/readyz", s.readiness())
	mux.Handle("/version", s.versionHandler())

	// Anything else is a 404 in the JSON shape GraphQL clients already parse,
	// rather than net/http's plain-text one. A client that only knows how to read
	// the GraphQL error envelope should not have to special-case a typo'd path.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"errors": []map[string]any{{
				"message": "no such endpoint",
				"path":    r.URL.Path,
			}},
		})
	})

	// The order is deliberate and reads outside-in: recover, then request budget,
	// then logging, then the security headers and origin check. Recovery is first
	// so it also catches a panic in the middleware behind it; the budget is next
	// so a rejected request is still logged.
	//
	// The request cache is not in this chain: it is graph's, not the transport's, and
	// this package must not import graph. main wraps the finished handler with it.
	return Recover(s.Log)(
		RequestBudget(s.Config.RequestTimeout, s.Log)(
			Logging(s.Log)(
				SecurityHeaders(s.Config.OriginAllowed)(mux),
			),
		),
	)
}

// liveness answers "is this process running".
//
// It touches nothing downstream. A liveness probe that called a dependency would
// have the aggregator restart itself whenever a dependency blipped, which converts
// a partial outage into a full one and removes the partial-failure behaviour the
// whole design depends on.
func (s *Server) liveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
}

// readiness answers "should this instance receive traffic".
//
// This one does check the dependencies, and the asymmetry with liveness is
// deliberate. Kubernetes uses the two differently: a failing liveness probe kills
// the pod, a failing readiness probe only removes it from the load balancer. So a
// dependency outage should make this instance stop taking new requests while
// leaving it alive to serve what it can.
func (s *Server) readiness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ReadyCheck == nil {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.Config.DependencyTimeout)
		defer cancel()

		if err := s.ReadyCheck(ctx); err != nil {
			s.Log.Warn("readiness check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unavailable",
				"error":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
}

// versionHandler reports build metadata.
func (s *Server) versionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"version": version.Short(),
			"build":   version.String(),
		})
	})
}

// writeJSON sends a JSON response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already on the wire, so there is nothing to do but
		// record it; the connection will be closed by the server.
		slog.Error("write response", "error", err)
	}
}

// errorResponse builds a GraphQL-shaped error envelope, so that a transport-level
// failure looks like the rest of the API to a client that only parses one shape.
func errorResponse(status int, message, code string) map[string]any {
	return map[string]any{
		"errors": []map[string]any{{
			"message":    message,
			"extensions": map[string]any{"code": code},
		}},
		"data": nil,
		// Echoed so a client can distinguish "the server said no" from "no response
		// arrived", which is the difference between a bug report and a retry.
		"status": status,
	}
}

// classifyStatus maps an aggregator error to an HTTP status.
//
// GraphQL normally answers 200 even for errors, because the error is in the body.
// This function is only used for failures that happen before or around execution,
// where there is no body to carry them: a rejected query, an unparseable request.
// Returning the real status there is what lets a proxy, a client library, or a
// rate limiter see what actually happened.
func classifyStatus(err error) (int, string) {
	switch {
	case errors.Is(err, apperror.ErrRejected):
		return http.StatusUnprocessableEntity, "REJECTED"
	case errors.Is(err, apperror.ErrInvalidInput):
		return http.StatusBadRequest, "BAD_USER_INPUT"
	case errors.Is(err, apperror.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, apperror.ErrConflict):
		return http.StatusConflict, "CONFLICT"
	case errors.Is(err, apperror.ErrUnavailable):
		return http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}

// logRequestError records a failure that happened outside resolver execution.
func logRequestError(log *slog.Logger, err error, r *http.Request, started time.Time) {
	status, code := classifyStatus(err)
	log.Warn("request rejected",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"code", code,
		"error", err.Error(),
		"duration_ms", time.Since(started).Milliseconds(),
	)
}
