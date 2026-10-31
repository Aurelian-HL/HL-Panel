package postgressnapshot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestCustomerListReadsCommittedStateDuringPendingWrite(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, _, err := customers.NewService(store, nil).CreateCustomer(ctx, "admin-one", customers.CustomerInput{AccountKind: customers.AccountService, Username: "pending-write", IdempotencyKey: "pending-write-create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCustomers(ctx); err != nil {
		t.Fatal(err)
	}
	// Model a queued business writer and a transaction holding the snapshot
	// row. Customer reads must keep serving committed data without either lock.
	release, err := store.writeGate.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	writer, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	var revision int64
	if err := writer.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	items, err := store.ListCustomers(readCtx)
	if err != nil || len(items) != 1 || items[0].ID != c.ID {
		t.Fatal("customer list waited for a business write or lost committed data", err)
	}
}

func TestUsageCacheRefreshesAcrossWritersRepairsAndDatabaseFailure(t *testing.T) {
	dsn, ctx := isolatedDatabaseURL(t), context.Background()
	admin := bootstrapAdministrator(t)
	reader, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	service := customers.NewService(writer, nil)
	c, _, err := service.CreateCustomer(ctx, admin.ID, customers.CustomerInput{AccountKind: customers.AccountPanel, Username: "usage-cache", Password: "isolated-password", IdempotencyKey: "usage-create"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := reader.Customer(ctx, c.ID)
	if err != nil || first.Disabled {
		t.Fatal(err)
	}
	cached := reader.readCache.usage
	if len(first.PasswordHash) == 0 {
		t.Fatal("panel account credential missing from private projection")
	}
	first.PasswordHash[0] ^= 1
	again, err := reader.Customer(ctx, c.ID)
	if err != nil || again.PasswordHash[0] == first.PasswordHash[0] || reader.readCache.usage != cached {
		t.Fatal("projection contaminated or unnecessarily decoded", err)
	}
	list, err := reader.ListCustomers(ctx)
	if err != nil || len(list) != 1 || list[0].ID != c.ID || list[0].PasswordHash[0] != again.PasswordHash[0] || reader.readCache.usage != cached {
		t.Fatal("customer list did not reuse the validated private projection", err)
	}
	list[0].PasswordHash[0] ^= 1
	input := customers.SaveCustomerInput{Customer: again, ExpectedRevision: again.Revision, IdempotencyKey: "usage-disable", RequestSHA256: "usage-disable-digest", CreatedBy: admin.ID}
	input.Customer.Disabled = true
	input.Customer.Revision++
	if _, _, err = writer.SaveCustomer(ctx, input, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	again, err = reader.Customer(ctx, c.ID)
	if err != nil || !again.Disabled || reader.readCache.usage == cached {
		t.Fatal("other instance update hidden by usage cache", err)
	}
	list, err = reader.ListCustomers(ctx)
	if err != nil || len(list) != 1 || !list[0].Disabled || list[0].Revision != again.Revision || list[0].PasswordHash[0] != again.PasswordHash[0] {
		t.Fatal("customer list hid committed changes or retained caller mutations", err)
	}
	replacement, err := memoryrepo.New(admin).EncodeSnapshotWithAudit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Customer(ctx, c.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("same-revision replacement hidden by usage cache", err)
	}
	list, err = reader.ListCustomers(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatal("customer list hid a same-revision replacement", err)
	}
	if _, err = writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, []byte(`{"Version":17}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ListRuleTrafficStates(ctx); err == nil {
		t.Fatal("corrupt database served cached accounting")
	}
	if _, err = reader.ListCustomers(ctx); err == nil {
		t.Fatal("corrupt database served a cached customer list")
	}
	reader.Close()
	if _, err = reader.Customer(ctx, c.ID); err == nil {
		t.Fatal("unavailable database served cached accounting")
	}
	if _, err = reader.ListCustomers(ctx); err == nil {
		t.Fatal("unavailable database served a cached customer list")
	}
}

func BenchmarkCustomerListReadProjection(b *testing.B) {
	state, _ := snapshotReadFixture(b, 1000)
	ctx := context.Background()
	service := customers.NewService(state, nil)
	for i := 0; i < 600; i++ {
		name := fmt.Sprintf("customer-%04d", i)
		if _, _, err := service.CreateCustomer(ctx, "cache-admin", customers.CustomerInput{AccountKind: customers.AccountService, Username: name, IdempotencyKey: name}); err != nil {
			b.Fatal(err)
		}
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		b.Fatal(err)
	}
	view := state.UsageReadView()
	b.Logf("synthetic snapshot: %d bytes, 600 customers, 1000 sessions", len(raw))
	b.Run("decode_snapshot_then_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			private, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
			if err != nil {
				b.Fatal(err)
			}
			items, err := private.ListCustomers(ctx)
			if err != nil || len(items) != 600 {
				b.Fatal("customer inventory mismatch", err)
			}
		}
	})
	b.Run("detached_customer_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			items, err := view.ListCustomers(ctx)
			if err != nil || len(items) != 600 {
				b.Fatal("customer inventory mismatch", err)
			}
		}
	})
}

func BenchmarkUsageReadProjection(b *testing.B) {
	state, _ := snapshotReadFixture(b, 1000)
	c, _, err := customers.NewService(state, nil).CreateCustomer(context.Background(), "cache-admin", customers.CustomerInput{Username: "benchmark-customer", Password: "isolated-benchmark-password", IdempotencyKey: "benchmark-create"})
	if err != nil {
		b.Fatal(err)
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		b.Fatal(err)
	}
	view, ctx := state.UsageReadView(), context.Background()
	b.Run("decode_snapshot_then_customer_lookup", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = s.Customer(ctx, c.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("detached_customer_lookup", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := view.Customer(ctx, c.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("publish_detached_usage_view", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if state.UsageReadView() == nil {
				b.Fatal("missing projection")
			}
		}
	})
}
