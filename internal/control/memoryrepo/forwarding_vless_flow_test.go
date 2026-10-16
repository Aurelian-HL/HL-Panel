package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func TestExplicitEmptyFlowSurvivesLegacyEditReplayAndRestart(t *testing.T) {
	store, request, rule, _ := vlessCredentialEditFixture(t)
	ctx := context.Background()
	service := forwarding.NewService(store, nil)
	if rule.EffectiveVLESSFlow() != "xtls-rprx-vision" {
		t.Fatal("new rules lost historical Vision default")
	}
	request.VLESSFlow, request.VLESSFlowExplicit = "", true
	updated, replayed, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "set-empty-flow")
	if err != nil || replayed || !updated.VLESSFlowExplicit || updated.EffectiveVLESSFlow() != "" {
		t.Fatalf("explicit empty flow not saved: %v", err)
	}
	assertEmpty := func(item forwarding.Rule, err error) {
		t.Helper()
		if err != nil || !item.VLESSFlowExplicit || item.VLESSFlow != "" || item.EffectiveVLESSFlow() != "" {
			t.Fatalf("empty flow changed: %v", err)
		}
	}
	// Old clients used to force Vision on every save and have no explicit marker.
	request.Revision = updated.Revision
	request.VLESSFlow, request.VLESSFlowExplicit = "xtls-rprx-vision", false
	request.Description = "legacy client edit"
	legacy, _, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "legacy-flow-edit")
	assertEmpty(legacy, err)
	auditCount := len(store.AuditEvents())
	replay, replayed, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "legacy-flow-edit")
	assertEmpty(replay, err)
	if !replayed || replay.Revision != legacy.Revision || len(store.AuditEvents()) != auditCount {
		t.Fatal("flow edit replay duplicated revision or audit")
	}
	// CAS rejects a stale explicit request as well.
	request.VLESSFlowExplicit = true
	if _, _, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "stale-flow-edit"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("stale edit changed explicit flow")
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	old["Version"] = json.RawMessage("18")
	downgrade, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(downgrade); err == nil {
		t.Fatal("legacy snapshot accepted explicit flow")
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	assertEmpty(restored.forwardRules[rule.ID], nil)
	request.Revision = legacy.Revision
	request.VLESSFlow, request.VLESSFlowExplicit = "", false
	service = forwarding.NewService(restored, nil)
	afterRestart, _, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "restart-legacy-edit")
	assertEmpty(afterRestart, err)
	request.Revision = afterRestart.Revision
	request.VLESSFlow, request.VLESSFlowExplicit = "xtls-rprx-vision", true
	vision, _, err := service.UpdateForAdministrator(ctx, "admin-test", rule.ID, request, "explicit-vision-edit")
	if err != nil || vision.EffectiveVLESSFlow() != "xtls-rprx-vision" || !vision.VLESSFlowExplicit {
		t.Fatalf("explicit flow change rejected: %v", err)
	}
}
