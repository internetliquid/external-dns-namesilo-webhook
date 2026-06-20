package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/internetliquid/external-dns-namesilo-webhook/internal/namesilo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
)

type call struct {
	op   string
	zone string
	id   string
	in   namesilo.RecordInput
}

type mockClient struct {
	records map[string][]namesilo.Record
	listErr error
	nextID  int
	calls   []call
}

func (m *mockClient) ListRecords(_ context.Context, zone string) ([]namesilo.Record, error) {
	m.calls = append(m.calls, call{op: "list", zone: zone})
	if m.listErr != nil {
		return nil, m.listErr
	}
	return append([]namesilo.Record(nil), m.records[zone]...), nil
}

func (m *mockClient) AddRecord(_ context.Context, zone string, in namesilo.RecordInput) (string, error) {
	m.nextID++
	id := fmt.Sprintf("id-%d", m.nextID)
	m.calls = append(m.calls, call{op: "add", zone: zone, id: id, in: in})
	return id, nil
}

func (m *mockClient) UpdateRecord(_ context.Context, zone, id string, in namesilo.RecordInput) error {
	m.calls = append(m.calls, call{op: "update", zone: zone, id: id, in: in})
	return nil
}

func (m *mockClient) DeleteRecord(_ context.Context, zone, id string) error {
	m.calls = append(m.calls, call{op: "delete", zone: zone, id: id})
	return nil
}

func (m *mockClient) opsOf(op string) []call {
	var out []call
	for _, c := range m.calls {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func testProvider(client apiClient, zones []string, dryRun bool) *NamesiloProvider {
	return New(Options{
		Client:       client,
		DomainFilter: zones,
		DefaultTTL:   3600,
		DryRun:       dryRun,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestRecordsToEndpoints_GroupsAndMaps(t *testing.T) {
	records := []namesilo.Record{
		{ID: "1", Type: "A", Host: "www.example.com", Value: "192.0.2.1", TTL: 3600},
		{ID: "2", Type: "A", Host: "www.example.com", Value: "192.0.2.2", TTL: 3600},
		{ID: "3", Type: "MX", Host: "example.com", Value: "mail.example.com", TTL: 7200, Distance: 10},
		{ID: "4", Type: "TXT", Host: "example.com", Value: `"heritage=external-dns"`, TTL: 300},
		{ID: "5", Type: "NS", Host: "example.com", Value: "ns1.namesilo.com", TTL: 3600},
	}

	eps := recordsToEndpoints(records)
	require.Len(t, eps, 3, "two A records collapse to one endpoint; NS is unsupported and dropped")

	assert.Equal(t, "www.example.com", eps[0].DNSName)
	assert.Equal(t, "A", eps[0].RecordType)
	assert.ElementsMatch(t, []string{"192.0.2.1", "192.0.2.2"}, []string(eps[0].Targets))

	assert.Equal(t, "MX", eps[1].RecordType)
	assert.Equal(t, []string{"10 mail.example.com"}, []string(eps[1].Targets))

	assert.Equal(t, "TXT", eps[2].RecordType)
	assert.Equal(t, []string{"heritage=external-dns"}, []string(eps[2].Targets), "TXT quotes stripped")
}

func TestRecords_AcrossZonesAndListError(t *testing.T) {
	m := &mockClient{records: map[string][]namesilo.Record{
		"example.com": {{ID: "1", Type: "A", Host: "a.example.com", Value: "192.0.2.1", TTL: 3600}},
	}}
	p := testProvider(m, []string{"example.com"}, false)

	eps, err := p.Records(context.Background())
	require.NoError(t, err)
	require.Len(t, eps, 1)
	assert.Equal(t, "a.example.com", eps[0].DNSName)

	// A list failure must fail the whole call (no partial view to ExternalDNS).
	m.listErr = errors.New("boom")
	_, err = p.Records(context.Background())
	require.Error(t, err)
}

func TestAdjustEndpoints_SetsDefaultTTL(t *testing.T) {
	p := testProvider(&mockClient{}, []string{"example.com"}, false)
	in := []*endpoint.Endpoint{
		{DNSName: "a.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}, RecordTTL: 0},
		{DNSName: "b.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.2"}, RecordTTL: 120},
	}
	out, err := p.AdjustEndpoints(in)
	require.NoError(t, err)
	assert.Equal(t, endpoint.TTL(3600), out[0].RecordTTL)
	assert.Equal(t, endpoint.TTL(120), out[1].RecordTTL)
}

func minTTLProvider(client apiClient, zones []string, minTTL int) *NamesiloProvider {
	return New(Options{
		Client:       client,
		DomainFilter: zones,
		DefaultTTL:   3600,
		MinTTL:       minTTL,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestAdjustEndpoints_ClampsToMinTTL(t *testing.T) {
	p := minTTLProvider(&mockClient{}, []string{"example.com"}, 3600)
	in := []*endpoint.Endpoint{
		{DNSName: "low.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}, RecordTTL: 300},
		{DNSName: "unset.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.2"}, RecordTTL: 0},
		{DNSName: "high.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.3"}, RecordTTL: 7200},
	}
	out, err := p.AdjustEndpoints(in)
	require.NoError(t, err)
	assert.Equal(t, endpoint.TTL(3600), out[0].RecordTTL, "sub-floor TTL clamps up to the Namesilo minimum")
	assert.Equal(t, endpoint.TTL(3600), out[1].RecordTTL, "unset TTL becomes the default (== floor here)")
	assert.Equal(t, endpoint.TTL(7200), out[2].RecordTTL, "above-floor TTL is left untouched")
}

func TestApplyCreate_ClampsTTLToMin(t *testing.T) {
	m := &mockClient{}
	p := minTTLProvider(m, []string{"example.com"}, 3600)

	// A create whose TTL slipped through below the floor is still clamped on the
	// write path, so we never send Namesilo a TTL it would reject.
	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{{DNSName: "a.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}, RecordTTL: 300}},
	})
	require.NoError(t, err)

	adds := m.opsOf("add")
	require.Len(t, adds, 1)
	assert.Equal(t, 3600, adds[0].in.TTL)
}

func TestGetDomainFilter(t *testing.T) {
	p := testProvider(&mockClient{}, []string{"example.com"}, false)
	assert.True(t, p.GetDomainFilter().Match("example.com"))
	assert.False(t, p.GetDomainFilter().Match("other.org"))
}

func TestApplyChanges_DryRunMakesNoCalls(t *testing.T) {
	m := &mockClient{}
	p := testProvider(m, []string{"example.com"}, true)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{{DNSName: "a.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}}},
	})
	require.NoError(t, err)
	assert.Empty(t, m.calls, "dry-run must not touch the API")
}

func TestApplyCreate_RelativizesHostAndPerTarget(t *testing.T) {
	m := &mockClient{}
	p := testProvider(m, []string{"example.com"}, false)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{
			{DNSName: "www.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1", "192.0.2.2"}, RecordTTL: 300},
			{DNSName: "example.com", RecordType: "TXT", Targets: endpoint.Targets{"heritage=external-dns"}},
		},
	})
	require.NoError(t, err)

	adds := m.opsOf("add")
	require.Len(t, adds, 3)
	assert.Equal(t, namesilo.RecordInput{Type: "A", Host: "www", Value: "192.0.2.1", TTL: 300}, adds[0].in)
	assert.Equal(t, namesilo.RecordInput{Type: "A", Host: "www", Value: "192.0.2.2", TTL: 300}, adds[1].in)
	// apex host relativizes to "" and TTL falls back to the default.
	assert.Equal(t, namesilo.RecordInput{Type: "TXT", Host: "", Value: "heritage=external-dns", TTL: 3600}, adds[2].in)
}

func TestApplyCreate_MXParsesPreference(t *testing.T) {
	m := &mockClient{}
	p := testProvider(m, []string{"example.com"}, false)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{{DNSName: "example.com", RecordType: "MX", Targets: endpoint.Targets{"10 mail.example.com"}, RecordTTL: 3600}},
	})
	require.NoError(t, err)

	adds := m.opsOf("add")
	require.Len(t, adds, 1)
	assert.Equal(t, namesilo.RecordInput{Type: "MX", Host: "", Value: "mail.example.com", TTL: 3600, Distance: 10}, adds[0].in)
}

func TestApplyCreate_InvalidMXErrors(t *testing.T) {
	m := &mockClient{}
	p := testProvider(m, []string{"example.com"}, false)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{{DNSName: "example.com", RecordType: "MX", Targets: endpoint.Targets{"mail.example.com"}}},
	})
	require.Error(t, err)
	assert.Empty(t, m.opsOf("add"))
}

func TestApplyDelete_ResolvesIDAndIsIdempotent(t *testing.T) {
	m := &mockClient{records: map[string][]namesilo.Record{
		"example.com": {{ID: "r1", Type: "A", Host: "www.example.com", Value: "192.0.2.1", TTL: 3600}},
	}}
	p := testProvider(m, []string{"example.com"}, false)

	// Deleting a present target resolves its id; deleting a missing one is a no-op.
	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Delete: []*endpoint.Endpoint{
			{DNSName: "www.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1", "192.0.2.99"}},
		},
	})
	require.NoError(t, err)

	dels := m.opsOf("delete")
	require.Len(t, dels, 1)
	assert.Equal(t, "r1", dels[0].id)
}

func TestApplyUpdate_TargetChangeDeletesAndAdds(t *testing.T) {
	m := &mockClient{records: map[string][]namesilo.Record{
		"example.com": {{ID: "r1", Type: "A", Host: "www.example.com", Value: "192.0.2.1", TTL: 3600}},
	}}
	p := testProvider(m, []string{"example.com"}, false)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		UpdateOld: []*endpoint.Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}, RecordTTL: 3600}},
		UpdateNew: []*endpoint.Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.2"}, RecordTTL: 3600}},
	})
	require.NoError(t, err)

	require.Len(t, m.opsOf("delete"), 1)
	assert.Equal(t, "r1", m.opsOf("delete")[0].id)
	adds := m.opsOf("add")
	require.Len(t, adds, 1)
	assert.Equal(t, "192.0.2.2", adds[0].in.Value)
	assert.Empty(t, m.opsOf("update"))
}

func TestApplyUpdate_TTLChangeUpdatesInPlace(t *testing.T) {
	m := &mockClient{records: map[string][]namesilo.Record{
		"example.com": {{ID: "r1", Type: "A", Host: "www.example.com", Value: "192.0.2.1", TTL: 3600}},
	}}
	p := testProvider(m, []string{"example.com"}, false)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		UpdateOld: []*endpoint.Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}, RecordTTL: 3600}},
		UpdateNew: []*endpoint.Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}, RecordTTL: 600}},
	})
	require.NoError(t, err)

	assert.Empty(t, m.opsOf("delete"))
	assert.Empty(t, m.opsOf("add"))
	updates := m.opsOf("update")
	require.Len(t, updates, 1)
	assert.Equal(t, "r1", updates[0].id)
	assert.Equal(t, 600, updates[0].in.TTL)
}

func TestApplyCreate_UnresolvableZoneIsSkipped(t *testing.T) {
	m := &mockClient{}
	p := testProvider(m, []string{"example.com"}, false)

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{{DNSName: "host.other.org", RecordType: "A", Targets: endpoint.Targets{"192.0.2.1"}}},
	})
	require.NoError(t, err)
	assert.Empty(t, m.opsOf("add"), "names outside managed zones are skipped, not applied")
}

func TestResolveZoneAndRelativeHost(t *testing.T) {
	zones := []string{"example.com", "sub.example.com"}

	z, ok := resolveZone("a.sub.example.com", zones)
	require.True(t, ok)
	assert.Equal(t, "sub.example.com", z, "longest matching zone wins")
	assert.Equal(t, "a", relativeHost("a.sub.example.com", z))

	z, ok = resolveZone("example.com.", zones)
	require.True(t, ok)
	assert.Equal(t, "example.com", z)
	assert.Equal(t, "", relativeHost("example.com.", z), "apex relativizes to empty host")

	_, ok = resolveZone("nope.org", zones)
	assert.False(t, ok)
}
