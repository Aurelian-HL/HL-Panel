package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func legacyTransfer(request forwarding.Request, content string) forwarding.TransferRequest {
	return forwarding.TransferRequest{Format: forwarding.ImportFormatNYText, Content: content, Mapping: forwarding.ImportMapping{
		CustomerID: request.CustomerID, EntryGroupID: request.EntryGroupID, EgressMode: request.EgressMode,
		Protocol: request.Protocol, SelectionPolicy: request.SelectionPolicy,
	}}
}

func TestForwardingImportIsAtomicIdempotentAndAudited(t *testing.T) {
	store, base := forwardingRepositoryFixture(t)
	service := forwarding.NewService(store, nil)
	ctx := context.Background()
	conflicting := legacyTransfer(base, "one#12001#one.example.test#443\ntwo#12001#two.example.test#443")
	preview, err := service.PreviewImport(ctx, conflicting)
	if err != nil || preview.Valid != 1 || preview.Invalid != 1 || preview.Rows[1].Issues[0].Code != "port_conflict" {
		t.Fatalf("preview did not report the collision: %+v %v", preview, err)
	}
	if _, _, err := service.Import(ctx, "admin", conflicting, "atomic-conflict"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("conflicting import did not fail atomically: %v", err)
	}
	items, _ := service.List(ctx)
	if len(items) != 0 || len(store.AuditEvents()) != 0 {
		t.Fatal("failed import partially wrote rules or audit events")
	}

	valid := legacyTransfer(base, "one##one.example.test#443\ntwo##two.example.test#443")
	result, replayed, err := service.Import(ctx, "admin", valid, "valid-import")
	if err != nil || replayed || result.Created != 2 || result.Updated != 0 || result.Items[0].ListenPort == result.Items[1].ListenPort {
		t.Fatalf("valid import failed: %+v %v", result, err)
	}
	auditCount := len(store.AuditEvents())
	replayedResult, replayed, err := service.Import(ctx, "admin", valid, "valid-import")
	if err != nil || !replayed || len(replayedResult.Items) != 2 || len(store.AuditEvents()) != auditCount {
		t.Fatal("idempotent replay created rules or duplicated audit")
	}
	changed := valid
	changed.Content += "\nthree##three.example.test#443"
	if _, _, err := service.Import(ctx, "admin", changed, "valid-import"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("changed replay content accepted: %v", err)
	}
}

func TestForwardingJSONUpsertUsesRevisionAndPreservesAdvancedFields(t *testing.T) {
	store, base := forwardingRepositoryFixture(t)
	service := forwarding.NewService(store, nil)
	ctx := context.Background()
	base.SelectionPolicy = forwarding.SelectionFailover
	base.AcceptProxyProtocol = true
	base.SendProxyProtocol = forwarding.SendProxyV2TCP
	base.SpeedLimitMbps = 50
	base.IPLimit = 3
	base.ConnectionLimit = 8
	created, _, err := service.Create(ctx, "admin", base, "seed")
	if err != nil {
		t.Fatal(err)
	}
	document, err := service.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	document.Rules[0].Rule.Name = "updated-by-import"
	raw, _ := json.Marshal(document)
	input := forwarding.TransferRequest{Format: forwarding.ImportFormatJSONV1, Content: string(raw)}
	result, _, err := service.Import(ctx, "admin", input, "json-update")
	if err != nil || result.Updated != 1 || result.Items[0].Revision != created.Revision+1 || result.Items[0].SelectionPolicy != forwarding.SelectionFailover || result.Items[0].SpeedLimitMbps != 50 {
		t.Fatalf("JSON upsert failed: %+v %v", result, err)
	}
	if _, _, err := service.Import(ctx, "admin", input, "stale-json-update"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("stale revision was accepted: %v", err)
	}
	stored, _ := service.Get(ctx, created.ID)
	if stored.Revision != 2 || stored.Name != "updated-by-import" {
		t.Fatalf("stale retry changed stored rule: %+v", stored)
	}
}

func TestForwardingImportCannotTakeOverAnotherOwnersRule(t *testing.T) {
	store, base := forwardingRepositoryFixture(t)
	service := forwarding.NewService(store, nil)
	ctx := context.Background()
	created, _, err := service.Create(ctx, "admin", base, "seed-owner")
	if err != nil {
		t.Fatal(err)
	}
	input := forwarding.TransferRequest{Format: forwarding.ImportFormatJSONV1}
	stolen := base
	stolen.CustomerID = forwarding.AdministratorSubjectID("admin-test")
	stolen.OwnerKind = forwarding.OwnerAdministrator
	stolen.OwnerID = "admin-test"
	stolen.Revision = created.Revision
	stolen.Name = "taken-over"
	content, err := json.Marshal(forwarding.ExportDocument{
		SchemaVersion: forwarding.ExportSchemaV1,
		Rules:         []forwarding.ExportRule{{Operation: forwarding.ImportUpsert, ID: created.ID, Rule: stolen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Content = string(content)
	preview, err := service.PreviewImportForAdministrator(ctx, "admin-test", input)
	if err != nil || preview.Invalid != 1 || preview.Rows[0].Issues[0].Code != "missing_resource" {
		t.Fatalf("cross-owner import preview was accepted: %+v %v", preview, err)
	}
	if _, _, err := service.ImportForAdministrator(ctx, "admin-test", input, "takeover"); !errors.Is(err, faults.ErrConflict) {
		t.Fatalf("cross-owner import was accepted: %v", err)
	}
	stored, err := service.Get(ctx, created.ID)
	if err != nil || stored.Name != created.Name || stored.CustomerID != created.CustomerID {
		t.Fatalf("cross-owner import changed the rule: %+v %v", stored, err)
	}
}

func TestAdministratorImportPreviewCommitAndExportUseSessionOwner(t *testing.T) {
	store, base := forwardingRepositoryFixture(t)
	service := forwarding.NewService(store, nil)
	ctx := context.Background()
	legacy, _, err := service.Create(ctx, "admin-test", base, "legacy-customer-rule")
	if err != nil {
		t.Fatal(err)
	}
	input := legacyTransfer(base, "admin-line##new.example.test#443")
	input.Mapping.CustomerID = legacy.CustomerID
	preview, err := service.PreviewImportForAdministrator(ctx, "admin-test", input)
	if err != nil || preview.Invalid != 0 || preview.Valid != 1 || preview.Rows[0].Request.CustomerID != forwarding.AdministratorSubjectID("admin-test") || preview.Rows[0].Request.OwnerID != "admin-test" {
		t.Fatalf("preview did not bind session owner: %+v %v", preview, err)
	}
	result, replayed, err := service.ImportForAdministrator(ctx, "admin-test", input, "own-import")
	if err != nil || replayed || result.Created != 1 || !result.Items[0].OwnedByAdministrator("admin-test") {
		t.Fatalf("import did not bind session owner: %+v %v", result, err)
	}
	exported, err := service.ExportForAdministrator(ctx, "admin-test")
	if err != nil || len(exported.Rules) != 1 || exported.Rules[0].ID != result.Items[0].ID {
		t.Fatalf("export leaked another owner's rule: %+v %v", exported, err)
	}
}
