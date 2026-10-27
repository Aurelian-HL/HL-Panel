package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func TestManagementRuleEditsPreserveRuntimeReadinessAndCAS(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	s, ctx := f.store, context.Background()
	service := forwarding.NewService(s, nil)
	before := s.forwardRules[f.rule.ID]
	config := s.nodeConfigsByNode[f.nodeID][s.nodes[f.nodeID].DesiredGeneration]
	binding, material := s.vlessBindings[f.bindingID], s.vlessRuntimeMaterials[f.materialID]
	upstream, probe := s.vlessSOCKS5Upstreams[before.ID], s.protocolProbes[before.ID]
	wake := s.DesiredConfigChanges(f.nodeID)
	defer s.ReleaseDesiredConfigChanges(f.nodeID, wake)
	request := boundPortRequest(before)
	request.Name, request.Description = "renamed without deployment", "operator note"
	request.TrafficLimitBytes = 1000000
	updated, replayed, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "management-only")
	if err != nil || replayed || updated.Revision != before.Revision+1 || updated.EffectiveRuntimeRevision() != before.EffectiveRuntimeRevision() {
		t.Fatal("management edit failed or replaced runtime identity", err)
	}
	assertCompiledRuleReady(t, f, true)
	current := s.nodeConfigsByNode[f.nodeID][s.nodes[f.nodeID].DesiredGeneration]
	if current.Generation != config.Generation || !bytes.Equal(current.Config, config.Config) || current.ConfigSHA256 != config.ConfigSHA256 {
		t.Fatal("management edit changed desired config")
	}
	if !reflect.DeepEqual(binding, s.vlessBindings[f.bindingID]) || !reflect.DeepEqual(material, s.vlessRuntimeMaterials[f.materialID]) || upstream != s.vlessSOCKS5Upstreams[before.ID] || probe != s.protocolProbes[before.ID] || proof != s.protocolHealth[f.endpointID][f.nodeID] {
		t.Fatal("management edit changed identity, upstream, probe or lease")
	}
	// A later unrelated compilation must also emit exactly the same fragment.
	if err := s.recompileForwardingGroupsLocked([]string{before.EntryGroupID}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if s.nodes[f.nodeID].DesiredGeneration != config.Generation {
		t.Fatal("deferred compilation changed a management-only fragment")
	}
	requireNoDesiredChange(t, wake)
	audits := len(s.AuditEvents())
	again, replayed, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "management-only")
	if err != nil || !replayed || again.Revision != updated.Revision || !again.Deployed || len(s.AuditEvents()) != audits {
		t.Fatal("management edit replay changed state or readiness", err)
	}
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "stale-management-edit"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("runtime optimization bypassed management CAS", err)
	}
	requireNoDesiredChange(t, wake)
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("restore separated revisions", err)
	}
	restoredFixture := f
	restoredFixture.store = restored
	assertCompiledRuleReady(t, restoredFixture, true)
}

func TestBatchMoveAndTransferRenameDoNotDeploy(t *testing.T) {
	for _, kind := range []string{"batch move", "transfer rename", "repeat resume"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := compiledRuleReadinessFixture(t)
			s, ctx := f.store, context.Background()
			before := s.forwardRules[f.rule.ID]
			generation := s.nodes[f.nodeID].DesiredGeneration
			wake := s.DesiredConfigChanges(f.nodeID)
			defer s.ReleaseDesiredConfigChanges(f.nodeID, wake)
			if kind == "batch move" {
				groups := rulegroups.NewService(s, nil)
				group, _, err := groups.Create(ctx, "admin", rulegroups.Request{Name: "display category"}, "display-category")
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = groups.BatchForAdministrator(ctx, "admin", rulegroups.BatchRequest{Operation: rulegroups.BatchMoveGroup, RuleGroupID: group.ID, RuleIDs: []string{before.ID}, ExpectedRevisions: map[string]int64{before.ID: before.Revision}}, "category-move")
				if err != nil {
					t.Fatal(err)
				}
			} else if kind == "repeat resume" {
				_, _, err := rulegroups.NewService(s, nil).BatchForAdministrator(ctx, "admin", rulegroups.BatchRequest{Operation: rulegroups.BatchResume, RuleIDs: []string{before.ID}, ExpectedRevisions: map[string]int64{before.ID: before.Revision}}, "repeat-resume")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				service := forwarding.NewService(s, nil)
				document, err := service.ExportForAdministrator(ctx, "admin")
				if err != nil || len(document.Rules) != 1 {
					t.Fatal("export", err)
				}
				document.Rules[0].Rule.Name = "transfer display rename"
				raw, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = service.ImportForAdministrator(ctx, "admin", forwarding.TransferRequest{Format: forwarding.ImportFormatJSONV1, Content: string(raw)}, "transfer-display-only")
				if err != nil {
					t.Fatal(err)
				}
			}
			if s.nodes[f.nodeID].DesiredGeneration != generation || s.forwardRules[before.ID].Revision != before.Revision+1 || s.forwardRules[before.ID].EffectiveRuntimeRevision() != before.EffectiveRuntimeRevision() {
				t.Fatal("display batch changed runtime or lost management revision")
			}
			assertCompiledRuleReady(t, f, true)
			requireNoDesiredChange(t, wake)
		})
	}
}

func TestPasswordEditAfterRenameRequiresNewRuntimeProof(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	s, ctx := f.store, context.Background()
	service := forwarding.NewService(s, nil)
	before := s.forwardRules[f.rule.ID]
	request := boundPortRequest(before)
	request.Name = "management revision ahead of runtime"
	renamed, _, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "before-password-rename")
	if err != nil {
		t.Fatal(err)
	}
	request = boundPortRequest(renamed)
	request.VLESSSOCKS5Username = s.vlessSOCKS5Upstreams[before.ID].Username
	request.VLESSSOCKS5Password = "new-isolated-test-password"
	updated, _, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "password-edit")
	if err != nil || updated.EffectiveRuntimeRevision() != updated.Revision || updated.EffectiveRuntimeRevision() <= before.EffectiveRuntimeRevision() {
		t.Fatal("secret change did not advance runtime", err)
	}
	if s.vlessSOCKS5Upstreams[before.ID].Password != request.VLESSSOCKS5Password || s.nodes[f.nodeID].DesiredGeneration <= proof.NodeConfigGeneration {
		t.Fatal("secret change did not reach desired config")
	}
	assertCompiledRuleReady(t, f, false)
	if err := s.PublishProtocolHealth(ctx, proof); err == nil {
		t.Fatal("old proof authorized changed secret")
	}
	attestCompiledFixture(t, f)
	assertCompiledRuleReady(t, f, false)
}

func TestFailedRuntimeEditAfterRenamePreservesServingState(t *testing.T) {
	f, proof := compiledRuleReadinessFixture(t)
	s, ctx := f.store, context.Background()
	service := forwarding.NewService(s, nil)
	request := boundPortRequest(s.forwardRules[f.rule.ID])
	request.Name = "rename before rejected runtime update"
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", f.rule.ID, request, "rename-before-failure"); err != nil {
		t.Fatal(err)
	}
	before := cloneForwardingRule(s.forwardRules[f.rule.ID])
	config := cloneNodeConfig(s.nodeConfigsByNode[f.nodeID][s.nodes[f.nodeID].DesiredGeneration])
	upstream, probe := s.vlessSOCKS5Upstreams[before.ID], s.protocolProbes[before.ID]
	binding, material := s.vlessBindings[f.bindingID], s.vlessRuntimeMaterials[f.materialID]
	addBrokenMemberGroup(t, s, f.nodeID, time.Now().UTC())
	// Fault injection creates and audits a separate device group. Capture the
	// transaction baseline only after that setup has completed.
	audits, retries := len(s.auditEvents), len(s.businessIdempotency)
	wake := s.DesiredConfigChanges(f.nodeID)
	defer s.ReleaseDesiredConfigChanges(f.nodeID, wake)
	request = boundPortRequest(before)
	request.VLESSSOCKS5Username, request.VLESSSOCKS5Password = upstream.Username, "rejected-test-password"
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "rejected-runtime-edit"); err == nil {
		t.Fatal("injected compiler failure did not reject the update")
	}
	current := s.nodeConfigsByNode[f.nodeID][s.nodes[f.nodeID].DesiredGeneration]
	for _, check := range []struct {
		name string
		same bool
	}{
		{"rule", reflect.DeepEqual(before, s.forwardRules[before.ID])},
		{"node config", reflect.DeepEqual(config, current)},
		{"upstream", upstream == s.vlessSOCKS5Upstreams[before.ID]},
		{"probe", probe == s.protocolProbes[before.ID]},
		{"proof", proof == s.protocolHealth[f.endpointID][f.nodeID]},
		{"binding", reflect.DeepEqual(binding, s.vlessBindings[f.bindingID])},
		{"material", reflect.DeepEqual(material, s.vlessRuntimeMaterials[f.materialID])},
		{"audit count", audits == len(s.auditEvents)},
		{"idempotency count", retries == len(s.businessIdempotency)},
	} {
		if !check.same {
			t.Errorf("failed update changed last-known-good %s", check.name)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	assertCompiledRuleReady(t, f, true)
	requireNoDesiredChange(t, wake)
	delete(s.deviceGroups, "broken-"+f.nodeID)
	delete(s.membersByGroup, "broken-"+f.nodeID)
	delete(s.revisionsByGroup, "broken-"+f.nodeID)
	if _, _, err := service.UpdateForAdministrator(ctx, "admin", before.ID, request, "rejected-runtime-edit"); err != nil {
		t.Fatal("failure consumed the idempotency key or management revision", err)
	}
}

func TestQuotaBoundaryEditsAdvanceRuntimeOnlyWhenForwardingChanges(t *testing.T) {
	f, _ := compiledRuleReadinessFixture(t)
	s, ctx := f.store, context.Background()
	service := forwarding.NewService(s, nil)
	rule := s.forwardRules[f.rule.ID]
	rule.TrafficUsedBytes = 200
	s.forwardRules[rule.ID] = rule
	initialRuntime, initialGeneration := rule.EffectiveRuntimeRevision(), s.nodes[f.nodeID].DesiredGeneration
	for index, limit := range []int64{1000, 100, 500} {
		request := boundPortRequest(rule)
		request.TrafficLimitBytes = limit
		updated, _, err := service.UpdateForAdministrator(ctx, "admin", rule.ID, request, []string{"quota-nonblocking", "quota-blocking", "quota-restored"}[index])
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if updated.EffectiveRuntimeRevision() != initialRuntime || s.nodes[f.nodeID].DesiredGeneration != initialGeneration {
				t.Fatal("nonblocking quota edit deployed runtime")
			}
			assertCompiledRuleReady(t, f, true)
		} else if updated.EffectiveRuntimeRevision() != updated.Revision || s.nodes[f.nodeID].DesiredGeneration <= initialGeneration {
			t.Fatal("quota boundary failed to change runtime")
		}
		initialGeneration = s.nodes[f.nodeID].DesiredGeneration
		rule = s.forwardRules[rule.ID]
	}
}

func TestRuntimeRevisionSnapshotLegacyCompatibilityAndValidation(t *testing.T) {
	f, _ := compiledRuleReadinessFixture(t)
	rule := f.store.forwardRules[f.rule.ID]
	rule.RuntimeRevision = 0
	f.store.forwardRules[rule.ID] = rule
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.Version = 25
	legacy, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(legacy)
	if err != nil {
		t.Fatal("legacy bundle/proof identity rejected", err)
	}
	ff := f
	ff.store = restored
	assertCompiledRuleReady(t, ff, true)
	for _, revision := range []int64{-1, rule.Revision + 1} {
		state.Version = SnapshotVersion
		invalid := rule
		invalid.RuntimeRevision = revision
		state.ForwardRules[rule.ID] = invalid
		bad, _ := json.Marshal(state)
		if _, err := DecodeSnapshot(bad); err == nil {
			t.Fatal("invalid runtime revision accepted")
		}
	}
	state.Version = 25
	rule.RuntimeRevision = rule.Revision
	state.ForwardRules[rule.ID] = rule
	bad, _ := json.Marshal(state)
	if _, err := DecodeSnapshot(bad); err == nil {
		t.Fatal("new runtime field disguised as legacy format")
	}
}
