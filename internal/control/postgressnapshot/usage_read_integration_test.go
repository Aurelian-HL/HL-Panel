package postgressnapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

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
	if _, err = writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, []byte(`{"Version":17}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ListRuleTrafficStates(ctx); err == nil {
		t.Fatal("corrupt database served cached accounting")
	}
	reader.Close()
	if _, err = reader.Customer(ctx, c.ID); err == nil {
		t.Fatal("unavailable database served cached accounting")
	}
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
