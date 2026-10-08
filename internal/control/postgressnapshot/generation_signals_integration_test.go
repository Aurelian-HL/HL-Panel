package postgressnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestDesiredSignalRequiresCommittedPostgreSQLGeneration(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	s, err := Open(ctx, isolatedDatabaseURL(t), bootstrapAdministrator(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	event := audit.Event{ID: "signal-event", ActorType: "system", Action: "test", Outcome: "succeeded", CreatedAt: now}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "signal-token", Name: "isolated", TokenHash: "signal-token-hash", ExpiresAt: now.Add(time.Hour)}, event))
	_, err = s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "signal-token-hash", CredentialHash: "signal-node-hash", Node: nodes.Node{ID: "signal-node", Hostname: "isolated", CreatedAt: now, UpdatedAt: now}}, now, event)
	require(err)
	require(s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "signal-group", Name: "isolated", Kind: groups.KindEdge, CreatedAt: now}, event))
	_, _, err = s.UpsertGroupMember(ctx, groups.Member{GroupID: "signal-group", NodeID: "signal-node", Weight: 1, CreatedAt: now}, event)
	require(err)
	input := generations.CreateGroupRevisionInput{ID: "signal-revision", GroupID: "signal-group", Engine: agentv1.EngineGOST, Config: []byte(`{"services":[]}`), IdempotencyKey: "signal-key", RequestSHA256: "request", CreatedBy: "admin-one", CreatedAt: now}
	wake := s.DesiredConfigChanges("signal-node")
	rollback := errors.New("deliberate isolated transaction failure")
	err = mutate(ctx, s, func(state *memoryrepo.Store) error {
		if _, err := state.CreateGroupRevision(ctx, input, event); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal("fault injection did not roll back")
	}
	select {
	case <-wake:
		t.Fatal("rolled-back generation woke node")
	default:
	}
	result, err := s.CreateGroupRevision(ctx, input, event)
	require(err)
	if len(result.Assignments) != 1 {
		t.Fatal("missing committed assignment")
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed generation did not wake node")
	}
	desired, _, err := s.DesiredNodeConfig(ctx, "signal-node")
	require(err)
	if desired.Generation != result.Assignments[0].Generation {
		t.Fatal("signal preceded durable configuration")
	}
	wake = s.DesiredConfigChanges("signal-node")
	_, err = s.CreateGroupRevision(ctx, input, event)
	require(err)
	select {
	case <-wake:
		t.Fatal("idempotency replay woke unchanged node")
	default:
	}
}
