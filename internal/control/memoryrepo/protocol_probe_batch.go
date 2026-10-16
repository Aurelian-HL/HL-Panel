package memoryrepo

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"maps"
	"time"
)

// ConfigureProtocolProbes initializes only missing identities in one audited
// transaction and compiles each affected node once. Existing identities never
// rotate when a scheduler retries or another writer has already initialized one.
func (s *Store) ConfigureProtocolProbes(ctx context.Context, configs map[string]gatewaymembership.ProtocolProbeConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(configs) > 512 {
		return faults.ErrValidation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	additions := make(map[string]gatewaymembership.ProtocolProbeConfig)
	groups := []string{}
	identities := make(map[string]bool)
	for _, config := range s.protocolProbes {
		identities[config.UUID] = true
	}
	for id, config := range configs {
		if _, exists := s.protocolProbes[id]; exists {
			continue
		}
		rule, exists := s.forwardRules[id]
		if !exists {
			return faults.ErrNotFound
		}
		if config.UUID == "" || config.EchoPort < 1 || config.EchoPort > 65535 {
			return faults.ErrValidation
		}
		if identities[config.UUID] {
			return faults.ErrConflict
		}
		for _, binding := range s.vlessBindings {
			if binding.CredentialUUID == config.UUID {
				return faults.ErrConflict
			}
		}
		identities[config.UUID] = true
		additions[id] = config
		groups = append(groups, rule.EntryGroupID)
	}
	if len(additions) == 0 {
		return nil
	}
	event, err := audit.NewEvent(time.Now().UTC(), "system", "protocol-runner", "protocol_probe.initialize_batch", "protocol_probe", "batch", "succeeded", nil)
	if err != nil {
		return err
	}
	previous := maps.Clone(s.protocolProbes)
	for id, config := range additions {
		s.protocolProbes[id] = config
	}
	if err := s.recompileForwardingGroupsLocked(groups, time.Now().UTC()); err != nil {
		s.protocolProbes = previous
		return err
	}
	s.gatewayMutationRevision++
	s.auditEvents = append(s.auditEvents, event)
	return nil
}
