// Package namesilo is a small, purpose-built JSON client for Namesilo's
// classic DNS API (https://www.namesilo.com/api-reference).
//
// It deliberately does not wrap the legacy nrdcg/namesilo client (which is
// modeled around Namesilo's 2019 XML structs); instead it talks to the JSON
// variant of the API (type=json) end to end. The client adds the two things
// the ExternalDNS webhook needs to be a good Namesilo citizen: an internal
// rate limiter (Namesilo recommends ~1 request/second per IP) and a per-zone
// cache of dnsListRecords results so reconciles don't re-list on every call.
package namesilo

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// DefaultBaseURL is the base of Namesilo's classic API.
	DefaultBaseURL = "https://www.namesilo.com/api"

	// apiVersion is the value of the required version query parameter.
	apiVersion = "1"

	// successCode is the reply.code Namesilo returns when an operation
	// succeeds. Every other code is treated as an error.
	successCode = 300
)

// Record is a typed DNS resource record as returned by dnsListRecords.
type Record struct {
	// ID is the Namesilo record_id, required to update or delete a record. The
	// provider deliberately does not persist it in endpoint.ProviderSpecific;
	// it re-resolves the id at apply time by listing the zone (Namesilo rotates
	// the record_id on every successful update, so a cached id would go stale).
	ID string
	// Type is the record type (A, AAAA, CNAME, MX, TXT, ...).
	Type string
	// Host is the hostname exactly as Namesilo returns it: the full name
	// (e.g. "www.example.com", or "example.com" for the zone apex), not a
	// relative label. Confirmed against Namesilo's dnsListRecords reference,
	// whose sample data returns FQDNs in "host". The mapping in
	// internal/provider relativizes this against the managed zone.
	Host string
	// Value is the record value (rrvalue).
	Value string
	// TTL is the record TTL in seconds.
	TTL int
	// Distance is the MX preference (rrdistance); 0 for non-MX records.
	Distance int
}

// RecordInput describes a record to create (dnsAddRecord) or update
// (dnsUpdateRecord).
type RecordInput struct {
	// Type is required when adding a record. Namesilo does not allow changing
	// the type on update, so it is ignored there except to decide whether to
	// send rrdistance.
	Type string
	// Host is the RELATIVE host label: "" for the zone apex, "www" for
	// "www.example.com". The provider relativizes the ExternalDNS FQDN against
	// the managed zone before calling. Namesilo's dnsAddRecord/dnsUpdateRecord
	// reference documents rrhost as relative ("no need to include the .DOMAIN").
	Host string
	// Value is the record value (rrvalue).
	Value string
	// TTL is the record TTL in seconds (rrttl).
	TTL int
	// Distance is the MX preference (rrdistance); only sent for MX records.
	Distance int
}

// APIError is returned when Namesilo replies with HTTP 200 but a non-success
// reply.code. It is a permanent, well-formed API rejection (bad parameters,
// auth failure, etc.) as opposed to a transport error.
type APIError struct {
	Op     string
	Code   int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("namesilo %s: api code %d: %s", e.Op, e.Code, e.Detail)
}

// apiResponse mirrors the top-level Namesilo JSON envelope. Only the reply
// object carries data we care about.
type apiResponse struct {
	Reply apiReply `json:"reply"`
}

// apiReply is the union of fields returned across the operations we call.
// Unused fields for a given operation simply stay zero-valued.
type apiReply struct {
	Code   flexInt `json:"code"`
	Detail string  `json:"detail"`

	// dnsListRecords
	ResourceRecord []apiRecord `json:"resource_record"`

	// dnsAddRecord returns the new record's id here.
	RecordID string `json:"record_id"`
}

type apiRecord struct {
	RecordID string  `json:"record_id"`
	Type     string  `json:"type"`
	Host     string  `json:"host"`
	Value    string  `json:"value"`
	TTL      flexInt `json:"ttl"`
	Distance flexInt `json:"distance"`
}

// flexInt decodes a JSON value that may be either a number or a quoted number.
// Namesilo's JSON API is derived from its legacy XML schema and renders numeric
// fields (code, ttl, distance) as strings in practice, but we tolerate both so
// a representation change can't silently break success detection.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("flexInt: %q is not an integer: %w", s, err)
	}
	*n = flexInt(v)
	return nil
}
