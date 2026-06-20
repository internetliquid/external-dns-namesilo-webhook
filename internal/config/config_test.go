package config

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_RequiresAPIKey(t *testing.T) {
	t.Setenv("NAMESILO_API_KEY", "")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NAMESILO_API_KEY")
}

func TestLoad_RequiresDomainFilter(t *testing.T) {
	t.Setenv("NAMESILO_API_KEY", "key")
	// DOMAIN_FILTER unset.
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DOMAIN_FILTER")
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("NAMESILO_API_KEY", "key")
	t.Setenv("DOMAIN_FILTER", "example.com")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "key", cfg.APIKey)
	assert.Equal(t, []string{"example.com"}, cfg.DomainFilter)
	assert.False(t, cfg.DryRun)
	assert.Equal(t, "localhost", cfg.WebhookHost)
	assert.Equal(t, 8888, cfg.WebhookPort)
	assert.Equal(t, "0.0.0.0", cfg.MetricsHost)
	assert.Equal(t, 8080, cfg.MetricsPort)
	assert.Equal(t, float64(1), cfg.RateLimit)
	assert.Equal(t, time.Minute, cfg.CacheTTL)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
	assert.Equal(t, 3600, cfg.DefaultTTL)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, "json", cfg.LogFormat)
}

func TestLoad_CustomValues(t *testing.T) {
	t.Setenv("NAMESILO_API_KEY", "key")
	t.Setenv("DOMAIN_FILTER", "example.com, example.org ,")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("WEBHOOK_PORT", "9000")
	t.Setenv("METRICS_PORT", "9090")
	t.Setenv("NAMESILO_RATE_LIMIT", "0.5")
	t.Setenv("RECORD_CACHE_TTL", "120s")
	t.Setenv("NAMESILO_TIMEOUT", "10s")
	t.Setenv("DEFAULT_TTL", "7200")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "text")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, []string{"example.com", "example.org"}, cfg.DomainFilter)
	assert.True(t, cfg.DryRun)
	assert.Equal(t, 9000, cfg.WebhookPort)
	assert.Equal(t, 9090, cfg.MetricsPort)
	assert.Equal(t, 0.5, cfg.RateLimit)
	assert.Equal(t, 120*time.Second, cfg.CacheTTL)
	assert.Equal(t, 10*time.Second, cfg.Timeout)
	assert.Equal(t, 7200, cfg.DefaultTTL)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, "text", cfg.LogFormat)
}

func TestLoad_InvalidValues(t *testing.T) {
	cases := map[string]string{
		"WEBHOOK_PORT":        "notaport",
		"DRY_RUN":             "maybe",
		"NAMESILO_RATE_LIMIT": "-1",
		"RECORD_CACHE_TTL":    "5flarbs",
		"LOG_LEVEL":           "verbose",
		"LOG_FORMAT":          "xml",
	}
	for key, bad := range cases {
		t.Run(key, func(t *testing.T) {
			t.Setenv("NAMESILO_API_KEY", "key")
			t.Setenv("DOMAIN_FILTER", "example.com")
			t.Setenv(key, bad)
			_, err := Load()
			assert.Error(t, err, "%s=%q should fail validation", key, bad)
		})
	}
}
