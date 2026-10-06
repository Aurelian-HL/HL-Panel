package vlessruntime

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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
}

func NewService(repository Repository, now func() time.Time) (*Service, error) {
	if repository == nil {
		return nil, errors.New("VLESS runtime material repository is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}, nil
}

// Bind attaches one node to an existing VLESS identity binding. The UUID is
// resolved by the repository from that binding; callers only provide the
// node-local Reality private key. The returned value is public metadata only.
func (s *Service) Bind(ctx context.Context, administratorID, bindingID, nodeID, realityPrivateKey string, expectedRevision int64, idempotencyKey string) (PublicMaterial, bool, error) {
	administratorID, bindingID, nodeID, idempotencyKey = strings.TrimSpace(administratorID), strings.TrimSpace(bindingID), strings.TrimSpace(nodeID), strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(bindingID) || !validIdentifier(nodeID) || !validIdempotencyKey(idempotencyKey) {
		return PublicMaterial{}, false, fmt.Errorf("%w: administrator, binding, node and Idempotency-Key are required", faults.ErrValidation)
	}
	if expectedRevision != 0 {
		return PublicMaterial{}, false, fmt.Errorf("%w: initial runtime material revision must be zero", faults.ErrValidation)
	}
	if !validRealityPrivateKey(realityPrivateKey) {
		return PublicMaterial{}, false, fmt.Errorf("%w: Reality private key is invalid", faults.ErrValidation)
	}
	now := s.now().UTC()
	id, err := idgen.New("vrm")
	if err != nil {
		return PublicMaterial{}, false, err
	}
	requestHash := digest("bind", bindingID, nodeID, realityPrivateKey, fmt.Sprint(expectedRevision))
	event, err := audit.NewEvent(now, "administrator", administratorID, "vless_runtime.bind", "vless_runtime_material", id, "succeeded", map[string]any{
		"binding_id": bindingID, "node_id": nodeID, "revision": 1,
	})
	if err != nil {
		return PublicMaterial{}, false, err
	}
	return s.repository.SaveRuntimeMaterial(ctx, SaveInput{
		ID: id, BindingID: bindingID, NodeID: nodeID, RealityPrivateKey: realityPrivateKey,
		ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey,
		RequestSHA256: requestHash, CreatedBy: administratorID, CreatedAt: now,
	}, event)
}

func (s *Service) List(ctx context.Context, administratorID string) ([]PublicMaterial, error) {
	administratorID = strings.TrimSpace(administratorID)
	if !validIdentifier(administratorID) {
		return nil, fmt.Errorf("%w: administrator is required", faults.ErrValidation)
	}
	return s.repository.ListPublicRuntimeMaterialsForAdministrator(ctx, administratorID)
}

func (s *Service) Revoke(ctx context.Context, administratorID, materialID string, expectedRevision int64, idempotencyKey string) (PublicMaterial, bool, error) {
	administratorID, materialID, idempotencyKey = strings.TrimSpace(administratorID), strings.TrimSpace(materialID), strings.TrimSpace(idempotencyKey)
	if !validIdentifier(administratorID) || !validIdentifier(materialID) || !validRevision(expectedRevision) || !validIdempotencyKey(idempotencyKey) {
		return PublicMaterial{}, false, fmt.Errorf("%w: administrator, material, positive revision and Idempotency-Key are required", faults.ErrValidation)
	}
	now := s.now().UTC()
	requestHash := digest("revoke", materialID, fmt.Sprint(expectedRevision))
	event, err := audit.NewEvent(now, "administrator", administratorID, "vless_runtime.revoke", "vless_runtime_material", materialID, "succeeded", map[string]any{
		"revision": expectedRevision + 1,
	})
	if err != nil {
		return PublicMaterial{}, false, err
	}
	return s.repository.RevokeRuntimeMaterial(ctx, RevokeInput{
		ID: materialID, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey,
		RequestSHA256: requestHash, UpdatedBy: administratorID, UpdatedAt: now,
	}, event)
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validIdempotencyKey(value string) bool { return validIdentifier(value) }

func validRevision(value int64) bool { return value > 0 }

func validRealityPrivateKey(value string) bool {
	value = strings.TrimSpace(value)
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

// ValidCredentialUUID validates the canonical UUID form used by Xray VLESS
// clients. It does not expose or log the value.
func ValidCredentialUUID(value string) bool { return validUUID(strings.TrimSpace(value)) }

// ValidRealityPrivateKey validates an unpadded base64url X25519 private key.
func ValidRealityPrivateKey(value string) bool { return validRealityPrivateKey(value) }

// RealityPublicKey derives the X25519 public key from a private key without
// retaining either value. It is used to prove that node material matches the
// public key already advertised by a forwarding rule.
func RealityPublicKey(privateKey string) (string, error) {
	privateKey = strings.TrimSpace(privateKey)
	raw, err := base64.RawURLEncoding.DecodeString(privateKey)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("%w: Reality private key is invalid", faults.ErrValidation)
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("%w: Reality private key is invalid", faults.ErrValidation)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// RealityKeyMatchesPublicKey returns false for malformed material or a
// mismatched public key. It is safe for callers to use in validation errors;
// no secret is included in the result.
func RealityKeyMatchesPublicKey(privateKey, publicKey string) bool {
	if !validRealityPrivateKey(privateKey) {
		return false
	}
	derived, err := RealityPublicKey(privateKey)
	if err != nil {
		return false
	}
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(strings.TrimSpace(publicKey), "="))
	if decodeErr != nil || len(decoded) != 32 {
		return false
	}
	return derived == base64.RawURLEncoding.EncodeToString(decoded)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16
}

func digest(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
