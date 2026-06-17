package metrics

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServer() *Server {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestProbes_ReflectState(t *testing.T) {
	s := testServer()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)

	// Before SetReady/SetHealthy, both probes report unavailable.
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, path)
		_ = resp.Body.Close()
	}

	s.SetHealthy(true)
	s.SetReady(true)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		_ = resp.Body.Close()
	}

	// Readiness can be flipped independently (as happens on shutdown).
	s.SetReady(false)
	resp, err := http.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	_ = resp.Body.Close()
}

func TestMetricsEndpoint(t *testing.T) {
	s := testServer()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "go_goroutines", "Go runtime collectors should be exposed")
}
