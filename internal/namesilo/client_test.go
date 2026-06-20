package namesilo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedRequest struct {
	op    string
	query url.Values
}

// newTestClient starts an httptest server driven by handler and returns a Client
// pointed at it plus a pointer to the slice of captured requests. Unless the
// caller sets RateLimit, the limiter is made effectively unlimited so tests
// don't sleep.
func newTestClient(t *testing.T, opts Options, handler http.HandlerFunc) (*Client, *[]capturedRequest) {
	t.Helper()
	reqs := &[]capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := r.URL.Path
		if i := strings.LastIndex(op, "/"); i >= 0 {
			op = op[i+1:]
		}
		*reqs = append(*reqs, capturedRequest{op: op, query: r.URL.Query()})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	opts.BaseURL = srv.URL
	if opts.RateLimit == 0 {
		opts.RateLimit = 100000
	}
	return New(opts), reqs
}

func writeReply(t *testing.T, w http.ResponseWriter, reply map[string]any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"reply": reply}))
}

func TestListRecords_DecodesAndMaps(t *testing.T) {
	c, _ := newTestClient(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{
			"code":   300,
			"detail": "success",
			"resource_record": []map[string]any{
				{"record_id": "1", "type": "A", "host": "www.example.com", "value": "192.0.2.1", "ttl": "3600", "distance": "0"},
				{"record_id": "2", "type": "MX", "host": "example.com", "value": "mail.example.com", "ttl": "7200", "distance": "10"},
			},
		})
	})

	records, err := c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	require.Len(t, records, 2)

	assert.Equal(t, Record{ID: "1", Type: "A", Host: "www.example.com", Value: "192.0.2.1", TTL: 3600, Distance: 0}, records[0])
	assert.Equal(t, Record{ID: "2", Type: "MX", Host: "example.com", Value: "mail.example.com", TTL: 7200, Distance: 10}, records[1])
}

func TestListRecords_CacheHitThenExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }

	var hits int
	c, _ := newTestClient(t, Options{APIKey: "k", CacheTTL: time.Minute, clock: clock}, func(w http.ResponseWriter, _ *http.Request) {
		hits++
		writeReply(t, w, map[string]any{
			"code":            300,
			"resource_record": []map[string]any{{"record_id": "1", "type": "A", "host": "a.example.com", "value": "192.0.2.1", "ttl": "3600"}},
		})
	})

	_, err := c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	_, err = c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, 1, hits, "second list within TTL should be served from cache")

	now = now.Add(61 * time.Second) // past the 60s TTL
	_, err = c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, 2, hits, "list after TTL expiry should re-fetch")
}

func TestListRecords_CacheDisabled(t *testing.T) {
	var hits int
	c, _ := newTestClient(t, Options{APIKey: "k" /* CacheTTL: 0 => disabled */}, func(w http.ResponseWriter, _ *http.Request) {
		hits++
		writeReply(t, w, map[string]any{"code": 300, "resource_record": []map[string]any{}})
	})

	for i := 0; i < 3; i++ {
		_, err := c.ListRecords(context.Background(), "example.com")
		require.NoError(t, err)
	}
	assert.Equal(t, 3, hits, "with caching disabled every list should hit the API")
}

func TestMutations_InvalidateCache(t *testing.T) {
	var listHits int
	c, _ := newTestClient(t, Options{APIKey: "k", CacheTTL: time.Hour}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "dnsListRecords") {
			listHits++
			writeReply(t, w, map[string]any{"code": 300, "resource_record": []map[string]any{}})
			return
		}
		writeReply(t, w, map[string]any{"code": 300, "record_id": "55"})
	})

	_, err := c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	require.Equal(t, 1, listHits)

	// A mutation should drop the cached zone so the next list re-fetches.
	_, err = c.AddRecord(context.Background(), "example.com", RecordInput{Type: "A", Host: "www", Value: "192.0.2.1", TTL: 3600})
	require.NoError(t, err)

	_, err = c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, 2, listHits, "list after a mutation should bypass the invalidated cache")
}

func TestAddRecord_SendsParamsAndReturnsID(t *testing.T) {
	c, reqs := newTestClient(t, Options{APIKey: "secret-key"}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{"code": 300, "record_id": "999"})
	})

	id, err := c.AddRecord(context.Background(), "example.com", RecordInput{Type: "A", Host: "www", Value: "192.0.2.1", TTL: 3600})
	require.NoError(t, err)
	assert.Equal(t, "999", id)

	require.Len(t, *reqs, 1)
	q := (*reqs)[0].query
	assert.Equal(t, "dnsAddRecord", (*reqs)[0].op)
	assert.Equal(t, "1", q.Get("version"))
	assert.Equal(t, "json", q.Get("type"))
	assert.Equal(t, "secret-key", q.Get("key"))
	assert.Equal(t, "example.com", q.Get("domain"))
	assert.Equal(t, "A", q.Get("rrtype"))
	assert.Equal(t, "www", q.Get("rrhost"))
	assert.Equal(t, "192.0.2.1", q.Get("rrvalue"))
	assert.Equal(t, "3600", q.Get("rrttl"))
	assert.False(t, q.Has("rrdistance"), "non-MX records must not send rrdistance")
}

func TestAddRecord_MXSendsDistance(t *testing.T) {
	c, reqs := newTestClient(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{"code": 300, "record_id": "7"})
	})

	_, err := c.AddRecord(context.Background(), "example.com", RecordInput{Type: "MX", Host: "", Value: "mail.example.com", TTL: 7200, Distance: 10})
	require.NoError(t, err)
	assert.Equal(t, "10", (*reqs)[0].query.Get("rrdistance"))
}

func TestUpdateAndDeleteRecord_SendRecordID(t *testing.T) {
	c, reqs := newTestClient(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{"code": 300})
	})

	require.NoError(t, c.UpdateRecord(context.Background(), "example.com", "42", RecordInput{Type: "A", Host: "www", Value: "192.0.2.9", TTL: 300}))
	require.NoError(t, c.DeleteRecord(context.Background(), "example.com", "42"))

	require.Len(t, *reqs, 2)
	assert.Equal(t, "dnsUpdateRecord", (*reqs)[0].op)
	assert.Equal(t, "42", (*reqs)[0].query.Get("rrid"))
	assert.Equal(t, "192.0.2.9", (*reqs)[0].query.Get("rrvalue"))

	assert.Equal(t, "dnsDeleteRecord", (*reqs)[1].op)
	assert.Equal(t, "42", (*reqs)[1].query.Get("rrid"))
}

func TestAPIError_OnNonSuccessCode(t *testing.T) {
	// 110 is Namesilo's "invalid API key" code (300 is success). Any non-300
	// reply.code surfaces as an *APIError.
	c, _ := newTestClient(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{"code": 110, "detail": "invalid API key"})
	})

	_, err := c.ListRecords(context.Background(), "example.com")
	require.Error(t, err)

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 110, apiErr.Code)
	assert.Equal(t, "invalid API key", apiErr.Detail)
	assert.Equal(t, "dnsListRecords", apiErr.Op)
}

func TestHTTPStatusError(t *testing.T) {
	c, _ := newTestClient(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := c.ListRecords(context.Background(), "example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP status 500")
}

func TestRateLimiter_ThrottlesAndRespectsContext(t *testing.T) {
	c, _ := newTestClient(t, Options{APIKey: "k", RateLimit: 1, Burst: 1}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{"code": 300, "resource_record": []map[string]any{}})
	})

	// First call consumes the only token immediately.
	_, err := c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)

	// The second call must wait ~1s for a refill; a short deadline forces the
	// limiter to fail rather than block, which is what surfaces as a transient
	// error to ExternalDNS.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.ListRecords(ctx, "example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limiter")
}

func TestRequestError_RedactsAPIKey(t *testing.T) {
	// Point the client at a server we immediately close, forcing a transport
	// (url.Error) failure whose message embeds the request URL — including the
	// key query parameter.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	c := New(Options{APIKey: "SUPERSECRET", BaseURL: base, RateLimit: 100000})
	_, err := c.ListRecords(context.Background(), "example.com")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SUPERSECRET", "API key must never appear in errors")
	assert.Contains(t, err.Error(), "REDACTED")
}

type fakeRecorder struct {
	apiCalls, rlWaits, hits, misses int
}

func (f *fakeRecorder) ObserveAPICall(string, time.Duration, error) { f.apiCalls++ }
func (f *fakeRecorder) ObserveRateLimitWait(time.Duration)          { f.rlWaits++ }
func (f *fakeRecorder) CacheHit()                                   { f.hits++ }
func (f *fakeRecorder) CacheMiss()                                  { f.misses++ }

func TestMetricsRecorder_RecordsCallsHitsAndWaits(t *testing.T) {
	rec := &fakeRecorder{}
	now := time.Unix(1_000_000, 0)
	c, _ := newTestClient(t, Options{APIKey: "k", CacheTTL: time.Minute, clock: func() time.Time { return now }, Metrics: rec}, func(w http.ResponseWriter, _ *http.Request) {
		writeReply(t, w, map[string]any{"code": 300, "resource_record": []map[string]any{}})
	})

	// First list misses the cache and reaches the API; the second is served
	// from cache with no API call.
	_, err := c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)
	_, err = c.ListRecords(context.Background(), "example.com")
	require.NoError(t, err)

	assert.Equal(t, 1, rec.misses)
	assert.Equal(t, 1, rec.hits)
	assert.Equal(t, 1, rec.apiCalls, "only the cache-missing list reaches the API")
	assert.Equal(t, 1, rec.rlWaits)
}

func TestScrubKey(t *testing.T) {
	out := scrubKey("https://www.namesilo.com/api/dnsListRecords?version=1&type=json&key=abc123&domain=example.com")
	assert.NotContains(t, out, "abc123")
	assert.Contains(t, out, "key=REDACTED")
	assert.Contains(t, out, "domain=example.com")
}

func TestFlexInt(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: `300`, want: 300},
		{in: `"300"`, want: 300},
		{in: `""`, want: 0},
		{in: `null`, want: 0},
		{in: `"abc"`, wantErr: true},
	}
	for _, tc := range cases {
		var n flexInt
		err := n.UnmarshalJSON([]byte(tc.in))
		if tc.wantErr {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, int(n), tc.in)
	}
}
