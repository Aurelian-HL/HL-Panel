package postgressnapshot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func snapshotReadFixture(t testing.TB, sessions int) (*memoryrepo.Store, []byte) {
	t.Helper()
	hash, err := auth.HashPassword("isolated-cache-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := memoryrepo.New(auth.Administrator{ID: "cache-admin", Username: "admin", PasswordHash: hash, CreatedAt: now})
	for i := 0; i < sessions; i++ {
		err := state.CreateSession(context.Background(), auth.Session{ID: fmt.Sprintf("cache-session-%d", i), AdminID: "cache-admin", TokenHash: fmt.Sprintf("%064x", i), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{})
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := state.EncodeSnapshotWithAudit(nil)
	if err != nil {
		t.Fatal(err)
	}
	return state, raw
}

func TestReadSnapshotDecodesIndependentConfidentialState(t *testing.T) {
	_, raw := snapshotReadFixture(t, 1)
	first, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := first.AdministratorByID(context.Background(), "cache-admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = first.UpdateAdministratorPassword(context.Background(), admin.ID, admin.PasswordHash, admin.PasswordHash, true, audit.Event{ID: "private-change"}); err != nil {
		t.Fatal(err)
	}
	second, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.SessionByTokenHash(context.Background(), fmt.Sprintf("%064x", 0), time.Now().UTC()); err != nil {
		t.Fatal("read mutation invalidated cached session", err)
	}
	unchanged, err := second.AdministratorByID(context.Background(), "cache-admin")
	if err != nil || unchanged.MustChangePassword || len(second.AuditEvents()) != 0 {
		t.Fatal("read mutation contaminated immutable cache")
	}
}

func BenchmarkReadSnapshot(b *testing.B) {
	state, raw := snapshotReadFixture(b, 1000)
	b.Run("encode_and_decode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			data, err := state.EncodeSnapshotWithAudit(nil)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = decodeReadSnapshot(data, memoryrepo.SnapshotVersion, 3); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode_cached_payload", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := decodeReadSnapshot(raw, memoryrepo.SnapshotVersion, 3); err != nil {
				b.Fatal(err)
			}
		}
	})
}
