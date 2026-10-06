package usage

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type RuleLimitReader interface {
	ForwardingRule(context.Context, string) (forwarding.Rule, error)
}

type RuntimeLimitPolicyAdapter struct {
	customers CustomerReader
	rules     RuleLimitReader
}

func NewRuntimeLimitPolicyAdapter(customers CustomerReader, rules RuleLimitReader) *RuntimeLimitPolicyAdapter {
	return &RuntimeLimitPolicyAdapter{customers: customers, rules: rules}
}

func (adapter *RuntimeLimitPolicyAdapter) EffectiveLimits(ctx context.Context, customerID, ruleID string) (agentv1.EffectiveLimits, string, error) {
	if adapter == nil || adapter.customers == nil || adapter.rules == nil {
		return agentv1.EffectiveLimits{}, "", errorsUnavailable()
	}
	rule, err := adapter.rules.ForwardingRule(ctx, ruleID)
	if err != nil {
		return agentv1.EffectiveLimits{}, "", err
	}
	if rule.CustomerID != customerID {
		return agentv1.EffectiveLimits{}, "", fmt.Errorf("%w: rule does not belong to enforcement customer", faults.ErrConflict)
	}
	if rule.OwnerKind == forwarding.OwnerAdministrator {
		if !rule.OwnedByAdministrator(rule.OwnerID) {
			return agentv1.EffectiveLimits{}, "", faults.ErrNotFound
		}
		if err := administratorSubject(ctx, adapter.customers, customerID); err != nil {
			return agentv1.EffectiveLimits{}, "", err
		}
		return ruleLimits(rule, 0, 0, 0)
	}
	customer, err := adapter.customers.Customer(ctx, customerID)
	if err != nil {
		return agentv1.EffectiveLimits{}, "", err
	}
	return ruleLimits(rule, customer.SpeedLimitMbps, customer.IPLimit, customer.ConnectionLimit)
}

func ruleLimits(rule forwarding.Rule, speedLimitMbps, ipLimit, connectionLimit int) (agentv1.EffectiveLimits, string, error) {
	protocol := string(rule.Protocol)
	if rule.Protocol == forwarding.ProtocolUDP {
		return agentv1.EffectiveLimits{}, protocol, nil
	}
	if rule.Protocol != forwarding.ProtocolTCP {
		return agentv1.EffectiveLimits{}, "", fmt.Errorf("%w: unsupported forwarding protocol", faults.ErrValidation)
	}
	return agentv1.EffectiveLimits{
		SpeedLimitMbps:  strictPositiveLimit(speedLimitMbps, rule.SpeedLimitMbps),
		IPLimit:         strictPositiveLimit(ipLimit, rule.IPLimit),
		ConnectionLimit: strictPositiveLimit(connectionLimit, rule.ConnectionLimit),
	}, protocol, nil
}

// Zero means unlimited. If both layers set a limit, the smaller value is the
// effective, stricter limit.
func strictPositiveLimit(customer, rule int) int {
	if customer <= 0 {
		return max(rule, 0)
	}
	if rule <= 0 {
		return customer
	}
	return min(customer, rule)
}
