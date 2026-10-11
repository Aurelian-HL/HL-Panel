package postgressnapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestReadCacheTracksOtherWritersAndSameRevisionRepairs(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	first, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Now().UTC()
	tokenHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = first.CreateSession(ctx, auth.Session{ID: "cache-session", AdminID: admin.ID, TokenHash: tokenHash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, audit.Event{ID: "cache-login"}); err != nil {
		t.Fatal(err)
	}
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now); err != nil {
		t.Fatal(err)
	}
	cached := &first.readCache.payload[0]
	identities := first.readCache.identities
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now); err != nil || &first.readCache.payload[0] != cached || first.readCache.identities != identities {
		t.Fatal("unchanged reads did not reuse validated state")
	}
	projection, err := first.AdministratorByID(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection.PasswordHash[0] ^= 1
	projection, err = first.AdministratorByUsername(ctx, admin.Username)
	if err != nil || projection.PasswordHash[0] != admin.PasswordHash[0] {
		t.Fatal("caller contaminated cached password hash")
	}
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now.Add(time.Hour)); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("cached session survived its expiry")
	}
	if err = second.UpdateAdministratorPassword(ctx, admin.ID, admin.PasswordHash, admin.PasswordHash, false, audit.Event{ID: "password-reset"}); err != nil {
		t.Fatal(err)
	}
	if _, err = first.SessionByTokenHash(ctx, tokenHash, now); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("cached session survived password reset in another instance")
	}
	cached = &first.readCache.payload[0]
	err = mutate(ctx, second, func(state *memoryrepo.Store) error {
		_ = state.AppendAudit(ctx, audit.Event{ID: "rollback"})
		return faults.ErrConflict
	})
	if !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
	events, err := first.AuditEvents(ctx)
	if err != nil || len(events) != 2 || &first.readCache.payload[0] != cached {
		t.Fatal("rollback invalidated or contaminated read state")
	}
	replacement, err := memoryrepo.New(admin).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, replacement); err != nil {
		t.Fatal(err)
	}
	events, err = first.AuditEvents(ctx)
	if err != nil || len(events) != 2 || &first.readCache.payload[0] == cached {
		t.Fatal("same-revision replacement was hidden by cache")
	}
	if _, err = second.db.ExecContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1`, []byte(`{"Version":17}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = first.ListNodes(ctx); err == nil {
		t.Fatal("same-revision corrupted snapshot was hidden by cache")
	}
	if _, err = first.AdministratorByID(ctx, admin.ID); err == nil {
		t.Fatal("corrupted snapshot authorized a cached identity")
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = first.AdministratorByID(ctx, admin.ID); err == nil {
		t.Fatal("cached identity served while database unavailable")
	}
}

func TestIdentityCacheTracksNodeMutationsAndKeepsBusinessReadsPrivate(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	reader, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	now := time.Now().UTC()
	event := audit.Event{ID: "cache-node-event"}
	oldHash := securetoken.Hash("isolated-old-credential")
	newHash := securetoken.Hash("isolated-new-credential")
	if err = writer.CreateEnrollmentToken(ctx, enrollment.Token{ID: "cache-token", TokenHash: "cache-token-hash", Name: "cache-node", ExpiresAt: now.Add(time.Hour)}, event); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "cache-token-hash", CredentialHash: oldHash, Node: nodes.Node{ID: "cache-node", Hostname: "cache-node", CreatedAt: now, UpdatedAt: now}}, now, event); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.NodeByCredentialHash(ctx, oldHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err = writer.RotateCredential(ctx, nodes.RotateCredentialInput{NodeID: "cache-node", CredentialHash: newHash, UpdatedBy: admin.ID, IdempotencyKey: "rotate-cache", RequestSHA256: "rotate-digest", UpdatedAt: now}, event); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.NodeByCredentialHash(ctx, oldHash); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("cached old credential survived rotation")
	}
	if _, err = reader.NodeByCredentialHash(ctx, newHash); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.UpdateHeartbeat(ctx, "cache-node", nodes.Heartbeat{Hostname: "new-host", Resources: map[string]any{"cpu": map[string]any{"percent": float64(7)}}}, now, event); err != nil {
		t.Fatal(err)
	}
	node, err := reader.NodeByCredentialHash(ctx, newHash)
	if err != nil || node.Hostname != "new-host" || node.LastHeartbeatAt == nil || !node.LastHeartbeatAt.Equal(now) {
		t.Fatal("cached identity hid heartbeat update")
	}
	var group sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(business bool) {
			defer group.Done()
			for j := 0; j < 3; j++ {
				var value nodes.Node
				if business {
					items, err := reader.ListNodes(ctx)
					if err != nil {
						failures <- err
						return
					}
					if len(items) != 1 {
						failures <- fmt.Errorf("node count %d", len(items))
						return
					}
					value = items[0]
				} else {
					var err error
					value, err = reader.NodeByCredentialHash(ctx, newHash)
					if err != nil {
						failures <- err
						return
					}
				}
				if value.Resources["cpu"].(map[string]any)["percent"] != float64(7) {
					failures <- errors.New("concurrent caller contaminated telemetry")
					return
				}
				value.Resources["cpu"].(map[string]any)["percent"] = float64(999)
			}
		}(i%2 == 0)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if _, _, err = writer.DeleteOffline(ctx, nodes.DeleteInput{NodeID: "cache-node", DeletedBy: admin.ID, IdempotencyKey: "delete-cache", RequestSHA256: "delete-digest", DeletedAt: now.Add(2 * time.Minute), OnlineFor: time.Minute}, event); err != nil {
		t.Fatal(err)
	}
	service := nodes.NewService(reader, nil, time.Minute)
	if _, err = service.AuthenticateCredential(ctx, "isolated-new-credential"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("cached deleted node remained authenticated")
	}
}
