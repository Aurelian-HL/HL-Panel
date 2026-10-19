package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/usagemigration"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func protocolHealthFixture(t *testing.T) (*Store, string, gatewaymembership.ProtocolObservation) {
	ctx := context.Background()
	dsn := isolatedDatabaseURL(t)
	s, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("fixture failure: %v", err)
		}
	}
	runner, err := usagemigration.New(s.db)
	require(err)
	_, err = runner.Apply(ctx)
	require(err)
	now := time.Now().UTC().Add(-30 * time.Second)
	event := audit.Event{ID: "health-fixture", ActorType: "administrator", ActorID: "admin-one", Action: "test", Outcome: "succeeded", CreatedAt: now}
	require(s.CreateEnrollmentToken(ctx, enrollment.Token{ID: "token", TokenHash: "token-hash", ExpiresAt: now.Add(time.Hour)}, event))
	_, err = s.ConsumeEnrollmentToken(ctx, enrollment.ConsumeInput{TokenHash: "token-hash", CredentialHash: "credential-hash", Node: nodes.Node{ID: "node", Hostname: "127.0.0.1", Capabilities: []string{"xray", "vless-reality"}, CreatedAt: now, UpdatedAt: now}}, now, event)
	require(err)
	_, err = s.UpdateHeartbeat(ctx, "node", nodes.Heartbeat{Capabilities: []string{"xray", "vless-reality"}}, now, event)
	require(err)
	require(s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "group", Name: "loopback", Kind: groups.KindEntry, SelectionPolicy: endpoints.SelectionWeightedRoundRobin, CreatedAt: now}, event))
	_, _, err = s.UpdateGroupNetwork(ctx, groupconfig.UpdateInput{Network: groupconfig.GroupNetwork{GroupID: "group", ConnectHost: "127.0.0.1", PortStart: 12000, PortEnd: 12010, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: now}, IdempotencyKey: "network", RequestSHA256: "network-hash", CreatedBy: "admin-one"}, event)
	require(err)
	_, _, err = s.UpsertGroupMember(ctx, groups.Member{GroupID: "group", NodeID: "node", DialHost: "127.0.0.1", Weight: 1, CreatedAt: now, UpdatedAt: now}, event)
	require(err)
	service := forwarding.NewService(s, time.Now, forwarding.WithRealityDefaults(forwarding.RealityDefaults{ServerName: "example.com", Destination: "example.com:443"}))
	rule, _, err := service.CreateForAdministrator(ctx, "admin-one", forwarding.Request{Name: "health", EntryGroupID: "group", EgressMode: forwarding.EgressDirect, IngressProtocol: forwarding.IngressVLESSReality, VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "127.0.0.1", VLESSSOCKS5Port: 1080, VLESSSOCKS5Username: "isolated", VLESSSOCKS5Password: "isolated", Protocol: forwarding.ProtocolTCP, ListenPort: 12000, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 19001}}, SelectionPolicy: forwarding.SelectionRoundRobin}, "health-rule")
	require(err)
	pool, _, err := endpoints.NewService(s, time.Now).CreateBound(ctx, "admin-one", "health", "group", rule.ID, endpoints.ModeSingleServiceEndpoint, "vless", "127.0.0.1", 12000, endpoints.SelectionWeightedRoundRobin, "health-pool")
	require(err)
	identity, err := vlessidentity.NewService(s, time.Now, nil)
	require(err)
	_, _, err = identity.Provision(ctx, "admin-one", vlessidentity.ProvisionRequest{CustomerID: rule.CustomerID, ForwardingRuleID: rule.ID, EndpointPoolID: pool.ID}, "health-binding")
	require(err)
	config, _, err := s.DesiredNodeConfig(ctx, "node")
	require(err)
	for _, phase := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		_, err = s.RecordApplyResult(ctx, generations.ApplyResult{ID: string(phase), NodeID: "node", Generation: config.Generation, AttemptID: "0123456789abcdef0123456789abcdef", ConfigSHA256: config.ConfigSHA256, Phase: phase, Status: agentv1.ApplyStatusSucceeded, EngineMode: "xray", CreatedAt: now}, event)
		require(err)
	}
	pools, err := s.ListEndpointPools(ctx)
	require(err)
	for _, pool := range pools {
		if pool.RuleID == rule.ID {
			state, err := s.GatewayMembershipState(ctx, pool.ID)
			require(err)
			if len(state.Members) != 1 {
				t.Fatalf("members=%+v", state.Members)
			}
			verified := time.Now().UTC().Add(-10 * time.Second)
			return s, dsn, gatewaymembership.ProtocolObservation{RuleID: rule.ID, NodeID: "node", RuleRevision: rule.Revision, NodeConfigGeneration: config.Generation, ConfigSHA256: config.ConfigSHA256, Protocol: forwarding.IngressVLESSReality, DialHost: state.Members[0].DialHost, VerifiedAt: verified, LeaseExpiresAt: verified.Add(endpoints.DefaultHealthTTL)}
		}
	}
	t.Fatal("missing automatic pool")
	return nil, "", gatewaymembership.ProtocolObservation{}
}

func healthSnapshot(t *testing.T, s *Store) ([]byte, int64) {
	t.Helper()
	var raw []byte
	var revision int64
	if err := s.db.QueryRow(`SELECT payload,revision FROM nyvp_control_snapshots WHERE singleton=true`).Scan(&raw, &revision); err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	return raw, revision
}

func assertHealth(t *testing.T, s *Store, proof gatewaymembership.ProtocolObservation) {
	t.Helper()
	states, err := s.ProtocolProbeStates(context.Background())
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	for _, state := range states {
		if state.State.Rule.ID == proof.RuleID {
			actual := state.State.ProtocolHealth[proof.NodeID]
			if !actual.VerifiedAt.Equal(proof.VerifiedAt) || !actual.LeaseExpiresAt.Equal(proof.LeaseExpiresAt) {
				t.Fatalf("proof changed: %+v", actual)
			}
			return
		}
	}
	t.Fatal("missing proof state")
}

func TestProtocolHealthRenewalDoesNotRewriteSnapshot(t *testing.T) {
	s, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	before, _ := healthSnapshot(t, s)
	results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof})
	if err != nil || len(results) != 1 || results[0] != nil {
		t.Fatalf("activation: %v %v", results, err)
	}
	activated, revision := healthSnapshot(t, s)
	if bytes.Equal(before, activated) {
		t.Fatal("first activation was not folded")
	}
	audits, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	count := 0
	for _, event := range audits {
		if event.Action == "forwarding_rule.activate" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("activation audits=%d", count)
	}
	old := proof
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	results, err = s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof, old})
	if err != nil || results[0] != nil || results[1] == nil {
		t.Fatalf("ordered renewal: %v %v", results, err)
	}
	renewed, next := healthSnapshot(t, s)
	if !bytes.Equal(activated, renewed) || next != revision+1 {
		t.Fatal("renewal rewrote snapshot or lost revision")
	}
	assertHealth(t, s, proof)
	other, err := Open(ctx, dsn, bootstrapAdministrator(t))
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	defer other.Close()
	assertHealth(t, other, proof)
	invalid := proof
	invalid.ConfigSHA256 = string(bytes.Repeat([]byte("a"), 64))
	results, err = other.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{invalid, old})
	if err != nil || results[0] == nil || results[1] == nil {
		t.Fatal("invalid proofs accepted")
	}
	_, next = healthSnapshot(t, s)
	if next != revision+1 {
		t.Fatal("rejected proofs wrote state")
	}
	exported, err := s.ExportMigration(ctx)
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	decoded, err := memoryrepo.DecodeSnapshot(exported.Snapshot)
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	states, err := decoded.ProtocolProbeStates(ctx)
	if err != nil || !states[0].State.ProtocolHealth[proof.NodeID].VerifiedAt.Equal(proof.VerifiedAt) {
		t.Fatal("export lost renewal")
	}
	// A business write must fold the latest proof and clear the overlay atomically.
	if err := s.AppendAudit(ctx, audit.Event{ID: "fold-health", Action: "test"}); err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	var overlays int
	if err := s.db.QueryRow(`SELECT count(*) FROM hl_panel_protocol_health`).Scan(&overlays); err != nil || overlays != 0 {
		t.Fatal("business write did not fold overlay")
	}
	assertHealth(t, other, proof)
	// The persisted lease is restored verbatim; restoration never renews it.
	proof.VerifiedAt = time.Now().UTC().Add(-time.Hour)
	proof.LeaseExpiresAt = proof.VerifiedAt.Add(endpoints.DefaultHealthTTL)
	raw, _ := json.Marshal(proof)
	if _, err := s.db.Exec(`INSERT INTO hl_panel_protocol_health VALUES($1,$2,$3)`, proof.RuleID, proof.NodeID, raw); err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	// The older same-deployment overlay cannot roll back the folded proof.
	proof = old
	proof.VerifiedAt = old.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = old.LeaseExpiresAt.Add(time.Second)
	assertHealth(t, other, proof)
}

func TestProtocolHealthRenewalAfterLeaseLossDoesNotRewriteSnapshot(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	if results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil || results[0] != nil {
		t.Fatal(results, err)
	}
	// Simulate lost leases only in the isolated test database. The durable rule
	// remains activated, while its public projection must fail closed.
	raw, _ := healthSnapshot(t, s)
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["ProtocolHealth"] = json.RawMessage(`{}`)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE nyvp_control_snapshots SET payload=$1,revision=revision+1 WHERE singleton=true`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM hl_panel_protocol_health`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rule, err := s.ForwardingRule(ctx, proof.RuleID)
	if err != nil || rule.Deployed {
		t.Fatalf("missing lease did not fail closed: deployed=%v err=%v", rule.Deployed, err)
	}
	before, revision := healthSnapshot(t, s)
	beforeAudits, err := s.AuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	if results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil || results[0] != nil {
		t.Fatal(results, err)
	}
	after, next := healthSnapshot(t, s)
	if !bytes.Equal(before, after) || next != revision+1 {
		t.Fatal("lease recovery rewrote snapshot or lost revision")
	}
	afterAudits, err := s.AuditEvents(ctx)
	if err != nil || len(afterAudits) != len(beforeAudits) {
		t.Fatalf("lease recovery repeated activation audit: %v", err)
	}
	assertHealth(t, s, proof)
	rule, err = s.ForwardingRule(ctx, proof.RuleID)
	if err != nil || !rule.Deployed {
		t.Fatalf("fresh valid lease did not restore health: deployed=%v err=%v", rule.Deployed, err)
	}
}

func TestProtocolHealthConcurrentBusinessWriteAndRollback(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof})
	if err != nil || results[0] != nil {
		t.Fatal(results, err)
	}
	var wg sync.WaitGroup
	fail := make(chan error, 2)
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	wg.Add(2)
	go func() { defer wg.Done(); fail <- s.PublishProtocolHealth(ctx, proof) }()
	go func() {
		defer wg.Done()
		fail <- s.AppendAudit(ctx, audit.Event{ID: "concurrent-health-edit", Action: "test"})
	}()
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatalf("fixture failure: %v", err)
		}
	}
	assertHealth(t, s, proof)
	// Reject a database write after memory validation; cache must remain committed.
	before, revision := healthSnapshot(t, s)
	_, err = s.db.Exec(`CREATE FUNCTION reject_health_write() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected health failure'; END;$$; CREATE TRIGGER reject_health_write BEFORE INSERT OR UPDATE ON hl_panel_protocol_health FOR EACH ROW EXECUTE FUNCTION reject_health_write()`)
	if err != nil {
		t.Fatalf("fixture failure: %v", err)
	}
	next := proof
	next.VerifiedAt = next.VerifiedAt.Add(time.Second)
	next.LeaseExpiresAt = next.LeaseExpiresAt.Add(time.Second)
	if err := s.PublishProtocolHealth(ctx, next); err == nil {
		t.Fatal("injected failure ignored")
	}
	after, current := healthSnapshot(t, s)
	if !bytes.Equal(before, after) || revision != current {
		t.Fatal("failed write changed committed snapshot")
	}
	assertHealth(t, s, proof)
}

func TestProtocolHealthEditRejectsOldDeploymentProof(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	if results, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil || results[0] != nil {
		t.Fatal(results, err)
	}
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	if err := s.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	rule, err := s.ForwardingRule(ctx, proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	previousRevision := rule.Revision
	rule.Revision++
	rule.Name = "edited-health-rule"
	_, _, err = s.UpdateForwardingRule(ctx, forwarding.UpdateInput{Rule: rule, ExpectedRevision: previousRevision, IdempotencyKey: "health-edit", RequestSHA256: "health-edit", CreatedBy: "admin-one"}, audit.Event{ID: "health-edit", Action: "forwarding_rule.update"})
	if err != nil {
		t.Fatal(err)
	}
	_, revision := healthSnapshot(t, s)
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	if err := s.PublishProtocolHealth(ctx, proof); err == nil {
		t.Fatal("old deployment proof accepted after edit")
	}
	_, after := healthSnapshot(t, s)
	if after != revision {
		t.Fatal("stale proof mutated state")
	}
}
