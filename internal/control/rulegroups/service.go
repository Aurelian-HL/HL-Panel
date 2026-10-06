package rulegroups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/idgen"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) List(ctx context.Context) ([]RuleGroup, error) {
	items, err := s.repository.ListRuleGroups(ctx)
	if items == nil && err == nil {
		items = []RuleGroup{}
	}
	return items, err
}

func (s *Service) ListForAdministrator(ctx context.Context, adminID string) ([]RuleGroup, error) {
	items, err := s.repository.ListRuleGroupsForAdministrator(ctx, adminID)
	if items == nil && err == nil {
		items = []RuleGroup{}
	}
	return items, err
}

func (s *Service) Get(ctx context.Context, id string) (RuleGroup, error) {
	id = strings.TrimSpace(id)
	if !validReference(id) {
		return RuleGroup{}, fmt.Errorf("%w: invalid rule group id", faults.ErrValidation)
	}
	return s.repository.RuleGroup(ctx, id)
}

func (s *Service) GetForAdministrator(ctx context.Context, adminID, id string) (RuleGroup, error) {
	id = strings.TrimSpace(id)
	if !validReference(id) {
		return RuleGroup{}, fmt.Errorf("%w: invalid rule group id", faults.ErrValidation)
	}
	return s.repository.RuleGroupForAdministrator(ctx, adminID, id)
}

func (s *Service) Create(ctx context.Context, adminID string, request Request, idempotencyKey string) (RuleGroup, bool, error) {
	request, err := NormalizeRequest(request)
	if err != nil {
		return RuleGroup{}, false, err
	}
	if request.Revision != 0 {
		return RuleGroup{}, false, fmt.Errorf("%w: revision must be zero for creation", faults.ErrValidation)
	}
	id, err := idgen.New("rgrp")
	if err != nil {
		return RuleGroup{}, false, err
	}
	now := s.now().UTC()
	item := RuleGroup{ID: id, OwnerAdministratorID: adminID, Name: request.Name, Description: request.Description, Revision: 1, CreatedAt: now, UpdatedAt: now}
	return s.save(ctx, adminID, "", request, item, idempotencyKey, "rule_group.create")
}

func (s *Service) Update(ctx context.Context, adminID, id string, request Request, idempotencyKey string) (RuleGroup, bool, error) {
	id = strings.TrimSpace(id)
	if !validReference(id) {
		return RuleGroup{}, false, fmt.Errorf("%w: invalid rule group id", faults.ErrValidation)
	}
	request, err := NormalizeRequest(request)
	if err != nil {
		return RuleGroup{}, false, err
	}
	if request.Revision < 1 || request.Revision == int64(^uint64(0)>>1) {
		return RuleGroup{}, false, fmt.Errorf("%w: revision is required and must be incrementable", faults.ErrValidation)
	}
	previous, err := s.repository.RuleGroupForAdministrator(ctx, adminID, id)
	if err != nil {
		return RuleGroup{}, false, err
	}
	item := RuleGroup{ID: id, OwnerAdministratorID: adminID, Name: request.Name, Description: request.Description, Revision: request.Revision + 1, CreatedAt: previous.CreatedAt, UpdatedAt: s.now().UTC()}
	return s.save(ctx, adminID, id, request, item, idempotencyKey, "rule_group.update")
}

func (s *Service) save(ctx context.Context, adminID, requestID string, request Request, item RuleGroup, idempotencyKey, action string) (RuleGroup, bool, error) {
	key, digest, err := mutationIdentity(idempotencyKey, struct {
		ID      string  `json:"id"`
		Request Request `json:"request"`
	}{requestID, request})
	if err != nil {
		return RuleGroup{}, false, err
	}
	event, err := audit.NewEvent(item.UpdatedAt, "administrator", adminID, action, "rule_group", item.ID, "succeeded", map[string]any{"name": item.Name, "revision": item.Revision})
	if err != nil {
		return RuleGroup{}, false, err
	}
	return s.repository.SaveRuleGroup(ctx, SaveInput{RuleGroup: item, ExpectedRevision: request.Revision, IdempotencyKey: key, RequestSHA256: digest, CreatedBy: adminID}, event)
}

func (s *Service) Batch(ctx context.Context, adminID string, request BatchRequest, idempotencyKey string) (BatchResult, bool, error) {
	return s.batch(ctx, "administrator", adminID, "", request, idempotencyKey)
}

func (s *Service) BatchForAdministrator(ctx context.Context, adminID string, request BatchRequest, idempotencyKey string) (BatchResult, bool, error) {
	return s.batch(ctx, "administrator", adminID, forwarding.AdministratorSubjectID(adminID), request, idempotencyKey)
}

func (s *Service) BatchForCustomer(ctx context.Context, customerID string, request BatchRequest, idempotencyKey string) (BatchResult, bool, error) {
	if request.Operation != BatchPause && request.Operation != BatchResume && request.Operation != BatchDelete {
		return BatchResult{}, false, fmt.Errorf("%w: customer operation must be pause, resume or delete", faults.ErrValidation)
	}
	return s.batch(ctx, "customer", customerID, customerID, request, idempotencyKey)
}

func (s *Service) batch(ctx context.Context, actorType, actorID, expectedCustomerID string, request BatchRequest, idempotencyKey string) (BatchResult, bool, error) {
	request, err := NormalizeBatchRequest(request)
	if err != nil {
		return BatchResult{}, false, err
	}
	key, digest, err := mutationIdentity(idempotencyKey, request)
	if err != nil {
		return BatchResult{}, false, err
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, actorType, actorID, "forwarding_rule.batch_"+string(request.Operation), "forwarding_rule_batch", key, "succeeded", map[string]any{"operation": request.Operation, "rule_count": len(request.RuleIDs), "rule_group_id": request.RuleGroupID})
	if err != nil {
		return BatchResult{}, false, err
	}
	createdBy := actorID
	if actorType == "customer" {
		createdBy = "customer:" + actorID
	} else if expectedCustomerID != "" {
		createdBy = "admin-rules:" + actorID
	}
	return s.repository.BatchUpdateRules(ctx, BatchInput{Request: request, ExpectedCustomerID: expectedCustomerID, UpdatedAt: now, IdempotencyKey: key, RequestSHA256: digest, CreatedBy: createdBy}, event)
}

func mutationIdentity(idempotencyKey string, value any) (string, string, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validReference(idempotencyKey) {
		return "", "", fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(raw)
	return idempotencyKey, hex.EncodeToString(digest[:]), nil
}
