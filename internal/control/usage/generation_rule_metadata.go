package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"math"
	"time"
)

func (p appliedRuleMetadataProvider) ResolveRuleUsageMetadataAt(ctx context.Context, nodeID, ruleID string, generation agentv1.NodeConfigGeneration, endedAt time.Time) (LegacyRuleMetadata, error) {
	if generation == 0 {
		return p.ResolveLegacyRuleUsageMetadata(ctx, nodeID, ruleID)
	}
	if !validIdentifier(nodeID) || !validIdentifier(ruleID) || uint64(generation) > math.MaxInt64 {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid usage configuration identity", faults.ErrValidation)
	}
	repo, ok := p.repository.(deploymentreceipts.HistoricalRepository)
	if !ok {
		return LegacyRuleMetadata{}, errorsUnavailable()
	}
	input, err := repo.UsageConfigInput(ctx, nodeID, int64(generation))
	if err != nil {
		return LegacyRuleMetadata{}, err
	}
	var bundle agentv1.ConfigurationBundle
	if json.Unmarshal(input.Config.Config, &bundle) != nil || input.Config.CreatedAt.IsZero() || input.Config.CreatedAt.After(endedAt) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid usage bundle or interval", faults.ErrValidation)
	}
	var match *agentv1.ConfigurationFragment
	for i := range bundle.Fragments {
		f := &bundle.Fragments[i]
		if f.GroupID != generations.ForwardingFragmentID(agentv1.EngineXray, ruleID) &&
			f.GroupID != generations.ForwardingFragmentID(agentv1.EngineGOST, ruleID) {
			continue
		}
		if match != nil {
			return LegacyRuleMetadata{}, fmt.Errorf("%w: duplicate usage fragment", faults.ErrValidation)
		}
		match = f
	}
	if match == nil {
		// Xray keeps counters for removed inbounds, including final bytes of
		// existing connections. Older reporters stamp those deltas with the new
		// LKG generation. Resolve only a completely absent rule, and require
		// real same-attempt receipts for both the removal and latest old rule.
		// Never use history to rescue bad metadata/receipts in a present rule.
		history, ok := p.repository.(deploymentreceipts.HistoricalRuleRepository)
		if !ok || !deploymentreceipts.VerifiedHistoricalTransition(input, nodeID, int64(generation), bundle) {
			return LegacyRuleMetadata{}, fmt.Errorf("%w: usage configuration is not applied and verified", faults.ErrValidation)
		}
		previous, err := history.UsageRuleConfigInput(ctx, nodeID, ruleID, int64(generation))
		if err != nil {
			return LegacyRuleMetadata{}, err
		}
		if previous.Config.Generation <= 0 || previous.Config.Generation >= int64(generation) ||
			previous.Config.CreatedAt.IsZero() || previous.Config.CreatedAt.After(input.Config.CreatedAt) {
			return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid previous usage configuration", faults.ErrValidation)
		}
		// Resolve once with the selected evidence, with no recursive fallback.
		return historicalRuleMetadata(previous, nodeID, ruleID, previous.Config.Generation, endedAt)
	}
	return verifiedFragmentMetadata(input, nodeID, ruleID, int64(generation), bundle, match)
}

func historicalRuleMetadata(input deploymentreceipts.HistoricalInput, nodeID, ruleID string, generation int64, endedAt time.Time) (LegacyRuleMetadata, error) {
	var bundle agentv1.ConfigurationBundle
	if json.Unmarshal(input.Config.Config, &bundle) != nil || input.Config.CreatedAt.After(endedAt) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid usage bundle or interval", faults.ErrValidation)
	}
	var match *agentv1.ConfigurationFragment
	for i := range bundle.Fragments {
		f := &bundle.Fragments[i]
		if f.GroupID == generations.ForwardingFragmentID(agentv1.EngineXray, ruleID) || f.GroupID == generations.ForwardingFragmentID(agentv1.EngineGOST, ruleID) {
			if match != nil {
				return LegacyRuleMetadata{}, fmt.Errorf("%w: duplicate usage fragment", faults.ErrValidation)
			}
			match = f
		}
	}
	return verifiedFragmentMetadata(input, nodeID, ruleID, generation, bundle, match)
}

func verifiedFragmentMetadata(input deploymentreceipts.HistoricalInput, nodeID, ruleID string, generation int64, bundle agentv1.ConfigurationBundle, match *agentv1.ConfigurationFragment) (LegacyRuleMetadata, error) {
	if match == nil || match.Usage == nil || match.GroupID != generations.ForwardingFragmentID(match.Engine, ruleID) ||
		!deploymentreceipts.VerifiedHistoricalBundle(input, nodeID, generation, match.Engine, bundle) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: usage configuration is not applied and verified", faults.ErrValidation)
	}
	m := match.Usage
	if m.RuleID != ruleID || !validIdentifier(m.CustomerID) || !validIdentifier(m.EntryGroupID) ||
		(m.ExitGroupID != "" && !validIdentifier(m.ExitGroupID)) || (m.Protocol != "tcp" && m.Protocol != "udp") ||
		!validMultiplier(m.EntryMultiplierMicros) || !validMultiplier(m.ExitMultiplierMicros) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid compiled usage metadata", faults.ErrValidation)
	}
	nativeMatch := match.Engine == agentv1.EngineGOST && hasGOSTService(match.Config, "forward-"+ruleID, m.Protocol) ||
		match.Engine == agentv1.EngineXray && hasXrayInboundTag(match.Config, "vless-reality-fwd_"+ruleID, forwarding.Rule{
			ID: ruleID, CustomerID: m.CustomerID, EntryGroupID: m.EntryGroupID, ExitGroupID: m.ExitGroupID, Protocol: forwarding.Protocol(m.Protocol)})
	if !nativeMatch {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: usage counter is absent from verified fragment", faults.ErrValidation)
	}
	return LegacyRuleMetadata{RuleID: ruleID, CustomerID: m.CustomerID, EntryGroup: m.EntryGroupID,
		ExitGroup: m.ExitGroupID, Protocol: m.Protocol, EntryMultiplierMicros: m.EntryMultiplierMicros, ExitMultiplierMicros: m.ExitMultiplierMicros}, nil
}

func (s *Service) ruleMetadataForReport(ctx context.Context, report *Report, ruleID string) (LegacyRuleMetadata, error) {
	if report.ConfigGeneration != 0 {
		p, ok := s.legacyRuleMetadata.(GenerationRuleMetadataProvider)
		if !ok {
			return LegacyRuleMetadata{}, errorsUnavailable()
		}
		return p.ResolveRuleUsageMetadataAt(ctx, report.NodeID, ruleID, report.ConfigGeneration, report.PeriodEndedAt)
	}
	return s.legacyRuleMetadata.ResolveLegacyRuleUsageMetadata(ctx, report.NodeID, ruleID)
}
