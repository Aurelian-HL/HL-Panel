package forwarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/idgen"
)

type BatchCreateRequest struct {
	Rules []Request `json:"rules"`
}
type BatchCreateResult struct {
	Created int    `json:"created"`
	Items   []Rule `json:"items"`
}

// Private material stays in the repository input, never in results or audit.
type BatchCreateInput struct {
	Rules          []BatchCreateRule
	CreatedBy      string
	IdempotencyKey string
	RequestSHA256  string
}

type BatchCreateRule struct {
	CreateInput
	Credential vlessidentity.CredentialRecord
}

type BatchCreateRepository interface {
	CreateForwardingRules(context.Context, BatchCreateInput, audit.Event) (BatchCreateResult, bool, error)
}

func (s *Service) BatchCreateForAdministrator(ctx context.Context, adminID string, input BatchCreateRequest, key string) (BatchCreateResult, bool, error) {
	if !validReference(adminID) {
		return BatchCreateResult{}, false, faults.ErrValidation
	}
	key = strings.TrimSpace(key)
	if !validReference(key) {
		return BatchCreateResult{}, false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	if len(input.Rules) == 0 || len(input.Rules) > MaxImportRules {
		return BatchCreateResult{}, false, fmt.Errorf("%w: 每批请创建 1 至 %d 条规则", faults.ErrValidation, MaxImportRules)
	}
	repository, ok := s.repository.(BatchCreateRepository)
	if !ok {
		return BatchCreateResult{}, false, fmt.Errorf("%w: batch creation is unavailable", faults.ErrConflict)
	}
	requests := make([]Request, len(input.Rules))
	for i, request := range input.Rules {
		request.CustomerID = AdministratorSubjectID(adminID)
		request.OwnerKind, request.OwnerID = OwnerAdministrator, adminID
		if strings.TrimSpace(request.Name) == "" {
			request.Name = strings.TrimSpace(request.VLESSSOCKS5Host)
		}
		normalized, err := NormalizeRequest(request)
		if err != nil {
			return BatchCreateResult{}, false, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		if normalized.Revision != 0 || normalized.ListenPort != 0 || normalized.EffectiveIngressProtocol() != IngressVLESSReality || normalized.VLESSOutboundMode != VLESSOutboundSOCKS5 || normalized.EgressMode != EgressDirect || normalized.RealityPublicKey != "" || normalized.RealityShortID != "" {
			return BatchCreateResult{}, false, fmt.Errorf("%w: 第 %d 条需使用新建 VLESS + Reality / SOCKS5 规则，自动分配端口和密钥", faults.ErrValidation, i+1)
		}
		if normalized.VLESSSOCKS5Username == "" || normalized.VLESSSOCKS5Password == "" {
			return BatchCreateResult{}, false, fmt.Errorf("%w: 第 %d 条需填写完整 SK5 账号和密码", faults.ErrValidation, i+1)
		}
		requests[i] = normalized
	}
	canonical, err := json.Marshal(requests)
	if err != nil {
		return BatchCreateResult{}, false, err
	}
	digest := sha256.Sum256(canonical)
	prepared := make([]BatchCreateRule, len(requests))
	now := s.now().UTC()
	for i, request := range requests {
		if err := s.selectCreationRealityTarget(&request); err != nil {
			return BatchCreateResult{}, false, err
		}
		privateKey, err := s.fillAutomaticReality(&request)
		if err != nil {
			return BatchCreateResult{}, false, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		if privateKey == "" {
			return BatchCreateResult{}, false, fmt.Errorf("%w: Reality target is not configured", faults.ErrValidation)
		}
		id, err := idgen.New("fwd")
		if err != nil {
			return BatchCreateResult{}, false, err
		}
		rule := request.rule(id, now)
		rule.Revision = 1
		poolID, err := idgen.New("epool")
		if err != nil {
			return BatchCreateResult{}, false, err
		}
		credential, err := vlessidentity.PrepareCredential(rule.CustomerID, id, poolID, now, nil)
		if err != nil {
			return BatchCreateResult{}, false, err
		}
		prepared[i] = BatchCreateRule{CreateInput: CreateInput{Rule: rule, ProtocolProbeEchoPort: s.probeEchoPort, RealityPrivateKey: privateKey, VLESSSOCKS5Password: request.VLESSSOCKS5Password, AutoReality: true}, Credential: credential}
	}
	batchID, err := idgen.New("batch")
	if err != nil {
		return BatchCreateResult{}, false, err
	}
	event, err := audit.NewEvent(now, "administrator", adminID, "forwarding_rule.batch_create", "forwarding_rule_batch", batchID, "succeeded", map[string]any{"rule_count": len(prepared)})
	if err != nil {
		return BatchCreateResult{}, false, err
	}
	return repository.CreateForwardingRules(ctx, BatchCreateInput{Rules: prepared, CreatedBy: "admin-rules:" + adminID, IdempotencyKey: key, RequestSHA256: hex.EncodeToString(digest[:])}, event)
}
