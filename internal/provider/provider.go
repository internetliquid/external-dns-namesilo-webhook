// Package provider implements the ExternalDNS provider.Provider interface on
// top of the Namesilo API client. It maps between ExternalDNS endpoints and
// Namesilo records and translates a plan.Changes into the individual
// add/update/delete API calls Namesilo requires (there is no bulk apply).
package provider

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/internetliquid/external-dns-namesilo-webhook/internal/namesilo"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

// supportedTypes are the record types mapped between ExternalDNS and Namesilo.
// TXT matters because ExternalDNS uses TXT records for its ownership registry.
var supportedTypes = map[string]bool{
	endpoint.RecordTypeA:     true,
	endpoint.RecordTypeAAAA:  true,
	endpoint.RecordTypeCNAME: true,
	endpoint.RecordTypeMX:    true,
	endpoint.RecordTypeTXT:   true,
}

// apiClient is the subset of *namesilo.Client the provider needs, expressed as
// an interface so the provider can be unit-tested without a live API or HTTP.
type apiClient interface {
	ListRecords(ctx context.Context, domain string) ([]namesilo.Record, error)
	AddRecord(ctx context.Context, domain string, in namesilo.RecordInput) (string, error)
	UpdateRecord(ctx context.Context, domain, recordID string, in namesilo.RecordInput) error
	DeleteRecord(ctx context.Context, domain, recordID string) error
}

// NamesiloProvider implements provider.Provider.
type NamesiloProvider struct {
	provider.BaseProvider
	client       apiClient
	domainFilter *endpoint.DomainFilter
	zones        []string // managed zones, from the required DOMAIN_FILTER
	defaultTTL   int
	minTTL       int // floor applied to every TTL before writing; 0 disables
	dryRun       bool
	logger       *slog.Logger
}

// Options configures a NamesiloProvider.
type Options struct {
	Client       apiClient
	DomainFilter []string
	DefaultTTL   int
	MinTTL       int
	DryRun       bool
	Logger       *slog.Logger
}

// New constructs a NamesiloProvider.
func New(opts Options) *NamesiloProvider {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &NamesiloProvider{
		client:       opts.Client,
		domainFilter: endpoint.NewDomainFilter(opts.DomainFilter),
		zones:        normalizeZones(opts.DomainFilter),
		defaultTTL:   opts.DefaultTTL,
		minTTL:       opts.MinTTL,
		dryRun:       opts.DryRun,
		logger:       logger,
	}
}

// GetDomainFilter returns the configured domain filter (the managed zones),
// which ExternalDNS reads during negotiation to scope reconciliation.
func (p *NamesiloProvider) GetDomainFilter() endpoint.DomainFilterInterface {
	return p.domainFilter
}

// AdjustEndpoints canonicalizes desired endpoints so the change plan matches
// what Records would return. It fills in the default TTL when ExternalDNS left
// it unset and clamps every TTL up to the Namesilo floor. Doing the clamp here
// (not only on the write path) is what keeps the plan convergent: Namesilo will
// store at least minTTL, so the desired state has to say minTTL too or every
// reconcile would re-diff a sub-floor TTL against the stored one forever.
func (p *NamesiloProvider) AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	for _, ep := range endpoints {
		if ep.RecordTTL == 0 {
			ep.RecordTTL = endpoint.TTL(p.defaultTTL)
		}
		if p.minTTL > 0 && ep.RecordTTL < endpoint.TTL(p.minTTL) {
			ep.RecordTTL = endpoint.TTL(p.minTTL)
		}
	}
	return endpoints, nil
}

// Records returns every supported record across the managed zones, grouped into
// ExternalDNS endpoints (one endpoint per name+type, with all values as
// targets). A failure to list any zone fails the whole call so ExternalDNS
// retries rather than acting on a partial view.
func (p *NamesiloProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	var endpoints []*endpoint.Endpoint
	for _, zone := range p.zones {
		records, err := p.client.ListRecords(ctx, zone)
		if err != nil {
			return nil, fmt.Errorf("listing records for zone %s: %w", zone, err)
		}
		endpoints = append(endpoints, recordsToEndpoints(records)...)
	}
	return endpoints, nil
}

// ApplyChanges translates a plan into individual Namesilo API calls. Record ids
// for updates and deletes are resolved from a snapshot of each affected zone
// taken at the start of the call, rather than relying on ProviderSpecific data
// surviving the round trip through ExternalDNS.
func (p *NamesiloProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	if p.dryRun {
		p.logger.Info("dry-run: skipping Namesilo API calls",
			"create", len(changes.Create),
			"updateOld", len(changes.UpdateOld),
			"updateNew", len(changes.UpdateNew),
			"delete", len(changes.Delete))
		return nil
	}

	zones := p.zones
	state := &applyState{client: p.client, indexes: map[string]recordIndex{}}

	for _, ep := range changes.Create {
		if err := p.applyCreate(ctx, ep, zones); err != nil {
			return err
		}
	}
	for i, newEP := range changes.UpdateNew {
		var oldEP *endpoint.Endpoint
		if i < len(changes.UpdateOld) {
			oldEP = changes.UpdateOld[i]
		}
		if err := p.applyUpdate(ctx, state, oldEP, newEP, zones); err != nil {
			return err
		}
	}
	for _, ep := range changes.Delete {
		if err := p.applyDelete(ctx, state, ep, zones); err != nil {
			return err
		}
	}
	return nil
}

func (p *NamesiloProvider) applyCreate(ctx context.Context, ep *endpoint.Endpoint, zones []string) error {
	zone, ok := resolveZone(ep.DNSName, zones)
	if !ok {
		p.logger.Warn("skipping create: no managed zone for name", "dnsName", ep.DNSName, "type", ep.RecordType)
		return nil
	}
	if !supportedTypes[ep.RecordType] {
		p.logger.Warn("skipping create: unsupported record type", "dnsName", ep.DNSName, "type", ep.RecordType)
		return nil
	}

	host := relativeHost(ep.DNSName, zone)
	ttl := p.ttlOrDefault(ep.RecordTTL)
	for _, target := range ep.Targets {
		in, err := recordInput(ep.RecordType, host, target, ttl)
		if err != nil {
			return fmt.Errorf("create %s %s: %w", ep.RecordType, ep.DNSName, err)
		}
		p.logger.Info("creating record", "zone", zone, "host", host, "type", ep.RecordType, "value", in.Value)
		if _, err := p.client.AddRecord(ctx, zone, in); err != nil {
			return fmt.Errorf("create %s %s: %w", ep.RecordType, ep.DNSName, err)
		}
	}
	return nil
}

func (p *NamesiloProvider) applyUpdate(ctx context.Context, state *applyState, oldEP, newEP *endpoint.Endpoint, zones []string) error {
	zone, ok := resolveZone(newEP.DNSName, zones)
	if !ok {
		p.logger.Warn("skipping update: no managed zone for name", "dnsName", newEP.DNSName, "type", newEP.RecordType)
		return nil
	}
	if !supportedTypes[newEP.RecordType] {
		p.logger.Warn("skipping update: unsupported record type", "dnsName", newEP.DNSName, "type", newEP.RecordType)
		return nil
	}

	idx, err := state.index(ctx, zone)
	if err != nil {
		return err
	}

	host := relativeHost(newEP.DNSName, zone)
	newTTL := p.ttlOrDefault(newEP.RecordTTL)
	name := normalizeName(newEP.DNSName)

	oldTargets := targetSet(oldEP)
	newTargets := targetSet(newEP)

	// Remove targets that are gone.
	for target := range oldTargets {
		if _, keep := newTargets[target]; keep {
			continue
		}
		rec, found := idx.lookup(newEP.RecordType, name, target)
		if !found {
			p.logger.Warn("update: record to remove not found", "dnsName", newEP.DNSName, "type", newEP.RecordType, "target", target)
			continue
		}
		p.logger.Info("deleting record (update)", "zone", zone, "host", host, "type", newEP.RecordType, "value", rec.Value)
		if err := p.client.DeleteRecord(ctx, zone, rec.ID); err != nil {
			return fmt.Errorf("update %s %s: %w", newEP.RecordType, newEP.DNSName, err)
		}
	}

	// Add new targets, and update surviving ones whose TTL changed.
	ttlChanged := oldEP != nil && oldEP.RecordTTL != newEP.RecordTTL
	for target := range newTargets {
		in, err := recordInput(newEP.RecordType, host, target, newTTL)
		if err != nil {
			return fmt.Errorf("update %s %s: %w", newEP.RecordType, newEP.DNSName, err)
		}
		if _, existed := oldTargets[target]; existed {
			if !ttlChanged {
				continue
			}
			rec, found := idx.lookup(newEP.RecordType, name, target)
			if !found {
				// Surviving target not present upstream: create it.
				p.logger.Info("creating record (update, missing upstream)", "zone", zone, "host", host, "type", newEP.RecordType, "value", in.Value)
				if _, err := p.client.AddRecord(ctx, zone, in); err != nil {
					return fmt.Errorf("update %s %s: %w", newEP.RecordType, newEP.DNSName, err)
				}
				continue
			}
			p.logger.Info("updating record TTL", "zone", zone, "host", host, "type", newEP.RecordType, "value", in.Value, "ttl", newTTL)
			if err := p.client.UpdateRecord(ctx, zone, rec.ID, in); err != nil {
				return fmt.Errorf("update %s %s: %w", newEP.RecordType, newEP.DNSName, err)
			}
			continue
		}
		p.logger.Info("creating record (update)", "zone", zone, "host", host, "type", newEP.RecordType, "value", in.Value)
		if _, err := p.client.AddRecord(ctx, zone, in); err != nil {
			return fmt.Errorf("update %s %s: %w", newEP.RecordType, newEP.DNSName, err)
		}
	}
	return nil
}

func (p *NamesiloProvider) applyDelete(ctx context.Context, state *applyState, ep *endpoint.Endpoint, zones []string) error {
	zone, ok := resolveZone(ep.DNSName, zones)
	if !ok {
		p.logger.Warn("skipping delete: no managed zone for name", "dnsName", ep.DNSName, "type", ep.RecordType)
		return nil
	}

	idx, err := state.index(ctx, zone)
	if err != nil {
		return err
	}

	name := normalizeName(ep.DNSName)
	for _, target := range ep.Targets {
		rec, found := idx.lookup(ep.RecordType, name, target)
		if !found {
			// Already gone: deletion is idempotent.
			p.logger.Warn("delete: record not found, skipping", "dnsName", ep.DNSName, "type", ep.RecordType, "target", target)
			continue
		}
		p.logger.Info("deleting record", "zone", zone, "type", ep.RecordType, "value", rec.Value)
		if err := p.client.DeleteRecord(ctx, zone, rec.ID); err != nil {
			return fmt.Errorf("delete %s %s: %w", ep.RecordType, ep.DNSName, err)
		}
	}
	return nil
}

func (p *NamesiloProvider) ttlOrDefault(ttl endpoint.TTL) int {
	v := int(ttl)
	if v <= 0 {
		v = p.defaultTTL
	}
	if p.minTTL > 0 && v < p.minTTL {
		v = p.minTTL
	}
	return v
}

// --- mapping helpers ---

// recordsToEndpoints groups Namesilo records into ExternalDNS endpoints, one per
// name+type with all values collected as targets, preserving input order.
func recordsToEndpoints(records []namesilo.Record) []*endpoint.Endpoint {
	type key struct{ name, typ string }
	grouped := make(map[key]*endpoint.Endpoint)
	var order []key

	for _, r := range records {
		if !supportedTypes[r.Type] {
			continue
		}
		name := normalizeName(r.Host)
		k := key{name, r.Type}
		target := recordToTarget(r)

		if ep, ok := grouped[k]; ok {
			ep.Targets = append(ep.Targets, target)
			continue
		}
		grouped[k] = &endpoint.Endpoint{
			DNSName:    name,
			RecordType: r.Type,
			Targets:    endpoint.Targets{target},
			RecordTTL:  endpoint.TTL(r.TTL),
		}
		order = append(order, k)
	}

	out := make([]*endpoint.Endpoint, 0, len(order))
	for _, k := range order {
		out = append(out, grouped[k])
	}
	return out
}

// recordToTarget renders a Namesilo record value as an ExternalDNS target.
func recordToTarget(r namesilo.Record) string {
	switch r.Type {
	case endpoint.RecordTypeMX:
		// ExternalDNS represents MX targets as "<preference> <exchange>".
		return fmt.Sprintf("%d %s", r.Distance, r.Value)
	case endpoint.RecordTypeTXT:
		// ExternalDNS works with unquoted TXT values; strip surrounding quotes
		// Namesilo may include so we don't churn on a quoting mismatch.
		// TODO: verify against live API — confirm Namesilo returns/accepts TXT
		// values without enclosing quotes.
		return strings.Trim(r.Value, `"`)
	default:
		return r.Value
	}
}

// recordInput builds the Namesilo create/update parameters for one target.
func recordInput(recordType, host, target string, ttl int) (namesilo.RecordInput, error) {
	in := namesilo.RecordInput{Type: recordType, Host: host, TTL: ttl}
	switch recordType {
	case endpoint.RecordTypeMX:
		dist, value, err := splitMX(target)
		if err != nil {
			return in, err
		}
		in.Distance = dist
		in.Value = value
	case endpoint.RecordTypeTXT:
		in.Value = target // TODO: verify whether Namesilo expects quoted TXT values
	default:
		in.Value = target
	}
	return in, nil
}

func splitMX(target string) (int, string, error) {
	parts := strings.SplitN(strings.TrimSpace(target), " ", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("invalid MX target %q: expected \"<preference> <exchange>\"", target)
	}
	dist, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, "", fmt.Errorf("invalid MX preference in %q: %w", target, err)
	}
	return dist, strings.TrimSpace(parts[1]), nil
}

// resolveZone returns the longest managed zone that is a suffix of the name.
func resolveZone(dnsName string, zones []string) (string, bool) {
	name := normalizeName(dnsName)
	best := ""
	for _, zone := range zones {
		if name == zone || strings.HasSuffix(name, "."+zone) {
			if len(zone) > len(best) {
				best = zone
			}
		}
	}
	return best, best != ""
}

// relativeHost returns the host label relative to the zone ("" for the apex).
func relativeHost(dnsName, zone string) string {
	name := normalizeName(dnsName)
	if name == zone {
		return ""
	}
	return strings.TrimSuffix(name, "."+zone)
}

// normalizeName lowercases a DNS name and strips any trailing dot so names from
// ExternalDNS (no trailing dot) and Namesilo compare equal.
func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func normalizeZones(zones []string) []string {
	if len(zones) == 0 {
		return nil
	}
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		if z = normalizeName(z); z != "" {
			out = append(out, z)
		}
	}
	return out
}

func targetSet(ep *endpoint.Endpoint) map[string]struct{} {
	set := make(map[string]struct{})
	if ep == nil {
		return set
	}
	for _, t := range ep.Targets {
		set[t] = struct{}{}
	}
	return set
}

// --- record-id resolution ---

// recordIndex maps a normalized (type, name, value) tuple to the Namesilo record
// so updates and deletes can find the record id Namesilo needs.
type recordIndex map[string]namesilo.Record

func (idx recordIndex) lookup(recordType, name, target string) (namesilo.Record, bool) {
	value, dist := targetValueAndDistance(recordType, target)
	rec, ok := idx[indexKey(recordType, name, value, dist)]
	return rec, ok
}

// applyState memoizes per-zone record indexes for the duration of one
// ApplyChanges call so each zone is listed at most once.
type applyState struct {
	client  apiClient
	indexes map[string]recordIndex
}

func (s *applyState) index(ctx context.Context, zone string) (recordIndex, error) {
	if idx, ok := s.indexes[zone]; ok {
		return idx, nil
	}
	records, err := s.client.ListRecords(ctx, zone)
	if err != nil {
		return nil, fmt.Errorf("listing records for zone %s: %w", zone, err)
	}
	idx := make(recordIndex, len(records))
	for _, r := range records {
		name := normalizeName(r.Host)
		value, dist := recordValueAndDistance(r)
		idx[indexKey(r.Type, name, value, dist)] = r
	}
	s.indexes[zone] = idx
	return idx, nil
}

func indexKey(recordType, name, value, distance string) string {
	return strings.Join([]string{recordType, name, value, distance}, "\x00")
}

// recordValueAndDistance derives the index value/distance from a Namesilo record.
func recordValueAndDistance(r namesilo.Record) (string, string) {
	switch r.Type {
	case endpoint.RecordTypeMX:
		return r.Value, strconv.Itoa(r.Distance)
	case endpoint.RecordTypeTXT:
		return strings.Trim(r.Value, `"`), ""
	default:
		return r.Value, ""
	}
}

// targetValueAndDistance derives the index value/distance from an ExternalDNS
// target so it matches recordValueAndDistance for the same logical record.
func targetValueAndDistance(recordType, target string) (string, string) {
	switch recordType {
	case endpoint.RecordTypeMX:
		if dist, value, err := splitMX(target); err == nil {
			return value, strconv.Itoa(dist)
		}
		return target, ""
	default:
		return target, ""
	}
}
