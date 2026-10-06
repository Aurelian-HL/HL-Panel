package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func batchRuleFixture(t *testing.T) (*Store, forwarding.Request) {
	t.Helper()
	store, request := forwardingRepositoryFixture(t)
	store.nodes["batch-node"] = nodes.Node{ID: "batch-node"}
	store.membersByGroup[request.EntryGroupID]["batch-node"] = groups.Member{GroupID: request.EntryGroupID, NodeID: "batch-node", Weight: 1}
	return store, request
}

func batchDesiredRuleIDs(t *testing.T, store *Store) (int64, map[string]bool) {
	t.Helper()
	generation := store.nodes["batch-node"].DesiredGeneration
	if generation == 0 {
		t.Fatal("batch node has no desired configuration")
	}
	config, exists := store.nodeConfigsByNode["batch-node"][generation]
	if !exists {
		t.Fatalf("desired generation %d was not stored", generation)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, fragment := range bundle.Fragments {
		if id, ok := strings.CutPrefix(fragment.GroupID, generations.ForwardingGOSTFragmentPrefix); ok {
			ids[id] = true
		}
	}
	return generation, ids
}

func TestBatchMutationsRecompileDesiredNodeBundle(t *testing.T) {
	store, request := batchRuleFixture(t)
	ctx := context.Background()
	forwardingService := forwarding.NewService(store, nil)
	batchService := rulegroups.NewService(store, nil)
	first, _, err := forwardingService.Create(ctx, "admin", request, "batch-bundle-first")
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "second"
	second, _, err := forwardingService.Create(ctx, "admin", request, "batch-bundle-second")
	if err != nil {
		t.Fatal(err)
	}
	initialGeneration, initialIDs := batchDesiredRuleIDs(t, store)
	if !initialIDs[first.ID] || !initialIDs[second.ID] || len(initialIDs) != 2 {
		t.Fatalf("initial desired rules = %v", initialIDs)
	}

	pause := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{first.ID}, ExpectedRevisions: map[string]int64{first.ID: first.Revision}}
	if _, _, err := batchService.Batch(ctx, "admin", pause, "batch-bundle-pause"); err != nil {
		t.Fatal(err)
	}
	pausedGeneration, pausedIDs := batchDesiredRuleIDs(t, store)
	if pausedGeneration != initialGeneration+1 || pausedIDs[first.ID] || !pausedIDs[second.ID] || len(pausedIDs) != 1 {
		t.Fatalf("pause did not withdraw listener: generation=%d rules=%v", pausedGeneration, pausedIDs)
	}

	resume := rulegroups.BatchRequest{Operation: rulegroups.BatchResume, RuleIDs: []string{first.ID}, ExpectedRevisions: map[string]int64{first.ID: first.Revision + 1}}
	if _, _, err := batchService.Batch(ctx, "admin", resume, "batch-bundle-resume"); err != nil {
		t.Fatal(err)
	}
	resumedGeneration, resumedIDs := batchDesiredRuleIDs(t, store)
	if resumedGeneration != pausedGeneration+1 || !resumedIDs[first.ID] || !resumedIDs[second.ID] || len(resumedIDs) != 2 {
		t.Fatalf("resume did not restore listener: generation=%d rules=%v", resumedGeneration, resumedIDs)
	}

	remove := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{second.ID}, ExpectedRevisions: map[string]int64{second.ID: second.Revision}}
	if _, _, err := batchService.Batch(ctx, "admin", remove, "batch-bundle-delete"); err != nil {
		t.Fatal(err)
	}
	deletedGeneration, deletedIDs := batchDesiredRuleIDs(t, store)
	if deletedGeneration != resumedGeneration+1 || !deletedIDs[first.ID] || deletedIDs[second.ID] || len(deletedIDs) != 1 {
		t.Fatalf("delete did not withdraw listener: generation=%d rules=%v", deletedGeneration, deletedIDs)
	}
	if _, replayed, err := batchService.Batch(ctx, "admin", remove, "batch-bundle-delete"); err != nil || !replayed || store.nodes["batch-node"].DesiredGeneration != deletedGeneration {
		t.Fatalf("delete replay changed desired config: replay=%v err=%v", replayed, err)
	}
}

func TestBatchCompileFailureRollsBackRulesIdempotencyAuditAndDesiredBundle(t *testing.T) {
	store, request := batchRuleFixture(t)
	ctx := context.Background()
	forwardingService := forwarding.NewService(store, nil)
	first, _, err := forwardingService.Create(ctx, "admin", request, "batch-rollback-first")
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "second"
	second, _, err := forwardingService.Create(ctx, "admin", request, "batch-rollback-second")
	if err != nil {
		t.Fatal(err)
	}
	store.nodes["z-batch-node"] = nodes.Node{ID: "z-batch-node"}
	store.membersByGroup[request.EntryGroupID]["z-batch-node"] = groups.Member{GroupID: request.EntryGroupID, NodeID: "z-batch-node", Weight: 1}
	store.deviceGroups["broken-entry"] = groups.DeviceGroup{ID: "broken-entry", Name: "broken", Kind: groups.KindEntry}
	store.membersByGroup["broken-entry"] = map[string]groups.Member{
		"z-batch-node": {GroupID: "broken-entry", NodeID: "z-batch-node", Weight: 1},
	}
	brokenNetwork := store.groupNetworks[request.EntryGroupID]
	brokenNetwork.GroupID = "broken-entry"
	store.groupNetworks["broken-entry"] = brokenNetwork
	corrupted := cloneForwardingRule(store.forwardRules[first.ID])
	corrupted.ID = "bad-existing"
	corrupted.EntryGroupID = "broken-entry"
	corrupted.Targets[0].Host = ""
	store.forwardRules[corrupted.ID] = corrupted
	beforeRule := cloneForwardingRule(store.forwardRules[second.ID])
	beforeNodes := make(map[string]nodes.Node)
	beforeConfigs := make(map[string]map[int64][]byte)
	for _, nodeID := range []string{"batch-node", "z-batch-node"} {
		beforeNodes[nodeID] = store.nodes[nodeID]
		beforeConfigs[nodeID] = make(map[int64][]byte)
		for generation, config := range store.nodeConfigsByNode[nodeID] {
			beforeConfigs[nodeID][generation] = append([]byte(nil), config.Config...)
		}
	}
	beforeAudits := len(store.AuditEvents())
	requestBatch := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{second.ID}, ExpectedRevisions: map[string]int64{second.ID: second.Revision}}
	batchService := rulegroups.NewService(store, nil)
	if _, _, err := batchService.Batch(ctx, "admin", requestBatch, "batch-rollback"); err == nil {
		t.Fatal("expected bundle compilation to fail")
	}
	if got := store.forwardRules[second.ID]; !reflect.DeepEqual(got, beforeRule) {
		t.Fatalf("failed batch changed rule: %+v", got)
	}
	if len(store.AuditEvents()) != beforeAudits {
		t.Fatal("failed batch appended audit")
	}
	for nodeID, beforeNode := range beforeNodes {
		if !reflect.DeepEqual(store.nodes[nodeID], beforeNode) || len(store.nodeConfigsByNode[nodeID]) != len(beforeConfigs[nodeID]) {
			t.Fatalf("failed batch changed node %s or its desired config count", nodeID)
		}
		for generation, before := range beforeConfigs[nodeID] {
			if !bytes.Equal(store.nodeConfigsByNode[nodeID][generation].Config, before) {
				t.Fatalf("failed batch changed node %s desired generation %d", nodeID, generation)
			}
		}
	}
	if _, exists := store.businessIdempotency["admin\x00forwarding.batch\x00batch-rollback"]; exists {
		t.Fatal("failed batch retained idempotency record")
	}
	delete(store.forwardRules, corrupted.ID)
	if _, replayed, err := batchService.Batch(ctx, "admin", requestBatch, "batch-rollback"); err != nil || replayed {
		t.Fatalf("retry after fixing compiler input failed: replay=%v err=%v", replayed, err)
	}
}

func TestRuleGroupsAndBatchRulesAreTransactionalAndIdempotent(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	ctx := context.Background()
	groupService := rulegroups.NewService(store, nil)
	group, replayed, err := groupService.Create(ctx, "admin-test", rulegroups.Request{Name: "核心线路", Description: "生产规则"}, "group-create")
	if err != nil || replayed || group.Revision != 1 {
		t.Fatalf("create rule group: replay=%v err=%v", replayed, err)
	}
	if replay, ok, err := groupService.Create(ctx, "admin-test", rulegroups.Request{Name: "核心线路", Description: "生产规则"}, "group-create"); err != nil || !ok || replay.ID != group.ID {
		t.Fatalf("replay rule group: replay=%v err=%v", ok, err)
	}

	forwardingService := forwarding.NewService(store, nil)
	request.RuleGroupID = group.ID
	first, _, err := forwardingService.CreateForAdministrator(ctx, "admin-test", request, "rule-one")
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "second"
	second, _, err := forwardingService.CreateForAdministrator(ctx, "admin-test", request, "rule-two")
	if err != nil {
		t.Fatal(err)
	}
	batch := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: first.Revision, second.ID: second.Revision}}
	paused, replayed, err := groupService.Batch(ctx, "admin-test", batch, "pause-two")
	if err != nil || replayed || len(paused.Items) != 2 {
		t.Fatalf("batch pause: replay=%v err=%v", replayed, err)
	}
	for _, item := range paused.Items {
		if !item.Paused || item.Revision != 2 || item.Status != forwarding.StatusPaused {
			t.Fatalf("unexpected paused rule: %+v", item)
		}
	}
	if replay, ok, err := groupService.Batch(ctx, "admin-test", batch, "pause-two"); err != nil || !ok || len(replay.Items) != 2 {
		t.Fatalf("batch replay: replay=%v err=%v", ok, err)
	}

	stale := rulegroups.BatchRequest{Operation: rulegroups.BatchMoveGroup, RuleGroupID: "", RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: 2, second.ID: 1}}
	if _, _, err := groupService.Batch(ctx, "admin-test", stale, "stale-move"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("expected stale conflict, got %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		item, _ := forwardingService.Get(ctx, id)
		if item.RuleGroupID != group.ID || item.Revision != 2 {
			t.Fatalf("failed batch partially changed %s: %+v", id, item)
		}
	}

	unassign := rulegroups.BatchRequest{Operation: rulegroups.BatchMoveGroup, RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: 2, second.ID: 2}}
	moved, _, err := groupService.Batch(ctx, "admin-test", unassign, "unassign")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range moved.Items {
		if item.RuleGroupID != "" || item.Revision != 3 {
			t.Fatalf("move result: %+v", item)
		}
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := rulegroups.NewService(restored, nil).ListForAdministrator(ctx, "admin-test")
	if err != nil || len(owned) != 1 || owned[0].ID != group.ID {
		t.Fatalf("snapshot lost rule-group ownership: %+v err=%v", owned, err)
	}
}

func TestBatchResumeRejectsInactiveCustomerWithoutPartialWrite(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	ctx := context.Background()
	forwardingService := forwarding.NewService(store, nil)
	request.Paused = true
	first, _, _ := forwardingService.Create(ctx, "admin", request, "first-paused")
	request.Name = "second"
	second, _, _ := forwardingService.Create(ctx, "admin", request, "second-paused")
	customer := store.customers[request.CustomerID]
	customer.Disabled = true
	store.customers[customer.ID] = customer
	input := rulegroups.BatchRequest{Operation: rulegroups.BatchResume, RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: 1, second.ID: 1}}
	if _, _, err := rulegroups.NewService(store, nil).Batch(ctx, "admin", input, "resume-inactive"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("expected inactive customer conflict, got %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		item := store.forwardRules[id]
		if !item.Paused || item.Revision != 1 {
			t.Fatalf("failed resume partially changed %s", id)
		}
	}
}

func TestBatchDeleteIsAtomicIdempotentAndAudited(t *testing.T) {
	store, createRequest := forwardingRepositoryFixture(t)
	ctx := context.Background()
	forwardingService := forwarding.NewService(store, nil)
	createRequest.Name = "first"
	first, _, err := forwardingService.Create(ctx, "admin", createRequest, "delete-first")
	if err != nil {
		t.Fatal(err)
	}
	createRequest.Name = "second"
	second, _, err := forwardingService.Create(ctx, "admin", createRequest, "delete-second")
	if err != nil {
		t.Fatal(err)
	}
	request := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{first.ID, second.ID}, ExpectedRevisions: map[string]int64{first.ID: first.Revision, second.ID: second.Revision}}
	result, replayed, err := rulegroups.NewService(store, nil).Batch(ctx, "admin", request, "delete-two")
	if err != nil || replayed || len(result.Items) != 2 {
		t.Fatalf("delete failed: %+v replay=%v err=%v", result, replayed, err)
	}
	if _, err := store.ForwardingRule(ctx, first.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("first rule still exists: %v", err)
	}
	if replay, ok, err := rulegroups.NewService(store, nil).Batch(ctx, "admin", request, "delete-two"); err != nil || !ok || len(replay.Items) != 2 {
		t.Fatalf("delete replay failed: %+v replay=%v err=%v", replay, ok, err)
	}

	createRequest.Name = "third"
	third, _, err := forwardingService.Create(ctx, "admin", createRequest, "delete-third")
	if err != nil {
		t.Fatal(err)
	}
	createRequest.Name = "fourth"
	fourth, _, err := forwardingService.Create(ctx, "admin", createRequest, "delete-fourth")
	if err != nil {
		t.Fatal(err)
	}
	stale := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{third.ID, fourth.ID}, ExpectedRevisions: map[string]int64{third.ID: third.Revision, fourth.ID: fourth.Revision + 1}}
	if _, _, err := rulegroups.NewService(store, nil).Batch(ctx, "admin", stale, "delete-stale"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if _, err := store.ForwardingRule(ctx, third.ID); err != nil {
		t.Fatal("atomic delete removed a rule before conflict")
	}
}

func TestBatchDeleteRevokesAutomaticVLESSIdentityAndRestoresHistory(t *testing.T) {
	fixture := newVLESSBundleFixture(t, false)
	ctx := context.Background()
	now := fixture.rule.UpdatedAt.Add(time.Minute)

	// Deletion is intentionally blocked while a node still has live secret
	// material. Once that material is revoked, the rule owns the identity
	// lifecycle and can safely revoke the customer credential with the rule.
	material := fixture.store.vlessRuntimeMaterials[fixture.materialID]
	material.State = vlessruntime.StateRevoked
	material.Revision++
	material.RevokedAt = &now
	material.UpdatedAt = now
	material.CredentialUUID = ""
	material.RealityPrivateKey = ""
	fixture.store.vlessRuntimeMaterials[fixture.materialID] = material

	request := rulegroups.BatchRequest{
		Operation:         rulegroups.BatchDelete,
		RuleIDs:           []string{fixture.rule.ID},
		ExpectedRevisions: map[string]int64{fixture.rule.ID: fixture.rule.Revision},
	}
	result, replayed, err := rulegroups.NewService(fixture.store, func() time.Time { return now }).BatchForAdministrator(ctx, "admin", request, "delete-vless-rule")
	if err != nil || replayed || len(result.Items) != 1 {
		t.Fatalf("VLESS rule delete failed: result=%+v replayed=%v err=%v", result, replayed, err)
	}
	if _, err := fixture.store.ForwardingRule(ctx, fixture.rule.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("deleted VLESS rule still exists: %v", err)
	}
	if _, err := fixture.store.EndpointPool(ctx, fixture.endpointID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("deleted VLESS endpoint pool still exists: %v", err)
	}
	binding := fixture.store.vlessBindings[fixture.bindingID]
	if binding.Binding.State != vlessidentity.StateRevoked || binding.CredentialUUID != "" || binding.Binding.RevokedAt == nil {
		t.Fatalf("automatic VLESS identity was not revoked safely: %+v", binding)
	}
	foundRevocationAudit := false
	for _, event := range fixture.store.AuditEvents() {
		if event.Action == "vless_identity.revoke" && event.ResourceID == fixture.bindingID {
			foundRevocationAudit = true
			break
		}
	}
	if !foundRevocationAudit {
		t.Fatal("automatic identity revocation was not audited")
	}
	raw, err := fixture.store.EncodeSnapshot()
	if err != nil {
		t.Fatalf("encode snapshot after VLESS rule deletion: %v", err)
	}
	if _, err := DecodeSnapshot(raw); err != nil {
		t.Fatalf("revoked identity history made snapshot unrecoverable: %v", err)
	}
	if replay, replayed, err := rulegroups.NewService(fixture.store, func() time.Time { return now }).BatchForAdministrator(ctx, "admin", request, "delete-vless-rule"); err != nil || !replayed || len(replay.Items) != 1 {
		t.Fatalf("VLESS delete replay failed: result=%+v replayed=%v err=%v", replay, replayed, err)
	}
}

func TestBatchDeleteStillBlocksActiveVLESSRuntimeMaterial(t *testing.T) {
	fixture := newVLESSBundleFixture(t, true)
	request := rulegroups.BatchRequest{
		Operation:         rulegroups.BatchDelete,
		RuleIDs:           []string{fixture.rule.ID},
		ExpectedRevisions: map[string]int64{fixture.rule.ID: fixture.rule.Revision},
	}
	if _, _, err := rulegroups.NewService(fixture.store, nil).BatchForAdministrator(context.Background(), "admin", request, "delete-active-vless-rule"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("active VLESS runtime material did not block rule deletion: %v", err)
	}
	if _, err := fixture.store.ForwardingRule(context.Background(), fixture.rule.ID); err != nil {
		t.Fatalf("blocked VLESS rule deletion removed the rule: %v", err)
	}
}

func TestAdministratorBatchRejectsCustomerRuleWithCollidingSubjectID(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	ctx := context.Background()
	rule, _, err := forwarding.NewService(store, nil).Create(ctx, "admin-test", request, "collision-seed")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy/corrupt customer identifier colliding with the
	// administrator's synthetic traffic subject. Ownership must still win.
	colliding := store.forwardRules[rule.ID]
	colliding.CustomerID = forwarding.AdministratorSubjectID("admin-test")
	store.forwardRules[rule.ID] = colliding
	beforeAudits := len(store.AuditEvents())
	batch := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}
	if _, _, err := rulegroups.NewService(store, nil).BatchForAdministrator(ctx, "admin-test", batch, "collision-batch"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("administrator batch accepted colliding customer rule: %v", err)
	}
	got := store.forwardRules[rule.ID]
	if got.Paused || got.Revision != rule.Revision || len(store.AuditEvents()) != beforeAudits {
		t.Fatalf("rejected batch changed rule or audit: %+v", got)
	}
	if _, exists := store.businessIdempotency["admin-rules:admin-test\x00forwarding.batch\x00collision-batch"]; exists {
		t.Fatal("rejected batch retained idempotency record")
	}
}

func TestAdministratorRuleGroupsAreIsolatedIncludingBatchTarget(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	ctx := context.Background()
	service := rulegroups.NewService(store, nil)
	first, _, err := service.Create(ctx, "admin-test", rulegroups.Request{Name: "线路"}, "group-one")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.Create(ctx, "admin-two", rulegroups.Request{Name: "线路"}, "group-two")
	if err != nil {
		t.Fatal(err)
	}
	store.ruleGroups["legacy"] = rulegroups.RuleGroup{ID: "legacy", Name: "旧分组", Revision: 1}
	listed, err := service.ListForAdministrator(ctx, "admin-test")
	if err != nil || len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("administrator list leaked groups: %+v err=%v", listed, err)
	}
	for _, id := range []string{second.ID, "legacy"} {
		if _, err := service.GetForAdministrator(ctx, "admin-test", id); !errors.Is(err, faults.ErrNotFound) {
			t.Fatalf("foreign group %s was readable: %v", id, err)
		}
		if _, _, err := service.Update(ctx, "admin-test", id, rulegroups.Request{Name: "夺取", Revision: 1}, "stolen-"+id); !errors.Is(err, faults.ErrNotFound) {
			t.Fatalf("foreign group %s was writable: %v", id, err)
		}
	}
	request.RuleGroupID = second.ID
	if _, _, err := forwarding.NewService(store, nil).CreateForAdministrator(ctx, "admin-test", request, "foreign-group-rule"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("administrator rule referenced a foreign group: %v", err)
	}
	request.RuleGroupID = first.ID
	if _, _, err := forwarding.NewService(store, nil).CreateForCustomer(ctx, request.CustomerID, request, "customer-private-group"); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("customer rule referenced an administrator group: %v", err)
	}
	request.RuleGroupID = first.ID
	rule, _, err := forwarding.NewService(store, nil).CreateForAdministrator(ctx, "admin-test", request, "owned-rule")
	if err != nil {
		t.Fatal(err)
	}
	beforeAudits := len(store.AuditEvents())
	move := rulegroups.BatchRequest{Operation: rulegroups.BatchMoveGroup, RuleGroupID: second.ID, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}
	if _, _, err := service.BatchForAdministrator(ctx, "admin-test", move, "foreign-target"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("cross-owner move returned %v", err)
	}
	if got := store.forwardRules[rule.ID]; got.RuleGroupID != first.ID || got.Revision != rule.Revision || len(store.AuditEvents()) != beforeAudits {
		t.Fatalf("rejected move changed rule or audit: %+v", got)
	}
	move.RuleGroupID = "legacy"
	if _, _, err := service.BatchForAdministrator(ctx, "admin-test", move, "legacy-target"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("legacy group was assigned by batch: %v", err)
	}
}

func TestLegacyRuleGroupSnapshotLoadsWithoutAssigningOwner(t *testing.T) {
	store, _ := forwardingRepositoryFixture(t)
	store.ruleGroups["legacy"] = rulegroups.RuleGroup{ID: "legacy", Name: "旧分组", Revision: 1}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var old snapshot
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.Version = ruleGroupOwnershipSnapshotVersion - 1
	raw, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	items, err := rulegroups.NewService(restored, nil).ListForAdministrator(context.Background(), "admin")
	if err != nil || len(items) != 0 {
		t.Fatalf("legacy group assigned to administrator: %+v err=%v", items, err)
	}
	if _, err := restored.RuleGroup(context.Background(), "legacy"); err != nil {
		t.Fatalf("legacy group was lost: %v", err)
	}
	group := old.RuleGroups["legacy"]
	group.OwnerAdministratorID = "admin-test"
	old.RuleGroups["legacy"] = group
	raw, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err == nil {
		t.Fatal("legacy snapshot accepted newer rule-group ownership field")
	}
}
