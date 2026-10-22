package memoryrepo

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/licensing"
)

func TestForwardingProjectionAuthorizationAndIsolation(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	r := f.store.forwardRules[f.rule.ID]
	r.ImportedTraffic = forwarding.ImportedTraffic{SourceID: "synthetic-source", BatchID: "synthetic-batch", LineID: 1, ObservedAt: time.Now().UTC()}
	f.store.forwardRules[r.ID] = r
	view := f.store.ForwardingReadView()
	for _, actor := range []string{"admin", "unrelated", ""} {
		if got, want := view.state.importedRuleManagedByLocked(r, actor), f.store.importedRuleManagedByLocked(r, actor); got != want {
			t.Fatalf("imported authorization changed for %q", actor)
		}
	}
	// Break each provenance relationship independently. A cache must never turn
	// an arbitrary native listener into an administrator management grant.
	record := f.store.vlessBindings[f.bindingID]
	record.Binding.ImportedFrom.BatchID = "other-batch"
	f.store.vlessBindings[f.bindingID] = record
	fresh := f.store.ForwardingReadView()
	if fresh.state.importedRuleManagedByLocked(r, "admin") {
		t.Fatal("mismatched provenance granted access")
	}
	if !view.state.importedRuleManagedByLocked(r, "admin") {
		t.Fatal("immutable view changed with live store")
	}
	overlay := view.WithOverlays(1, nil, nil, nil)
	if !overlay.state.importedRuleManagedByLocked(r, "admin") {
		t.Fatal("overlay lost authorization")
	}
}

func TestForwardingProjectionDelegatedOwnership(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	r := f.rule
	r.OwnerKind = forwarding.OwnerCustomer
	r.OwnerID = r.CustomerID
	f.store.forwardRules[r.ID] = r
	raw, _ := json.Marshal(r)
	f.store.businessIdempotency["admin\x00forwarding.create\x00synthetic"] = businessMutation{ResourceID: r.ID, Result: raw}
	view := f.store.ForwardingReadView()
	for _, actor := range []string{"admin", "unrelated", ""} {
		if got, want := view.state.delegatedRuleManagedByLocked(r, actor), f.store.delegatedRuleManagedByLocked(r, actor); got != want {
			t.Fatalf("delegated authorization changed for %q", actor)
		}
	}
	r.CustomerID = "different-customer"
	f.store.forwardRules[r.ID] = r
	if f.store.ForwardingReadView().state.delegatedRuleManagedByLocked(r, "admin") {
		t.Fatal("stale creation record granted access")
	}
}

func TestSnapshotStandardAndImportedVersionCompatibility(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(legacyLoginSnapshot(t, raw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"20", "21", "22"} {
		doc["Version"] = json.RawMessage(version)
		delete(doc, "LicenseCodes")
		candidate, _ := json.Marshal(doc)
		restored, err := DecodeSnapshot(candidate)
		if version == "22" {
			if err == nil {
				t.Fatal("current snapshot accepted missing licensing map")
			}
			continue
		}
		if err != nil {
			t.Fatalf("legacy version %s rejected: %v", version, err)
		}
		after, err := restored.EncodeSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeSnapshot(after); err != nil {
			t.Fatal("upgraded snapshot invalid", err)
		}
	}
}

func TestSnapshotPreservesStandardLicensing(t *testing.T) {
	f, _ := nativeRuntimeFixture(t)
	item, _, _, err := licensing.NewService(f.store, nil).Issue(context.Background(), "admin", licensing.IssueInput{Domain: "panel.example.com", Months: 1}, "synthetic-license")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.licenseCodes[item.ID], f.store.licenseCodes[item.ID]) {
		t.Fatal("standard licensing data changed")
	}
}
