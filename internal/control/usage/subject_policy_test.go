package usage

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type subjectReader struct {
	adminErr error
}

func (reader subjectReader) Customer(context.Context, string) (customers.Customer, error) {
	return customers.Customer{}, faults.ErrNotFound
}

func (reader subjectReader) AdministratorByID(_ context.Context, id string) (auth.Administrator, error) {
	if reader.adminErr != nil {
		return auth.Administrator{}, reader.adminErr
	}
	if id != "admin-one" {
		return auth.Administrator{}, faults.ErrNotFound
	}
	return auth.Administrator{ID: id}, nil
}

func TestAdministratorUsageSubjectResolvesWithoutCustomerRecord(t *testing.T) {
	adapter := NewCustomerPolicyAdapter(subjectReader{})
	policy, err := adapter.UsagePolicy(context.Background(), forwarding.AdministratorSubjectID("admin-one"))
	if err != nil || policy.CustomerID != "adminline_admin-one" || policy.Disabled || policy.TrafficLimitBytes != 0 {
		t.Fatalf("administrator policy = %+v, %v", policy, err)
	}
	if _, err := adapter.UsagePolicy(context.Background(), "adminline_missing"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("unknown administrator subject accepted: %v", err)
	}
	backendErr := errors.New("administrator lookup unavailable")
	if _, err := NewCustomerPolicyAdapter(subjectReader{adminErr: backendErr}).UsagePolicy(context.Background(), "adminline_admin-one"); !errors.Is(err, backendErr) {
		t.Fatalf("lookup failure was hidden: %v", err)
	}
}

func TestAdministratorRuntimeLimitsRequireMatchingRuleAndIdentity(t *testing.T) {
	rule := forwarding.Rule{ID: "rule-one", CustomerID: "adminline_admin-one", OwnerKind: forwarding.OwnerAdministrator, OwnerID: "admin-one", Protocol: forwarding.ProtocolTCP, SpeedLimitMbps: 30, IPLimit: 2, ConnectionLimit: 5}
	adapter := NewRuntimeLimitPolicyAdapter(subjectReader{}, limitRuleReader{rule: rule})
	limits, protocol, err := adapter.EffectiveLimits(context.Background(), rule.CustomerID, rule.ID)
	if err != nil || protocol != "tcp" || limits.SpeedLimitMbps != 30 || limits.IPLimit != 2 || limits.ConnectionLimit != 5 {
		t.Fatalf("administrator limits = %+v %s, %v", limits, protocol, err)
	}
	if _, _, err := adapter.EffectiveLimits(context.Background(), "adminline_missing", rule.ID); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("cross-subject rule accepted: %v", err)
	}
	rule.OwnerID = "missing"
	if _, _, err := NewRuntimeLimitPolicyAdapter(subjectReader{}, limitRuleReader{rule: rule}).EffectiveLimits(context.Background(), rule.CustomerID, rule.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("invalid rule ownership accepted: %v", err)
	}
}
