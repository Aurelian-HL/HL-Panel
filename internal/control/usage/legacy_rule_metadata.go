package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type appliedRuleMetadataProvider struct {
	repository deploymentreceipts.Repository
}

// NewLegacyRuleMetadataProvider resolves legacy Xray tags and GOST service
// names only from the authenticated node's current, successfully applied rule
// configuration.
func NewLegacyRuleMetadataProvider(repository deploymentreceipts.Repository) LegacyRuleMetadataProvider {
	return appliedRuleMetadataProvider{repository: repository}
}

func (provider appliedRuleMetadataProvider) ResolveLegacyRuleUsageMetadata(ctx context.Context, nodeID, ruleID string) (LegacyRuleMetadata, error) {
	nodeID = strings.TrimSpace(nodeID)
	ruleID = strings.TrimSpace(ruleID)
	if !validIdentifier(nodeID) || !validIdentifier(ruleID) {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: invalid legacy usage identity", faults.ErrValidation)
	}
	if provider.repository == nil {
		return LegacyRuleMetadata{}, errorsUnavailable()
	}
	input, err := provider.repository.RuleNodeDeploymentInput(ctx, ruleID, nodeID)
	if err != nil {
		return LegacyRuleMetadata{}, err
	}
	if input.Rule.ID != ruleID || input.Node.ID != nodeID || input.Rule.Protocol != forwarding.ProtocolTCP {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: legacy rule metadata does not match an eligible TCP rule", faults.ErrValidation)
	}
	status := deploymentreceipts.Evaluate(input)
	if !status.ReceiptVerified {
		return LegacyRuleMetadata{}, fmt.Errorf("%w: legacy rule configuration is not applied and verified", faults.ErrValidation)
	}
	switch status.Engine {
	case agentv1.EngineXray:
		if input.Rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || !hasLegacyVLESSInboundTag(input, ruleID) {
			return LegacyRuleMetadata{}, fmt.Errorf("%w: legacy Xray tag is absent from the applied rule fragment", faults.ErrValidation)
		}
	case agentv1.EngineGOST:
		if input.Rule.EffectiveIngressProtocol() != forwarding.IngressTCP || !hasLegacyGOSTService(input, ruleID) {
			return LegacyRuleMetadata{}, fmt.Errorf("%w: legacy GOST service is absent from the applied rule fragment", faults.ErrValidation)
		}
	default:
		return LegacyRuleMetadata{}, fmt.Errorf("%w: legacy rule engine is unsupported", faults.ErrValidation)
	}
	return LegacyRuleMetadata{
		RuleID: ruleID, CustomerID: input.Rule.CustomerID,
		EntryGroup: input.Rule.EntryGroupID, ExitGroup: input.Rule.ExitGroupID,
		Protocol: string(input.Rule.Protocol),
	}, nil
}

// hasLegacyGOSTService proves that the current GOST fragment contains the
// service whose name is used by the agent's Prometheus counter collector.
// Legacy GOST reports intentionally carry only this rule ID; ownership is
// still resolved from the authenticated node's verified deployment.
func hasLegacyGOSTService(input deploymentreceipts.Input, ruleID string) bool {
	if input.CurrentConfig == nil {
		return false
	}
	wantFragmentID := generations.ForwardingFragmentID(agentv1.EngineGOST, ruleID)
	wantService := "forward-" + ruleID
	var bundle agentv1.ConfigurationBundle
	if json.Unmarshal(input.CurrentConfig.Config, &bundle) != nil || bundle.SchemaVersion != 1 {
		return false
	}
	var matching int
	for _, fragment := range bundle.Fragments {
		if fragment.GroupID != wantFragmentID {
			continue
		}
		matching++
		if fragment.Engine != agentv1.EngineGOST || !hasGOSTService(fragment.Config, wantService) {
			return false
		}
	}
	return matching == 1
}

func hasGOSTService(raw json.RawMessage, expected string) bool {
	var config struct {
		Services []struct {
			Name    string `json:"name"`
			Handler struct {
				Type string `json:"type"`
			} `json:"handler"`
			Listener struct {
				Type string `json:"type"`
			} `json:"listener"`
			Metadata map[string]any `json:"metadata"`
		} `json:"services"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return false
	}
	for _, service := range config.Services {
		if service.Name == expected && service.Handler.Type == "tcp" && service.Listener.Type == "tcp" {
			enableStats, ok := service.Metadata["enableStats"].(bool)
			if !ok || !enableStats {
				return false
			}
			return true
		}
	}
	return false
}

func hasLegacyVLESSInboundTag(input deploymentreceipts.Input, ruleID string) bool {
	if input.CurrentConfig == nil {
		return false
	}
	wantFragmentID := generations.ForwardingFragmentID(agentv1.EngineXray, ruleID)
	wantTag := "vless-reality-fwd_" + ruleID
	var bundle agentv1.ConfigurationBundle
	if json.Unmarshal(input.CurrentConfig.Config, &bundle) != nil || bundle.SchemaVersion != 1 {
		return false
	}
	var matching int
	for _, fragment := range bundle.Fragments {
		if fragment.GroupID != wantFragmentID {
			continue
		}
		matching++
		if fragment.Engine != agentv1.EngineXray || !hasXrayInboundTag(fragment.Config, wantTag) {
			return false
		}
	}
	return matching == 1
}

func hasXrayInboundTag(raw json.RawMessage, expected string) bool {
	var config struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return false
	}
	for _, inbound := range config.Inbounds {
		if inbound.Tag == expected {
			return true
		}
	}
	return false
}
