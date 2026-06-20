package namesilo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// maxResponseBytes caps how much of a Namesilo response body we read. Responses
// are tiny; this guards against a misbehaving upstream streaming unbounded data.
const maxResponseBytes = 1 << 20 // 1 MiB

// Options configures a Client. Zero values fall back to safe defaults.
type Options struct {
	// APIKey is the Namesilo API key. Treated as a secret: never logged.
	APIKey string
	// BaseURL overrides the API base (used by tests). Defaults to DefaultBaseURL.
	BaseURL string
	// HTTPClient lets callers inject a custom transport. Defaults to a plain
	// &http.Client{}; per-request deadlines are enforced via context.
	HTTPClient *http.Client
	// RateLimit is the sustained request rate in requests/second. Defaults to 1.
	RateLimit float64
	// Burst is the rate limiter's bucket size. Defaults to 1.
	Burst int
	// CacheTTL is how long dnsListRecords results are cached per zone. A
	// non-positive value disables caching.
	CacheTTL time.Duration
	// Timeout bounds each individual API call. Must be shorter than the
	// ExternalDNS webhook client deadline. Defaults to 30s.
	Timeout time.Duration
	// Logger receives debug logs. Defaults to slog.Default().
	Logger *slog.Logger

	// clock is a testing seam for cache expiry. Defaults to time.Now.
	clock func() time.Time
}

// Client is a typed JSON client for the Namesilo DNS API.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	limiter    *rate.Limiter
	cache      *zoneCache
	timeout    time.Duration
	logger     *slog.Logger
}

// New builds a Client from Options, applying defaults for any zero values.
func New(opts Options) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	if opts.RateLimit <= 0 {
		opts.RateLimit = 1
	}
	if opts.Burst <= 0 {
		opts.Burst = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.clock == nil {
		opts.clock = time.Now
	}

	return &Client{
		apiKey:     opts.APIKey,
		baseURL:    opts.BaseURL,
		httpClient: opts.HTTPClient,
		limiter:    rate.NewLimiter(rate.Limit(opts.RateLimit), opts.Burst),
		cache:      newZoneCache(opts.CacheTTL, opts.clock),
		timeout:    opts.Timeout,
		logger:     opts.Logger,
	}
}

// ListRecords returns all DNS records for a zone, serving a cached copy when a
// fresh one is available.
func (c *Client) ListRecords(ctx context.Context, domain string) ([]Record, error) {
	if cached, ok := c.cache.get(domain); ok {
		c.logger.Debug("namesilo record cache hit", "domain", domain, "records", len(cached))
		return cached, nil
	}

	params := url.Values{}
	params.Set("domain", domain)
	reply, err := c.do(ctx, "dnsListRecords", params)
	if err != nil {
		return nil, err
	}

	records := make([]Record, 0, len(reply.ResourceRecord))
	for _, r := range reply.ResourceRecord {
		records = append(records, Record{
			ID:       r.RecordID,
			Type:     r.Type,
			Host:     r.Host,
			Value:    r.Value,
			TTL:      int(r.TTL),
			Distance: int(r.Distance),
		})
	}

	c.cache.set(domain, records)
	return records, nil
}

// AddRecord creates a record and returns the new record's id. The zone cache is
// invalidated so the next list reflects the change.
func (c *Client) AddRecord(ctx context.Context, domain string, in RecordInput) (string, error) {
	params := url.Values{}
	params.Set("domain", domain)
	params.Set("rrtype", in.Type)
	params.Set("rrhost", in.Host)
	params.Set("rrvalue", in.Value)
	params.Set("rrttl", strconv.Itoa(in.TTL))
	if in.Type == "MX" {
		params.Set("rrdistance", strconv.Itoa(in.Distance))
	}

	reply, err := c.do(ctx, "dnsAddRecord", params)
	if err != nil {
		return "", err
	}
	c.cache.invalidate(domain)
	return reply.RecordID, nil
}

// UpdateRecord updates an existing record by id. Namesilo keys updates on the
// record id (rrid) and does not allow changing the record type.
func (c *Client) UpdateRecord(ctx context.Context, domain, recordID string, in RecordInput) error {
	params := url.Values{}
	params.Set("domain", domain)
	params.Set("rrid", recordID)
	params.Set("rrhost", in.Host)
	params.Set("rrvalue", in.Value)
	params.Set("rrttl", strconv.Itoa(in.TTL))
	if in.Type == "MX" {
		params.Set("rrdistance", strconv.Itoa(in.Distance))
	}

	if _, err := c.do(ctx, "dnsUpdateRecord", params); err != nil {
		return err
	}
	c.cache.invalidate(domain)
	return nil
}

// DeleteRecord deletes a record by id.
func (c *Client) DeleteRecord(ctx context.Context, domain, recordID string) error {
	params := url.Values{}
	params.Set("domain", domain)
	params.Set("rrid", recordID)

	if _, err := c.do(ctx, "dnsDeleteRecord", params); err != nil {
		return err
	}
	c.cache.invalidate(domain)
	return nil
}

// do performs a single Namesilo API call: it waits for a rate-limiter token,
// appends the required version/type/key parameters, issues the GET with a
// bounded timeout, and decodes the envelope, returning an *APIError when the
// reply code is not the success code.
func (c *Client) do(ctx context.Context, operation string, params url.Values) (*apiReply, error) {
	// Proactively throttle to respect Namesilo's ~1 req/s per-IP guidance.
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("namesilo %s: rate limiter: %w", operation, err)
	}

	if params == nil {
		params = url.Values{}
	}
	params.Set("version", apiVersion)
	params.Set("type", "json")
	params.Set("key", c.apiKey)

	reqURL := c.baseURL + "/" + operation + "?" + params.Encode()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("namesilo %s: build request: %w", operation, err)
	}
	req.Header.Set("Accept", "application/json")

	// Log the operation and domain only — never the URL, which carries the key.
	c.logger.Debug("namesilo api call", "operation", operation, "domain", params.Get("domain"))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("namesilo %s: request failed: %w", operation, redactKeyInError(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("namesilo %s: read body: %w", operation, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("namesilo %s: unexpected HTTP status %d", operation, resp.StatusCode)
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("namesilo %s: decode response: %w", operation, err)
	}
	if int(parsed.Reply.Code) != successCode {
		return nil, &APIError{Op: operation, Code: int(parsed.Reply.Code), Detail: parsed.Reply.Detail}
	}

	return &parsed.Reply, nil
}

// redactKeyInError scrubs the API key from errors that embed the request URL
// (notably *url.Error), so the secret can never reach logs via a transport
// failure such as "Get \"https://...&key=SECRET\": dial tcp: ...".
func redactKeyInError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = scrubKey(ue.URL)
	}
	return err
}

func scrubKey(raw string) string {
	u, perr := url.Parse(raw)
	if perr != nil {
		return raw
	}
	q := u.Query()
	if q.Has("key") {
		q.Set("key", "REDACTED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}
