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
	if json.Unmarshal(input.Config.Config, &bundle) != nil || input.Config.CreatedAt.After(endedAt) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid usage bundle or interval", faults.ErrValidation)
	}
	var match *agentv1.ConfigurationFragment
	for i := range bundle.Fragments {
		f := &bundle.Fragments[i]
		if f.GroupID != generations.ForwardingFragmentID(f.Engine, ruleID) {
			continue
		}
		if match != nil {
			return LegacyRuleMetadata{}, fmt.Errorf("%w: duplicate usage fragment", faults.ErrValidation)
		}
		match = f
	}
	if match == nil || match.Usage == nil || !deploymentreceipts.VerifiedHistoricalBundle(input, nodeID, int64(generation), match.Engine, bundle) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: usage configuration is not applied and verified", faults.ErrValidation)
	}
	m := match.Usage
	if m.RuleID != ruleID || !validIdentifier(m.CustomerID) || !validIdentifier(m.EntryGroupID) ||
		(m.ExitGroupID != "" && !validIdentifier(m.ExitGroupID)) || m.Protocol != "tcp" ||
		!validMultiplier(m.EntryMultiplierMicros) || !validMultiplier(m.ExitMultiplierMicros) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid compiled usage metadata", faults.ErrValidation)
	}
	nativeMatch := match.Engine == agentv1.EngineGOST && hasGOSTService(match.Config, "forward-"+ruleID) ||
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
