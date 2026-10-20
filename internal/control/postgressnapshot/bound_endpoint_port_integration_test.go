package postgressnapshot

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

func boundPortPGRequest(r forwarding.Rule) forwarding.Request {
	return forwarding.Request{
		Name: r.Name, CustomerID: r.CustomerID, OwnerKind: r.OwnerKind, OwnerID: r.OwnerID,
		RuleGroupID: r.RuleGroupID, EntryGroupID: r.EntryGroupID, ExitGroupID: r.ExitGroupID,
		EgressMode: r.EgressMode, TunnelTransport: r.TunnelTransport,
		VLESSOutboundMode: r.VLESSOutboundMode, VLESSSOCKS5Host: r.VLESSSOCKS5Host, VLESSSOCKS5Port: r.VLESSSOCKS5Port,
		IngressProtocol: r.IngressProtocol, VLESSFlow: r.VLESSFlow, VLESSFlowExplicit: r.VLESSFlowExplicit,
		RealityServerName: r.RealityServerName, RealityPublicKey: r.RealityPublicKey, RealityShortID: r.RealityShortID, RealityDestination: r.RealityDestination,
		Protocol: r.Protocol, ListenPort: r.ListenPort, Targets: r.Targets, SelectionPolicy: r.SelectionPolicy,
		AcceptProxyProtocol: r.AcceptProxyProtocol, SendProxyProtocol: r.SendProxyProtocol,
		SpeedLimitMbps: r.SpeedLimitMbps, IPLimit: r.IPLimit, ConnectionLimit: r.ConnectionLimit,
		TrafficLimitBytes: r.TrafficLimitBytes, TrafficQuotaMonthly: r.TrafficQuotaMonthly,
		Paused: r.Paused, Description: r.Description, Revision: r.Revision,
	}
}

func TestPostgreSQLBoundEndpointPortUpdatePersistsAndReplays(t *testing.T) {
	s, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	rejected, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof})
	if err != nil || len(rejected) != 1 || rejected[0] != nil {
		t.Fatal("fixture activation failed", err)
	}
	proof.VerifiedAt = proof.VerifiedAt.Add(time.Second)
	proof.LeaseExpiresAt = proof.LeaseExpiresAt.Add(time.Second)
	if err := s.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal("fixture health overlay failed", err)
	}
	var overlays int
	if err := s.db.QueryRow(`SELECT count(*) FROM hl_panel_protocol_health`).Scan(&overlays); err != nil || overlays != 1 {
		t.Fatal("fixture did not create committed health overlay", err)
	}
	reader, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	before, err := reader.ForwardingRule(ctx, proof.RuleID)
	if err != nil || !before.Deployed {
		t.Fatal("fixture rule not ready", err)
	}
	identityBefore, err := reader.CredentialForAdministrator(ctx, "admin-one", proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	materialBefore, err := reader.RuntimeMaterialForNode(ctx, proof.NodeID, identityBefore.Binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.DesiredNodeConfig(ctx, proof.NodeID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("applied fixture unexpectedly has pending configuration", err)
	}
	request := boundPortPGRequest(before)
	request.ListenPort++
	updated, replay, err := forwarding.NewService(s, time.Now).UpdateForAdministrator(ctx, "admin-one", proof.RuleID, request, "persist-bound-port")
	if err != nil || replay || updated.Revision != before.Revision+1 || updated.ListenPort != request.ListenPort {
		t.Fatal("port update failed", err)
	}
	restarted, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	for _, store := range []*Store{s, reader, restarted} {
		current, err := store.ForwardingRule(ctx, proof.RuleID)
		if err != nil || current.Revision != updated.Revision || current.ListenPort != updated.ListenPort || current.Deployed {
			t.Fatal("stale or ready rule after port update", err)
		}
		identity, err := store.CredentialForAdministrator(ctx, "admin-one", proof.RuleID)
		if err != nil || !reflect.DeepEqual(identity, identityBefore) {
			t.Fatal("identity changed after port update/restart", err)
		}
		pool, err := store.EndpointPool(ctx, identity.Binding.EndpointPoolID)
		if err != nil || pool.ID != identityBefore.Binding.EndpointPoolID || pool.RuleID != proof.RuleID || pool.Port != current.ListenPort {
			t.Fatal("bound endpoint did not persist atomically", err)
		}
		material, err := store.RuntimeMaterialForNode(ctx, proof.NodeID, identity.Binding.ID)
		if err != nil || !reflect.DeepEqual(material, materialBefore) {
			t.Fatal("runtime credentials changed after port update/restart", err)
		}
		state, err := store.GatewayMembershipState(ctx, pool.ID)
		if err != nil || len(state.ProtocolHealth) != 0 || len(state.Members) != 1 || state.Members[0].LastHealthAt != nil || state.Members[0].LastHealthReason != "listener_port_changed" {
			t.Fatal("old health survived port update/restart", err)
		}
		config, _, err := store.DesiredNodeConfig(ctx, proof.NodeID)
		if err != nil || config.Generation != proof.NodeConfigGeneration+1 || config.ConfigSHA256 == proof.ConfigSHA256 {
			t.Fatal("new listener configuration did not persist", err)
		}
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM hl_panel_protocol_health`).Scan(&overlays); err != nil || overlays != 0 {
		t.Fatal("old health overlay survived transaction", err)
	}
	if err := reader.PublishProtocolHealth(ctx, proof); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("old deployment proof was accepted", err)
	}
	rawBefore, revisionBefore := healthSnapshot(t, s)
	if result, replay, err := forwarding.NewService(restarted, time.Now).UpdateForAdministrator(ctx, "admin-one", proof.RuleID, request, "persist-bound-port"); err != nil || !replay || result.ID != updated.ID || result.Revision != updated.Revision {
		t.Fatal("persisted idempotency did not replay", err)
	}
	rawAfter, _ := healthSnapshot(t, s)
	// A replay may advance the repository snapshot revision, but cannot append
	// an audit, regenerate credentials or change the persisted payload.
	if !bytes.Equal(rawBefore, rawAfter) || revisionBefore < 1 {
		t.Fatal("replay changed business state")
	}
}

func TestPostgreSQLBoundEndpointPortUpdateRollsBackDeferredCommitFailure(t *testing.T) {
	s, _, proof := protocolHealthFixture(t)
	ctx := context.Background()
	if _, err := s.PublishProtocolHealthBatch(ctx, []gatewaymembership.ProtocolObservation{proof}); err != nil {
		t.Fatal(err)
	}
	proof.VerifiedAt, proof.LeaseExpiresAt = proof.VerifiedAt.Add(time.Second), proof.LeaseExpiresAt.Add(time.Second)
	if err := s.PublishProtocolHealth(ctx, proof); err != nil {
		t.Fatal(err)
	}
	rule, err := s.ForwardingRule(ctx, proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	request := boundPortPGRequest(rule)
	request.ListenPort++
	before, revision := healthSnapshot(t, s)
	cached := s.readCache
	_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_port_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated port commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_port_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_port_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	service := forwarding.NewService(s, time.Now)
	if _, _, err := service.UpdateForAdministrator(ctx, "admin-one", proof.RuleID, request, "retry-bound-port"); err == nil {
		t.Fatal("deferred commit failure was ignored")
	}
	after, afterRevision := healthSnapshot(t, s)
	if revision != afterRevision || !bytes.Equal(before, after) || s.readCache.revision != cached.revision || s.readCache.forwarding != cached.forwarding || s.readCache.identities != cached.identities {
		t.Fatal("failed commit published uncommitted state/cache")
	}
	assertHealth(t, s, proof)
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_port_commit ON nyvp_control_snapshots; DROP FUNCTION reject_port_commit()`); err != nil {
		t.Fatal(err)
	}
	if updated, replay, err := service.UpdateForAdministrator(ctx, "admin-one", proof.RuleID, request, "retry-bound-port"); err != nil || replay || updated.ListenPort != request.ListenPort {
		t.Fatal("rolled back idempotency could not retry", err)
	}
}

func TestPostgreSQLBoundEndpointPortUpdateSerializesTwoStores(t *testing.T) {
	s, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	second, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	rule, err := s.ForwardingRule(ctx, proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, store := range []*Store{s, second} {
		request := boundPortPGRequest(rule)
		request.ListenPort += i + 1
		wg.Add(1)
		go func(store *Store, request forwarding.Request) {
			defer wg.Done()
			_, _, err := forwarding.NewService(store, time.Now).UpdateForAdministrator(ctx, "admin-one", rule.ID, request, "two-stores-"+strconv.Itoa(request.ListenPort))
			results <- err
		}(store, request)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, faults.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	current, err := second.ForwardingRule(ctx, rule.ID)
	if err != nil || success != 1 || conflicts != 1 || current.Revision != rule.Revision+1 {
		t.Fatal("concurrent stores failed revision compare and swap", err)
	}
	identity, err := second.CredentialForAdministrator(ctx, "admin-one", rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := second.EndpointPool(ctx, identity.Binding.EndpointPoolID)
	if err != nil || pool.RuleID != rule.ID || pool.Port != current.ListenPort {
		t.Fatal("concurrent stores detached endpoint from rule", err)
	}
}
