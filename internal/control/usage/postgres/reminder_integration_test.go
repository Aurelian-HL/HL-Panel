package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestPostgreSQLHistoricalExpiryDoesNotBlockQuotaOrExplicitRevoke(t *testing.T) {
	db := isolatedUsageDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repository := New(db)
	service := usage.NewService(repository, integrationPolicies{limit: 50}, func() time.Time { return now })
	first, err := service.Ingest(ctx, integrationReport(1))
	if err != nil || first.Decision == nil {
		t.Fatalf("ingest: %#v, %v", first.Decision, err)
	}
	// Reproduce an old persisted command using its real serialized action.
	if _, err := db.ExecContext(ctx, `UPDATE usage_enforcement_decisions SET reason=$2 WHERE id=$1`, first.Decision.ID, usage.EnforcementCustomerExpired); err != nil {
		t.Fatal(err)
	}
	if got, err := service.DesiredEnforcement(ctx, "node-one"); err != nil || got != nil {
		t.Fatalf("historical expiry returned: %#v, %v", got, err)
	}
	now = now.Add(time.Second)
	second, err := service.Ingest(ctx, integrationReport(2))
	if err != nil || second.Decision == nil {
		t.Fatalf("ingest: %#v, %v", second.Decision, err)
	}
	if got, err := service.DesiredEnforcement(ctx, "node-one"); err != nil || got == nil || got.DecisionID != second.Decision.ID {
		t.Fatalf("expiry blocked quota: %#v, %v", got, err)
	}
	if _, _, err := service.RequestRevoke(ctx, "admin-one", first.Decision.ID, "revoke-expiry"); err != nil {
		t.Fatal(err)
	}
	if got, err := service.DesiredEnforcement(ctx, "node-one"); err != nil || got == nil || got.DecisionID != first.Decision.ID || got.Action != agentv1.EnforcementEnableCustomer {
		t.Fatalf("explicit revoke lost: %#v, %v", got, err)
	}
}
