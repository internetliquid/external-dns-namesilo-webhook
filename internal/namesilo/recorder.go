package namesilo

import "time"

// Recorder receives client telemetry. It is a consumer-side interface so the
// client stays decoupled from any particular metrics backend (the Prometheus
// implementation lives in internal/metrics). A nil Recorder is replaced with a
// no-op, so instrumentation is always safe to call.
type Recorder interface {
	// ObserveAPICall records one Namesilo API request: its operation, how long
	// the HTTP round trip plus decode took, and whether it failed (a transport
	// error or a non-success reply code both count as an error).
	ObserveAPICall(operation string, d time.Duration, err error)
	// ObserveRateLimitWait records how long a request blocked waiting for a
	// rate-limiter token before being sent.
	ObserveRateLimitWait(d time.Duration)
	// CacheHit records a dnsListRecords cache hit.
	CacheHit()
	// CacheMiss records a dnsListRecords cache miss (a list that hit the API).
	CacheMiss()
}

// nopRecorder is the default when no Recorder is supplied.
type nopRecorder struct{}

func (nopRecorder) ObserveAPICall(string, time.Duration, error) {}
func (nopRecorder) ObserveRateLimitWait(time.Duration)          {}
func (nopRecorder) CacheHit()                                   {}
func (nopRecorder) CacheMiss()                                  {}
