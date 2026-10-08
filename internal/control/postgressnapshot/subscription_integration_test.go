package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"testing"
)

func TestPostgreSQLSubscriptionsRestartReplayAndRollback(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabaseURL(t)
	admin := bootstrapAdministrator(t)
	store, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	customer, _, err := customers.NewService(store, nil).CreateCustomer(ctx, admin.ID, customers.CustomerInput{Username: "subscription-customer", Password: "isolated", IdempotencyKey: "subscription-customer"})
	if err != nil {
		t.Fatal(err)
	}
	service := subscriptions.NewService(store, nil, nil)
	req := subscriptions.Request{Name: "multi-line", CustomerID: customer.ID, Lines: []subscriptions.Line{{Name: "line", URI: "socks5://user:secret@127.0.0.1:1080"}}}
	item, _, err := service.Mutate(ctx, admin.ID, "", "create", req, "subscription-create")
	if err != nil {
		t.Fatal(err)
	}
	item, _, err = service.Mutate(ctx, admin.ID, item.ID, "publish", subscriptions.Request{Revision: item.Revision}, "subscription-publish")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := service.Detail(ctx, admin.ID, item.ID)
	auditsBefore, _ := store.AuditEvents(ctx)
	if _, _, err = service.Mutate(ctx, admin.ID, item.ID, "rotate", subscriptions.Request{Revision: 1}, "subscription-stale"); err == nil {
		t.Fatal("stale mutation accepted")
	}
	auditsAfter, _ := store.AuditEvents(ctx)
	if len(auditsBefore) != len(auditsAfter) {
		t.Fatal("failed transaction wrote audit")
	}
	store.Close()
	restored, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	service = subscriptions.NewService(restored, nil, nil)
	after, err := service.Detail(ctx, admin.ID, item.ID)
	if err != nil || after.Token != before.Token || after.Item != before.Item {
		t.Fatal("subscription lost after restart")
	}
	if _, lines, err := service.Public(ctx, after.Token); err != nil || len(lines) != 1 {
		t.Fatal("feed failed after restart")
	}
	replay, replayed, err := service.Mutate(ctx, admin.ID, "", "create", req, "subscription-create")
	if err != nil || !replayed || replay.ID != item.ID {
		t.Fatal("idempotency lost after restart")
	}
}
