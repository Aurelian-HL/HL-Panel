package usage

import (
	"context"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type limitCustomerReader struct{ customer customers.Customer }

func (reader limitCustomerReader) Customer(context.Context, string) (customers.Customer, error) {
	return reader.customer, nil
}

type limitRuleReader struct{ rule forwarding.Rule }

func (reader limitRuleReader) ForwardingRule(context.Context, string) (forwarding.Rule, error) {
	return reader.rule, nil
}

func TestRuntimeLimitPolicyUsesStricterCustomerAndRuleValues(t *testing.T) {
	adapter := NewRuntimeLimitPolicyAdapter(
		limitCustomerReader{customer: customers.Customer{ID: "customer-one", SpeedLimitMbps: 100, IPLimit: 0, ConnectionLimit: 20}},
		limitRuleReader{rule: forwarding.Rule{ID: "rule-one", CustomerID: "customer-one", Protocol: forwarding.ProtocolTCP, SpeedLimitMbps: 50, IPLimit: 3, ConnectionLimit: 30}},
	)
	limits, protocol, err := adapter.EffectiveLimits(context.Background(), "customer-one", "rule-one")
	if err != nil {
		t.Fatal(err)
	}
	if protocol != "tcp" || limits.SpeedLimitMbps != 50 || limits.IPLimit != 3 || limits.ConnectionLimit != 20 {
		t.Fatalf("effective limits = %#v protocol=%q", limits, protocol)
	}
}

func TestRuntimeLimitPolicyNeverAppliesTCPOnlyLimitsToUDP(t *testing.T) {
	adapter := NewRuntimeLimitPolicyAdapter(
		limitCustomerReader{customer: customers.Customer{ID: "customer-one", SpeedLimitMbps: 100, IPLimit: 2, ConnectionLimit: 20}},
		limitRuleReader{rule: forwarding.Rule{ID: "rule-one", CustomerID: "customer-one", Protocol: forwarding.ProtocolUDP}},
	)
	limits, protocol, err := adapter.EffectiveLimits(context.Background(), "customer-one", "rule-one")
	if err != nil {
		t.Fatal(err)
	}
	if protocol != "udp" || limits.SpeedLimitMbps != 0 || limits.IPLimit != 0 || limits.ConnectionLimit != 0 {
		t.Fatalf("UDP received TCP-only limits: %#v", limits)
	}
}
