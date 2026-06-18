// Package metrics provides the webhook's health and Prometheus metrics server.
// It is intentionally separate from the ExternalDNS provider API server (which
// is the external-dns StartHTTPApi helper, bound to localhost): this server
// binds 0.0.0.0 so liveness/readiness probes and Prometheus can reach it.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server exposes /healthz, /readyz, and /metrics. Liveness and readiness are
// toggled at runtime so shutdown can flip readiness to NotReady before exiting.
type Server struct {
	logger   *slog.Logger
	registry *prometheus.Registry
	httpSrv  *http.Server

	healthy atomic.Bool
	ready   atomic.Bool
}

// New creates a metrics Server with the Go runtime and process collectors
// registered. It does not start listening; call Serve.
func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return &Server{logger: logger, registry: reg}
}

// Registry exposes the Prometheus registry so callers can register additional
// collectors before Serve is called.
func (s *Server) Registry() *prometheus.Registry { return s.registry }

// SetHealthy controls the /healthz (liveness) response.
func (s *Server) SetHealthy(v bool) { s.healthy.Store(v) }

// SetReady controls the /readyz (readiness) response.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", probeHandler(&s.healthy))
	mux.HandleFunc("/readyz", probeHandler(&s.ready))
	mux.Handle("/metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{Registry: s.registry}))
	return mux
}

func probeHandler(flag *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if flag.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}
}

// Serve listens on addr and blocks until the server is shut down. A clean
// shutdown (via Shutdown) returns nil.
func (s *Server) Serve(addr string) error {
	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.logger.Info("metrics/health server listening", "addr", addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully drains the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}
