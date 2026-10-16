package memoryrepo

import (
	"encoding/json"
	"strings"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/importedconfig"
)

// Derived once per immutable structural view. Telemetry overlays do not alter
// ownership, provenance or runtime configuration. Authoritative mutations keep
// using the complete relationship checks under the store lock.
type forwardingProjection struct {
	imported, delegated map[string]map[string]bool
	summaries           map[string]forwarding.OutboundSummary
}

func (s *Store) buildForwardingProjectionLocked() *forwardingProjection {
	p := &forwardingProjection{
		imported:  make(map[string]map[string]bool),
		delegated: make(map[string]map[string]bool),
		summaries: make(map[string]forwarding.OutboundSummary),
	}
	grant := func(index map[string]map[string]bool, ruleID, actor string) {
		if index[ruleID] == nil {
			index[ruleID] = make(map[string]bool)
		}
		index[ruleID][actor] = true
	}
	for _, record := range s.vlessBindings {
		b := record.Binding
		r, exists := s.forwardRules[b.ForwardingRuleID]
		pool, found := s.endpointPools[b.EndpointPoolID]
		baseline := r.ImportedTraffic
		actor := pool.OwnerID
		if !exists || !found || actor == "" || r.VLESSOutboundMode != forwarding.VLESSOutboundNative ||
			baseline.SourceID == "" || !baseline.Valid() ||
			(r.OwnerKind != forwarding.OwnerCustomer && !r.IsUnassignedImport() && !(r.OwnerKind == forwarding.OwnerAdministrator && r.OwnerID == actor)) ||
			pool.RuleID != r.ID || pool.GroupID != r.EntryGroupID || pool.Port != r.ListenPort ||
			b.CustomerID != r.CustomerID || b.ImportedFrom == nil ||
			b.ImportedFrom.BatchID != baseline.BatchID || b.ImportedFrom.LineID != baseline.LineID {
			continue
		}
		grant(p.imported, r.ID, actor)
	}
	for key, mutation := range s.businessIdempotency {
		actor, rest, ok := strings.Cut(key, "\x00forwarding.create\x00")
		if !ok || actor == "" || rest == "" {
			continue
		}
		r, exists := s.forwardRules[mutation.ResourceID]
		if !exists || !r.OwnedByCustomer(r.CustomerID) {
			continue
		}
		var created forwarding.Rule
		if json.Unmarshal(mutation.Result, &created) == nil && created.OwnerKind == forwarding.OwnerCustomer &&
			created.OwnerID == r.CustomerID && created.CustomerID == r.CustomerID && created.ID == r.ID {
			grant(p.delegated, r.ID, actor)
		}
	}
	selected := make(map[string]string)
	for id, material := range s.vlessRuntimeMaterials {
		r := s.forwardRules[material.ForwardingRuleID]
		if r.VLESSOutboundMode != forwarding.VLESSOutboundNative || len(material.NativeConfig) == 0 {
			continue
		}
		if previous, ok := selected[r.ID]; ok && previous < id {
			continue
		}
		selected[r.ID] = id
	}
	for id, r := range s.forwardRules {
		if r.VLESSOutboundMode != forwarding.VLESSOutboundNative {
			continue
		}
		summary := forwarding.OutboundSummary{Mode: "UNKNOWN"}
		if materialID, ok := selected[id]; ok {
			mode, host, port := importedconfig.NativeOutboundSummary(s.vlessRuntimeMaterials[materialID].NativeConfig)
			summary = forwarding.OutboundSummary{Mode: mode, Host: host, Port: port}
		}
		p.summaries[id] = summary
	}
	return p
}
