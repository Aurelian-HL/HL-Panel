package postgressnapshot

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/groups"
)

func TestPostgreSQLReenrollmentCommitFailureIsRetryableAndDurable(t *testing.T) {
	s, dsn, _ := protocolHealthFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	service := enrollment.NewService(s, func() time.Time { return now }, time.Hour)
	input := enrollment.EnrollInput{Hostname: "isolated.example.test", DialHost: "127.0.0.2",
		Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"xray", "vless-reality"}}
	peerToken, err := service.IssueForGroup(ctx, "admin-one", "peer", "group", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input.RawToken = peerToken.Token
	peer, err := service.Enroll(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	event := audit.Event{ID: "reenrollment-setup", Action: "test", CreatedAt: now}
	if err := s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "replacement", Name: "replacement", Kind: groups.KindEntry, CreatedAt: now}, event); err != nil {
		t.Fatal(err)
	}
	token, err := service.IssueForGroup(ctx, "admin-one", "moving", "replacement", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input.RawToken, input.DialHost = token.Token, "127.0.0.3"
	node, err := s.NodeByCredentialHash(ctx, "credential-hash")
	if err != nil {
		t.Fatal(err)
	}
	beforeApplied, err := s.UsageConfigInput(ctx, node.ID, node.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig := beforeApplied.Config
	peerConfig, _, err := s.DesiredNodeConfig(ctx, peer.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	beforeMembers, err := s.ListGroupMembers(ctx, "group")
	if err != nil {
		t.Fatal(err)
	}
	beforeAudits, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, revision := healthSnapshot(t, s)
	cached := s.readCache
	wake := s.DesiredConfigChanges(node.ID)
	peerWake := s.DesiredConfigChanges(peer.NodeID)
	// Reject the real COMMIT after the snapshot UPDATE has already succeeded.
	// This trigger exists only in this test's isolated PostgreSQL schema.
	if _, err := s.db.ExecContext(ctx, `CREATE FUNCTION reject_reenrollment_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_reenrollment_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_reenrollment_commit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reenroll(ctx, node, input); err == nil {
		t.Fatal("reenrollment ignored a deferred commit failure")
	}
	if s.readCache.revision != cached.revision || s.readCache.identities != cached.identities || !bytes.Equal(s.readCache.payload, cached.payload) {
		t.Fatal("failed reenrollment published uncommitted cache")
	}
	after, next := healthSnapshot(t, s)
	if revision != next || !bytes.Equal(before, after) {
		t.Fatal("failed reenrollment changed the committed snapshot")
	}
	retained, err := s.UsageConfigInput(ctx, node.ID, node.DesiredGeneration)
	if err != nil || !bytes.Equal(retained.Config.Config, beforeConfig.Config) {
		t.Fatal("failed reenrollment made the previous configuration unavailable", err)
	}
	members, err := s.ListGroupMembers(ctx, "group")
	if err != nil || !reflect.DeepEqual(beforeMembers, members) {
		t.Fatal("failed reenrollment changed the old membership", err)
	}
	if members, err := s.ListGroupMembers(ctx, "replacement"); err != nil || len(members) != 0 {
		t.Fatal("failed reenrollment left a target membership", err)
	}
	audits, err := s.AuditEvents(ctx)
	if err != nil || !reflect.DeepEqual(beforeAudits, audits) {
		t.Fatal("failed reenrollment persisted an audit", err)
	}
	select {
	case <-wake:
		t.Fatal("failed reenrollment woke the moving node")
	default:
	}
	select {
	case <-peerWake:
		t.Fatal("failed reenrollment woke an unaffected peer")
	default:
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_reenrollment_commit ON nyvp_control_snapshots; DROP FUNCTION reject_reenrollment_commit()`); err != nil {
		t.Fatal(err)
	}
	moved, err := service.Reenroll(ctx, node, input)
	if err != nil {
		t.Fatal("rolled-back token could not be retried", err)
	}
	if moved.ID != node.ID || moved.CredentialHash != node.CredentialHash || !reflect.DeepEqual(moved.Capabilities, node.Capabilities) {
		t.Fatal("reenrollment changed the node identity or cleared its capabilities")
	}
	committed, _, err := s.DesiredNodeConfig(ctx, node.ID)
	if err != nil || committed.Generation <= beforeConfig.Generation || bytes.Equal(beforeConfig.Config, committed.Config) {
		t.Fatal("successful reenrollment did not withdraw the old rule bundle", err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed reenrollment did not wake its node")
	}
	unchanged, _, err := s.DesiredNodeConfig(ctx, peer.NodeID)
	if err != nil || unchanged.Generation != peerConfig.Generation || !bytes.Equal(unchanged.Config, peerConfig.Config) {
		t.Fatal("reenrollment changed an unaffected peer bundle", err)
	}
	select {
	case <-peerWake:
		t.Fatal("reenrollment woke an unchanged peer")
	default:
	}
	committedAudits, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatal("committed reenrollment did not survive reopening", err)
	}
	defer restored.Close()
	members, err = restored.ListGroupMembers(ctx, "replacement")
	if err != nil || len(members) != 1 || members[0].NodeID != node.ID || members[0].RetiredAt != nil {
		t.Fatal("target membership did not survive reopening", err)
	}
	members, err = restored.ListGroupMembers(ctx, "group")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.NodeID == node.ID && member.RetiredAt == nil {
			t.Fatal("old membership became active after reopening")
		}
	}
	authenticated, err := restored.NodeByCredentialHash(ctx, "credential-hash")
	if err != nil || authenticated.ID != node.ID {
		t.Fatal("preserved credential stopped authenticating after reopening", err)
	}
	wake = restored.DesiredConfigChanges(node.ID)
	service = enrollment.NewService(restored, func() time.Time { return now }, time.Hour)
	if _, err := service.Reenroll(ctx, authenticated, input); err != nil {
		t.Fatal("committed reenrollment could not be replayed after reopening", err)
	}
	replayed, _, err := restored.DesiredNodeConfig(ctx, node.ID)
	if err != nil || replayed.Generation != committed.Generation || !bytes.Equal(replayed.Config, committed.Config) {
		t.Fatal("reenrollment replay changed the committed configuration", err)
	}
	audits, err = restored.AuditEvents(ctx)
	if err != nil || !reflect.DeepEqual(committedAudits, audits) {
		t.Fatal("reenrollment replay duplicated an audit", err)
	}
	select {
	case <-wake:
		t.Fatal("reenrollment replay woke an unchanged node")
	default:
	}
}
