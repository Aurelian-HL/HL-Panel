package postgressnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func ruleDeletionPGFixture(t *testing.T) (*Store, string, rulegroups.BatchRequest, string, string) {
	t.Helper()
	s, dsn, proof := protocolHealthFixture(t)
	ctx := context.Background()
	identity, err := s.CredentialForAdministrator(ctx, "admin-one", proof.RuleID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := s.RuntimeMaterialForNode(ctx, proof.NodeID, identity.Binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := s.ListPublicRuntimeMaterials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(materials) == 0 {
		service, err := vlessruntime.NewService(s, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Bind(ctx, "admin-one", identity.Binding.ID, proof.NodeID, material.RealityPrivateKey, 0, "delete-runtime-bind"); err != nil {
			t.Fatal(err)
		}
	}
	request := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{proof.RuleID}, ExpectedRevisions: map[string]int64{proof.RuleID: proof.RuleRevision}}
	return s, dsn, request, identity.Binding.ID, identity.Binding.EndpointPoolID
}

func TestPostgreSQLRuleDeleteLifecyclePersistsAndReplaysAfterRestart(t *testing.T) {
	s, dsn, request, bindingID, poolID := ruleDeletionPGFixture(t)
	ctx := context.Background()
	service := rulegroups.NewService(s, time.Now)
	if _, replay, err := service.BatchForAdministrator(ctx, "admin-one", request, "delete-runtime-rule"); err != nil || replay {
		t.Fatal("rule lifecycle deletion failed", err)
	}
	restarted, err := Open(ctx, dsn, auth.Administrator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	for _, store := range []*Store{s, restarted} {
		if _, err := store.ForwardingRule(ctx, request.RuleIDs[0]); !errors.Is(err, faults.ErrNotFound) {
			t.Fatal("deleted rule still exists", err)
		}
		if _, err := store.EndpointPool(ctx, poolID); !errors.Is(err, faults.ErrNotFound) {
			t.Fatal("deleted pool still exists", err)
		}
		bindings, err := store.List(ctx)
		if err != nil || len(bindings) != 1 || bindings[0].ID != bindingID || bindings[0].State != vlessidentity.StateRevoked {
			t.Fatal("revoked identity did not persist", err)
		}
		materials, err := store.ListPublicRuntimeMaterials(ctx)
		if err != nil || len(materials) != 1 || materials[0].State != vlessruntime.StateRevoked || materials[0].Revision != 2 {
			t.Fatal("revoked runtime did not persist", err)
		}
		config, _, err := store.DesiredNodeConfig(ctx, "node")
		if err != nil {
			t.Fatal("deletion did not publish node configuration", err)
		}
		var bundle agentv1.ConfigurationBundle
		if err := json.Unmarshal(config.Config, &bundle); err != nil || len(bundle.Fragments) != 0 {
			t.Fatal("deleted listener remains in desired configuration", err)
		}
	}
	before, _ := healthSnapshot(t, s)
	if _, replay, err := rulegroups.NewService(restarted, time.Now).BatchForAdministrator(ctx, "admin-one", request, "delete-runtime-rule"); err != nil || !replay {
		t.Fatal("deletion replay after restart failed", err)
	}
	after, _ := healthSnapshot(t, s)
	if !bytes.Equal(before, after) {
		t.Fatal("replay changed durable business state")
	}
}

func TestPostgreSQLRuleDeleteLifecycleRollsBackDeferredCommitFailure(t *testing.T) {
	s, _, request, bindingID, _ := ruleDeletionPGFixture(t)
	ctx := context.Background()
	before, revision := healthSnapshot(t, s)
	cached := s.readCache
	_, err := s.db.ExecContext(ctx, `CREATE FUNCTION reject_rule_delete_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated deletion commit rejection'; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_rule_delete_commit AFTER UPDATE ON nyvp_control_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_rule_delete_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	service := rulegroups.NewService(s, time.Now)
	if _, _, err := service.BatchForAdministrator(ctx, "admin-one", request, "retry-rule-delete"); err == nil {
		t.Fatal("deferred commit failure was ignored")
	}
	after, afterRevision := healthSnapshot(t, s)
	if revision != afterRevision || !bytes.Equal(before, after) || s.readCache.revision != cached.revision || s.readCache.forwarding != cached.forwarding || s.readCache.identities != cached.identities {
		t.Fatal("failed deletion published uncommitted state or cache")
	}
	if _, err := s.RuntimeMaterialForNode(ctx, "node", bindingID); err != nil {
		t.Fatal("failed deletion revoked live runtime material", err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_rule_delete_commit ON nyvp_control_snapshots; DROP FUNCTION reject_rule_delete_commit()`); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := service.BatchForAdministrator(ctx, "admin-one", request, "retry-rule-delete"); err != nil || replay {
		t.Fatal("rolled back deletion could not retry", err)
	}
}
