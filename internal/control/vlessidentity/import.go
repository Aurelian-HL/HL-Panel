package vlessidentity

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
)

// ImportRequest is an in-process maintenance contract. It is deliberately not
// an HTTP request: the ordinary provisioning endpoint never accepts an old UUID.
// Import requires an owned, paused rule and does not grant runtime readiness.
type ImportRequest struct {
	ProvisionRequest
	CredentialUUID string `json:"-"`
	SourceBatchID  string
	SourceLineID   int64
}

type ImportInput struct {
	ProvisionInput
	SourceBatchID string
	SourceLineID  int64
}

// ImportRepository checks pause, ownership, duplicate credentials, idempotency
// and references under the same transaction as the binding and audit write.
type ImportRepository interface {
	ImportExistingIdentity(context.Context, ImportInput, audit.Event) (Binding, bool, error)
}

func (service *Service) ImportExisting(ctx context.Context, administratorID string, request ImportRequest, key string) (Binding, bool, error) {
	// Unlike ordinary provision, importing never generates or normalizes a UUID.
	// Even insignificant-looking whitespace is rejected rather than rewritten.
	if !validImportedUUID(request.CredentialUUID) || !validIdentifier(administratorID) ||
		!validIdentifier(request.CustomerID) || !validIdentifier(request.ForwardingRuleID) ||
		!validIdentifier(request.EndpointPoolID) || !validIdentifier(request.SourceBatchID) ||
		request.SourceLineID <= 0 || request.Revision != 0 || !validIdempotencyKey(key) {
		return Binding{}, false, fmt.Errorf("%w: invalid VLESS identity import", faults.ErrValidation)
	}
	repository, ok := service.repository.(ImportRepository)
	if !ok {
		return Binding{}, false, errors.New("VLESS identity import repository unavailable")
	}
	id, err := idgen.New("vbind")
	if err != nil {
		return Binding{}, false, errors.New("VLESS identity import identifier unavailable")
	}
	now := service.now().UTC()
	binding := Binding{ID: id, CustomerID: request.CustomerID, ForwardingRuleID: request.ForwardingRuleID,
		EndpointPoolID: request.EndpointPoolID, State: StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
		ImportedFrom: &ImportOrigin{BatchID: request.SourceBatchID, LineID: request.SourceLineID}}
	event, err := audit.NewEvent(now, "local_operator", administratorID, "vless_identity.import", "vless_identity_binding", id, "succeeded", map[string]any{
		"source_batch_id": request.SourceBatchID, "source_line_id": request.SourceLineID,
		"customer_id": request.CustomerID, "forwarding_rule_id": request.ForwardingRuleID, "endpoint_pool_id": request.EndpointPoolID,
	})
	if err != nil {
		return Binding{}, false, errors.New("VLESS identity import audit unavailable")
	}
	return repository.ImportExistingIdentity(ctx, ImportInput{ProvisionInput: ProvisionInput{
		Record: CredentialRecord{Binding: binding, CredentialUUID: request.CredentialUUID}, ExpectedRevision: 0,
		IdempotencyKey: key, RequestSHA256: digest("import", request.SourceBatchID, fmt.Sprint(request.SourceLineID), request.CustomerID, request.ForwardingRuleID, request.EndpointPoolID, request.CredentialUUID), CreatedBy: administratorID,
	}, SourceBatchID: request.SourceBatchID, SourceLineID: request.SourceLineID}, event)
}

func validImportedUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(raw) == 16
}

// ValidateImportInput also protects repositories used without the service.
func ValidateImportInput(input ImportInput) bool {
	origin := input.Record.Binding.ImportedFrom
	return validImportedUUID(input.Record.CredentialUUID) && validIdentifier(input.SourceBatchID) && input.SourceLineID > 0 && origin != nil && origin.BatchID == input.SourceBatchID && origin.LineID == input.SourceLineID
}

func ValidImportOrigin(origin *ImportOrigin) bool {
	return origin == nil || validIdentifier(origin.BatchID) && origin.LineID > 0
}
