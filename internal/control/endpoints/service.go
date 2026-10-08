package endpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

type Service struct {
	repository      Repository
	now             func() time.Time
	readyCandidates ReadyCandidateSource
}

// ReadyCandidateSource reports candidates authorized for gateway selection.
// A local TCP reachability veto may further reduce the selectable set.
type ReadyCandidateSource interface {
	ReadyCandidateCount(context.Context, string) (int, error)
}

// DefaultHealthTTL limits the age of a successful protocol-forwarding health
// observation. It is intentionally independent from agent heartbeat liveness,
// even though both currently use the same duration.
const DefaultHealthTTL = 90 * time.Second

func (s *Service) Create(ctx context.Context, adminID, name, groupID string, mode Mode, protocol, hostname string, port int, policy SelectionPolicy, idempotencyKey string) (EndpointPool, bool, error) {
	return s.CreateBound(ctx, adminID, name, groupID, "", mode, protocol, hostname, port, policy, idempotencyKey)
}

func (s *Service) CreateBound(ctx context.Context, adminID, name, groupID, ruleID string, mode Mode, protocol, hostname string, port int, policy SelectionPolicy, idempotencyKey string) (EndpointPool, bool, error) {
	name = strings.TrimSpace(name)
	groupID = strings.TrimSpace(groupID)
	ruleID = strings.TrimSpace(ruleID)
	canonicalHost, err := serviceaddress.NormalizeHost(hostname)
	if err != nil {
		return EndpointPool{}, false, fmt.Errorf("%w: %s", faults.ErrValidation, err)
	}
	hostname = canonicalHost
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if name == "" || len(name) > 128 || groupID == "" || hostname == "" || len(hostname) > 253 {
		return EndpointPool{}, false, fmt.Errorf("%w: endpoint name, group_id, and hostname are required and bounded", faults.ErrValidation)
	}
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return EndpointPool{}, false, fmt.Errorf("%w: idempotency_key must contain 1 to 128 characters", faults.ErrValidation)
	}
	if !mode.Valid() || (protocol != "vless" && protocol != "tcp" && protocol != "socks5") || port < 1 || port > 65535 || !ValidSelectionPolicy(policy) {
		return EndpointPool{}, false, fmt.Errorf("%w: invalid endpoint mode, protocol, port, or selection_policy", faults.ErrValidation)
	}
	now := s.now().UTC()
	requestHash := hashRequest(name, groupID, mode, protocol, hostname, port, policy, ruleID)
	id, err := idgen.New("epool")
	if err != nil {
		return EndpointPool{}, false, err
	}
	event, err := audit.NewEvent(now, "administrator", adminID, "endpoint_pool.create", "endpoint_pool", id, "succeeded", map[string]any{
		"group_id": groupID,
		"rule_id":  ruleID,
		"protocol": protocol,
		"hostname": hostname,
		"port":     port,
	})
	if err != nil {
		return EndpointPool{}, false, err
	}
	pool, replayed, err := s.repository.CreateEndpointPool(ctx, CreatePoolInput{
		ID: id, Name: name, GroupID: groupID, RuleID: ruleID, Mode: mode, Protocol: protocol,
		Hostname: hostname, Port: port, SelectionPolicy: policy,
		IdempotencyKey: idempotencyKey, RequestSHA256: requestHash,
		CreatedBy: adminID, CreatedAt: now,
	}, event)
	return pool, replayed, err
}

func (s *Service) ListPools(ctx context.Context) ([]EndpointPool, error) {
	items, err := s.repository.ListEndpointPools(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		// No gateway readiness source means there is no verified protocol
		// health. Never infer it from member timestamps.
		items[index].HealthyCandidateCount = 0
		if s.readyCandidates != nil {
			count, err := s.readyCandidates.ReadyCandidateCount(ctx, items[index].ID)
			if err != nil {
				return nil, err
			}
			items[index].HealthyCandidateCount = count
		}
	}
	return items, nil
}

// ListPoolsForAdministrator only exposes pools created by the current actor.
// Legacy pools without an owner are intentionally not attributed implicitly.
func (s *Service) ListPoolsForAdministrator(ctx context.Context, administratorID string) ([]EndpointPool, error) {
	if !validAdministratorID(administratorID) {
		return nil, fmt.Errorf("%w: administrator is required", faults.ErrValidation)
	}
	items, err := s.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]EndpointPool, 0, len(items))
	for _, item := range items {
		if item.OwnerID == administratorID {
			visible = append(visible, item)
		}
	}
	return visible, nil
}

func validAdministratorID(id string) bool {
	return strings.TrimSpace(id) != "" && id == strings.TrimSpace(id)
}

// GetPoolForAdministrator reads one owned pool without probing unrelated pools.
func (s *Service) GetPoolForAdministrator(ctx context.Context, administratorID, poolID string) (EndpointPool, error) {
	if !validAdministratorID(administratorID) {
		return EndpointPool{}, fmt.Errorf("%w: administrator is required", faults.ErrValidation)
	}
	pool, err := s.repository.EndpointPool(ctx, poolID)
	if err != nil {
		return EndpointPool{}, err
	}
	if pool.OwnerID != administratorID {
		return EndpointPool{}, faults.ErrNotFound
	}
	return pool, nil
}

func (s *Service) AddMember(ctx context.Context, adminID, poolID, nodeID string, weight, priority int, idempotencyKey string) (EndpointPoolMember, bool, error) {
	poolID = strings.TrimSpace(poolID)
	nodeID = strings.TrimSpace(nodeID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if poolID == "" || nodeID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return EndpointPoolMember{}, false, fmt.Errorf("%w: pool_id, node_id, and idempotency_key are required", faults.ErrValidation)
	}
	if weight < 1 || weight > 1000 || priority < 0 || priority > 1000 {
		return EndpointPoolMember{}, false, fmt.Errorf("%w: endpoint member weight/priority is outside the allowed range", faults.ErrValidation)
	}
	now := s.now().UTC()
	requestHash := hashMemberRequest(poolID, nodeID, weight, priority)
	event, err := audit.NewEvent(now, "administrator", adminID, "endpoint_pool.member_confirm", "endpoint_pool_member", poolID+":"+nodeID, "succeeded", map[string]any{
		"weight":   weight,
		"priority": priority,
	})
	if err != nil {
		return EndpointPoolMember{}, false, err
	}
	return s.repository.AddEndpointPoolMember(ctx, AddMemberInput{
		PoolID: poolID, NodeID: nodeID, Weight: weight, Priority: priority,
		IdempotencyKey: idempotencyKey, RequestSHA256: requestHash, CreatedBy: adminID, CreatedAt: now,
	}, event)
}

func (s *Service) Delete(ctx context.Context, adminID, poolID, idempotencyKey string) (bool, error) {
	poolID = strings.TrimSpace(poolID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validAdministratorID(adminID) || poolID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return false, fmt.Errorf("%w: administrator, pool_id, and idempotency_key are required", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", adminID, "endpoint_pool.delete", "endpoint_pool", poolID, "succeeded", nil)
	if err != nil {
		return false, err
	}
	return s.repository.DeleteEndpointPool(ctx, DeletePoolInput{
		PoolID: poolID, IdempotencyKey: idempotencyKey, RequestSHA256: hashStrings(poolID),
		DeletedBy: adminID, DeletedAt: now,
	}, event)
}

func hashRequest(name, groupID string, mode Mode, protocol, hostname string, port int, policy SelectionPolicy, ruleID ...string) string {
	boundRule := ""
	if len(ruleID) > 0 {
		boundRule = ruleID[0]
	}
	return hashStrings(name, groupID, boundRule, string(mode), protocol, hostname, fmt.Sprintf("%d", port), string(policy))
}

func hashMemberRequest(poolID, nodeID string, weight, priority int) string {
	return hashStrings(poolID, nodeID, fmt.Sprintf("%d", weight), fmt.Sprintf("%d", priority))
}

func hashStrings(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func NewService(repository Repository, now func() time.Time) *Service {
	return NewServiceWithReadyCandidateSource(repository, now, nil)
}

func NewServiceWithReadyCandidateSource(repository Repository, now func() time.Time, readyCandidates ReadyCandidateSource) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now, readyCandidates: readyCandidates}
}

func (s *Service) ValidatePool(pool EndpointPool, members []EndpointPoolMember) error {
	if !pool.Mode.Valid() {
		return fmt.Errorf("%w: unsupported endpoint mode", faults.ErrValidation)
	}
	if !ValidSelectionPolicy(pool.SelectionPolicy) {
		return fmt.Errorf("%w: unsupported endpoint selection policy", faults.ErrValidation)
	}
	if _, err := serviceaddress.NormalizeHost(pool.Hostname); err != nil {
		return fmt.Errorf("%w: %s", faults.ErrValidation, err)
	}
	if strings.TrimSpace(pool.GroupID) == "" {
		return fmt.Errorf("%w: group_id and hostname are required", faults.ErrValidation)
	}
	if pool.Port < 1 || pool.Port > 65535 {
		return fmt.Errorf("%w: endpoint port must be between 1 and 65535", faults.ErrValidation)
	}
	if pool.Protocol != "vless" && pool.Protocol != "tcp" && pool.Protocol != "socks5" {
		return fmt.Errorf("%w: endpoint protocol must be vless, tcp, or socks5", faults.ErrValidation)
	}
	if len(members) == 0 {
		return fmt.Errorf("%w: endpoint pool must have at least one candidate", faults.ErrValidation)
	}
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		if strings.TrimSpace(member.GroupID) == "" || member.GroupID != pool.GroupID {
			return fmt.Errorf("%w: endpoint candidate belongs to a different device group", faults.ErrConflict)
		}
		if strings.TrimSpace(member.NodeID) == "" {
			return fmt.Errorf("%w: endpoint candidate node_id is required", faults.ErrValidation)
		}
		if _, exists := seen[member.NodeID]; exists {
			return fmt.Errorf("%w: endpoint candidate node_id is duplicated", faults.ErrConflict)
		}
		seen[member.NodeID] = struct{}{}
		if member.Weight < 0 || member.Weight > 1000 || member.Priority < 0 || member.Priority > 1000 {
			return fmt.Errorf("%w: endpoint candidate weight/priority is outside the allowed range", faults.ErrValidation)
		}
		if !member.State.Valid() {
			return fmt.Errorf("%w: endpoint candidate state is invalid", faults.ErrValidation)
		}
	}
	return nil
}

func (s *Service) CandidateSet(ctx context.Context, poolID string, now time.Time, healthTTL time.Duration) ([]EndpointPoolMember, error) {
	members, err := s.repository.EndpointPoolMembers(ctx, poolID)
	if err != nil {
		return nil, err
	}
	eligible := make([]EndpointPoolMember, 0, len(members))
	for _, member := range members {
		if member.CandidateEligibleForNewConnection(now, healthTTL) {
			eligible = append(eligible, member)
		}
	}
	return eligible, nil
}
