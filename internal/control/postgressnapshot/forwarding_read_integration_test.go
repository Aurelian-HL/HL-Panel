package postgressnapshot

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestForwardingCacheTracksOverlaysRepairsAndDatabaseFailure(t *testing.T) {
	writer, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err = writer.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	assertHealth(t, reader, proof)
	cached := reader.readCache.forwarding
	if cached == nil {
		t.Fatal("missing forwarding projection")
	}
	if _, err = reader.ListForwardingRules(ctx); err != nil || reader.readCache.forwarding != cached {
		t.Fatal("unchanged read decoded snapshot", err)
	}
	proof.VerifiedAt = proof.VerifiedAt.Add(9 * time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(9 * time.Second)
	if err = writer.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	assertHealth(t, reader, proof)
	replacement, err := memoryrepo.New(bootstrapAdministrator(t)).EncodeSnapshotWithAudit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, replacement); err != nil {
		t.Fatal(err)
	}
	rules, err := reader.ListForwardingRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Fatal("same-revision replacement hidden", err)
	}
	if _, err = writer.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, []byte(`{"Version":17}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.GatewayMembershipStates(ctx, []string{"pool"}); err == nil {
		t.Fatal("corrupted snapshot served cached routing")
	}
	reader.Close()
	if _, err = reader.ProtocolProbeStates(ctx); err == nil {
		t.Fatal("unavailable database served cached routing")
	}
}

func BenchmarkForwardingReadProjection(b *testing.B) {
	state, raw := snapshotReadFixture(b, 1000)
	view := state.ForwardingReadView()
	ctx := context.Background()
	b.Run("decode_snapshot", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = s.ListForwardingRules(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("private_overlay_view", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v := view.WithOverlays(3, nil, []gatewaymembership.ProtocolObservation{}, nil)
			if _, err := v.ListForwardingRules(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
}
