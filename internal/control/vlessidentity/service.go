package vlessidentity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
)

type Service struct {
	repository Repository
	now        func() time.Time
	random     io.Reader
}

func NewService(repository Repository, now func() time.Time, random io.Reader) (*Service, error) {
	if repository == nil {
		return nil, errors.New("VLESS identity repository is required")
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &Service{repository: repository, now: now, random: random}, nil
}

func (service *Service) Provision(ctx context.Context, administratorID string, request ProvisionRequest, idempotencyKey string) (Binding, bool, error) {
	request.CustomerID = strings.TrimSpace(request.CustomerID)
	request.ForwardingRuleID = strings.TrimSpace(request.ForwardingRuleID)
	request.EndpointPoolID = strings.TrimSpace(request.EndpointPoolID)
	administratorID = strings.TrimSpace(administratorID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(request.CustomerID) || !validIdentifier(request.ForwardingRuleID) || !validIdentifier(request.EndpointPoolID) {
		return Binding{}, false, fmt.Errorf("%w: administrator, customer, forwarding rule, and endpoint pool are required", faults.ErrValidation)
	}
	if request.Revision != 0 {
		return Binding{}, false, fmt.Errorf("%w: initial revision must be zero", faults.ErrValidation)
	}
	if !validIdempotencyKey(idempotencyKey) {
		return Binding{}, false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	uuid, err := canonicalUUID(service.random)
	if err != nil {
		return Binding{}, false, errors.New("generate VLESS identity")
	}
	bindingID, err := idgen.New("vbind")
	if err != nil {
		return Binding{}, false, err
	}
	now := service.now().UTC()
	binding := Binding{
		ID: bindingID, CustomerID: request.CustomerID, ForwardingRuleID: request.ForwardingRuleID,
		EndpointPoolID: request.EndpointPoolID, State: StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	event, err := audit.NewEvent(now, "administrator", administratorID, "vless_identity.provision", "vless_identity_binding", binding.ID, "succeeded", map[string]any{
		"customer_id": request.CustomerID, "forwarding_rule_id": request.ForwardingRuleID,
		"endpoint_pool_id": request.EndpointPoolID, "revision": binding.Revision,
	})
	if err != nil {
		return Binding{}, false, err
	}
	result, replayed, err := service.repository.Provision(ctx, ProvisionInput{
		Record: CredentialRecord{Binding: binding, CredentialUUID: uuid}, ExpectedRevision: request.Revision,
		IdempotencyKey: idempotencyKey, RequestSHA256: digest("provision", request.CustomerID, request.ForwardingRuleID, request.EndpointPoolID, "0"), CreatedBy: administratorID,
	}, event)
	return result, replayed, err
}

func (service *Service) List(ctx context.Context, administratorID string) ([]Binding, error) {
	administratorID = strings.TrimSpace(administratorID)
	if !validIdentifier(administratorID) {
		return nil, fmt.Errorf("%w: administrator is required", faults.ErrValidation)
	}
	return service.repository.ListForAdministrator(ctx, administratorID)
}

func (service *Service) CredentialForAdministrator(ctx context.Context, administratorID, ruleID string) (CredentialRecord, error) {
	administratorID = strings.TrimSpace(administratorID)
	ruleID = strings.TrimSpace(ruleID)
	if !validIdentifier(administratorID) || !validIdentifier(ruleID) {
		return CredentialRecord{}, fmt.Errorf("%w: administrator and rule are required", faults.ErrValidation)
	}
	reader, ok := service.repository.(CredentialReader)
	if !ok {
		return CredentialRecord{}, faults.ErrNotFound
	}
	record, err := reader.CredentialForAdministrator(ctx, administratorID, ruleID)
	if err != nil {
		return CredentialRecord{}, err
	}
	if record.Binding.State != StateActive || record.CredentialUUID == "" {
		return CredentialRecord{}, faults.ErrNotFound
	}
	return record, nil
}

func (service *Service) Rotate(ctx context.Context, administratorID, bindingID string, request MutationRequest, idempotencyKey string) (Binding, bool, error) {
	administratorID, bindingID, idempotencyKey = strings.TrimSpace(administratorID), strings.TrimSpace(bindingID), strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(bindingID) || !validRevision(request.Revision) || !validIdempotencyKey(idempotencyKey) {
		return Binding{}, false, fmt.Errorf("%w: binding, positive revision, administrator, and Idempotency-Key are required", faults.ErrValidation)
	}
	uuid, err := canonicalUUID(service.random)
	if err != nil {
		return Binding{}, false, errors.New("generate VLESS identity")
	}
	now := service.now().UTC()
	event, err := audit.NewEvent(now, "administrator", administratorID, "vless_identity.rotate", "vless_identity_binding", bindingID, "succeeded", map[string]any{"revision": request.Revision + 1})
	if err != nil {
		return Binding{}, false, err
	}
	return service.repository.Rotate(ctx, RotateInput{
		BindingID: bindingID, CredentialUUID: uuid, ExpectedRevision: request.Revision,
		IdempotencyKey: idempotencyKey, RequestSHA256: digest("rotate", bindingID, fmt.Sprint(request.Revision)), UpdatedBy: administratorID, UpdatedAt: now,
	}, event)
}

func (service *Service) Revoke(ctx context.Context, administratorID, bindingID string, request MutationRequest, idempotencyKey string) (Binding, bool, error) {
	administratorID, bindingID, idempotencyKey = strings.TrimSpace(administratorID), strings.TrimSpace(bindingID), strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(bindingID) || !validRevision(request.Revision) || !validIdempotencyKey(idempotencyKey) {
		return Binding{}, false, fmt.Errorf("%w: binding, positive revision, administrator, and Idempotency-Key are required", faults.ErrValidation)
	}
	now := service.now().UTC()
	event, err := audit.NewEvent(now, "administrator", administratorID, "vless_identity.revoke", "vless_identity_binding", bindingID, "succeeded", map[string]any{"revision": request.Revision + 1})
	if err != nil {
		return Binding{}, false, err
	}
	return service.repository.Revoke(ctx, RevokeInput{
		BindingID: bindingID, ExpectedRevision: request.Revision, IdempotencyKey: idempotencyKey,
		RequestSHA256: digest("revoke", bindingID, fmt.Sprint(request.Revision)), UpdatedBy: administratorID, UpdatedAt: now,
	}, event)
}

func canonicalUUID(random io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func digest(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validIdempotencyKey(value string) bool { return validIdentifier(value) }

func validRevision(revision int64) bool { return revision > 0 && revision < math.MaxInt64 }
