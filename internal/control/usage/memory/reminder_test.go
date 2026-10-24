package memory

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestHistoricalExpiryDoesNotBlockQuotaOrExplicitRevoke(t *testing.T) {
	repository := New()
	now := time.Now().UTC()
	expired := usage.EnforcementDecision{ID: "old-expiry", Reason: usage.EnforcementCustomerExpired,
		Action: agentv1.EnforcementDisableCustomer, Status: usage.EnforcementPending,
		TriggerNodeID: "node-one", CreatedAt: now.Add(-time.Hour), Revision: 1}
	repository.decisions[expired.ID] = expired
	ctx := context.Background()
	if got, err := repository.DesiredEnforcement(ctx, "node-one"); err != nil || got != nil {
		t.Fatalf("historical expiry returned: %#v, %v", got, err)
	}
	quota := expired
	quota.ID, quota.Reason, quota.CreatedAt = "new-quota", usage.EnforcementQuotaExhausted, now
	repository.decisions[quota.ID] = quota
	if got, err := repository.DesiredEnforcement(ctx, "node-one"); err != nil || got == nil || got.ID != quota.ID {
		t.Fatalf("expiry blocked quota: %#v, %v", got, err)
	}
	if _, _, err := repository.RequestRevoke(ctx, usage.RevokeRequest{DecisionID: expired.ID, RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, err := repository.DesiredEnforcement(ctx, "node-one"); err != nil || got == nil || got.ID != expired.ID || got.Action != agentv1.EnforcementEnableCustomer {
		t.Fatalf("explicit revoke lost: %#v, %v", got, err)
	}
}
