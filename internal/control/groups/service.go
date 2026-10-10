package groups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

type CreateMetadata struct {
	UserGroupID string
	HideInProbe bool
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) Create(ctx context.Context, adminID, name string, kind Kind, selectionPolicy endpoints.SelectionPolicy, description string) (DeviceGroup, error) {
	return s.CreateWithMetadata(ctx, adminID, name, kind, selectionPolicy, description, CreateMetadata{})
}

func (s *Service) CreateWithMetadata(ctx context.Context, adminID, name string, kind Kind, selectionPolicy endpoints.SelectionPolicy, description string, metadata CreateMetadata) (DeviceGroup, error) {
	group, _, err := s.createWithMetadata(ctx, adminID, name, kind, selectionPolicy, description, metadata, "")
	return group, err
}

func (s *Service) CreateWithMetadataIdempotent(ctx context.Context, adminID, name string, kind Kind, selectionPolicy endpoints.SelectionPolicy, description string, metadata CreateMetadata, idempotencyKey string) (DeviceGroup, bool, error) {
	return s.createWithMetadata(ctx, adminID, name, kind, selectionPolicy, description, metadata, idempotencyKey)
}

func (s *Service) createWithMetadata(ctx context.Context, adminID, name string, kind Kind, selectionPolicy endpoints.SelectionPolicy, description string, metadata CreateMetadata, idempotencyKey string) (DeviceGroup, bool, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	metadata.UserGroupID = strings.TrimSpace(metadata.UserGroupID)
	if name == "" || len(name) > 128 {
		return DeviceGroup{}, false, fmt.Errorf("%w: name must contain 1 to 128 characters", faults.ErrValidation)
	}
	if !kind.Creatable() {
		return DeviceGroup{}, false, fmt.Errorf("%w: invalid device group kind", faults.ErrValidation)
	}
	if !endpoints.ValidSelectionPolicy(selectionPolicy) {
		return DeviceGroup{}, false, fmt.Errorf("%w: invalid selection_policy", faults.ErrValidation)
	}
	if len(description) > 1024 {
		return DeviceGroup{}, false, fmt.Errorf("%w: description is too long", faults.ErrValidation)
	}
	if idempotencyKey != "" && !validIdempotencyKey(idempotencyKey) {
		return DeviceGroup{}, false, fmt.Errorf("%w: invalid Idempotency-Key", faults.ErrValidation)
	}
	request, err := json.Marshal(struct {
		Name            string                    `json:"name"`
		Kind            Kind                      `json:"kind"`
		UserGroupID     string                    `json:"user_group_id"`
		HideInProbe     bool                      `json:"hide_in_probe"`
		SelectionPolicy endpoints.SelectionPolicy `json:"selection_policy"`
		Description     string                    `json:"description"`
	}{name, kind, metadata.UserGroupID, metadata.HideInProbe, selectionPolicy, description})
	if err != nil {
		return DeviceGroup{}, false, err
	}
	digest := sha256.Sum256(request)
	id, err := idgen.New("grp")
	if err != nil {
		return DeviceGroup{}, false, err
	}
	now := s.now().UTC()
	group := DeviceGroup{ID: id, Name: name, Kind: kind, UserGroupID: metadata.UserGroupID, HideInProbe: metadata.HideInProbe, SelectionPolicy: selectionPolicy, Description: description, MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.create", "device_group", id, "succeeded", map[string]any{
		"name":             name,
		"kind":             kind,
		"user_group_id":    metadata.UserGroupID,
		"hide_in_probe":    metadata.HideInProbe,
		"selection_policy": selectionPolicy,
	})
	if err != nil {
		return DeviceGroup{}, false, err
	}
	if idempotencyKey != "" {
		if repository, ok := s.repository.(IdempotentRepository); ok {
			group, replayed, err := repository.CreateDeviceGroupIdempotent(ctx, CreateDeviceGroupInput{
				Group: group, CreatedBy: adminID, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]),
			}, event)
			return group, replayed, err
		}
		return DeviceGroup{}, false, fmt.Errorf("%w: device group repository lacks durable idempotency", faults.ErrValidation)
	}
	if err := s.repository.CreateDeviceGroup(ctx, group, event); err != nil {
		return DeviceGroup{}, false, err
	}
	return group, false, nil
}

func (s *Service) List(ctx context.Context) ([]DeviceGroup, error) {
	return s.repository.ListDeviceGroups(ctx)
}

func (s *Service) Update(ctx context.Context, adminID, groupID, name, userGroupID, description string, hideInProbe bool, selectionPolicy endpoints.SelectionPolicy, expectedRevision int64, idempotencyKey string) (DeviceGroup, bool, error) {
	groupID, name, userGroupID, description, idempotencyKey = strings.TrimSpace(groupID), strings.TrimSpace(name), strings.TrimSpace(userGroupID), strings.TrimSpace(description), strings.TrimSpace(idempotencyKey)
	if groupID == "" || name == "" || len(name) > 128 || len(description) > 1024 || idempotencyKey == "" {
		return DeviceGroup{}, false, fmt.Errorf("%w: group id, name and Idempotency-Key are required; name/description length is invalid", faults.ErrValidation)
	}
	if !endpoints.ValidSelectionPolicy(selectionPolicy) {
		return DeviceGroup{}, false, fmt.Errorf("%w: invalid selection_policy", faults.ErrValidation)
	}
	if expectedRevision < 0 {
		return DeviceGroup{}, false, fmt.Errorf("%w: revision must be non-negative", faults.ErrValidation)
	}
	group := DeviceGroup{ID: groupID, Name: name, UserGroupID: userGroupID, HideInProbe: hideInProbe, SelectionPolicy: selectionPolicy, Description: description, MetadataRevision: expectedRevision + 1, UpdatedAt: s.now().UTC()}
	hashInput := struct {
		ID              string                    `json:"id"`
		Name            string                    `json:"name"`
		UserGroupID     string                    `json:"user_group_id"`
		HideInProbe     bool                      `json:"hide_in_probe"`
		SelectionPolicy endpoints.SelectionPolicy `json:"selection_policy"`
		Description     string                    `json:"description"`
		Revision        int64                     `json:"revision"`
	}{groupID, name, userGroupID, hideInProbe, selectionPolicy, description, expectedRevision}
	raw, err := json.Marshal(hashInput)
	if err != nil {
		return DeviceGroup{}, false, err
	}
	sum := sha256.Sum256(raw)
	event, err := audit.NewEvent(group.UpdatedAt, "administrator", adminID, "device_group.update", "device_group", groupID, "succeeded", map[string]any{"revision": expectedRevision + 1})
	if err != nil {
		return DeviceGroup{}, false, err
	}
	return s.repository.UpdateDeviceGroup(ctx, UpdateInput{Group: group, ExpectedRevision: expectedRevision, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(sum[:]), CreatedBy: adminID}, event)
}

func (s *Service) Delete(ctx context.Context, adminID, groupID, idempotencyKey string) (bool, error) {
	adminID, groupID, idempotencyKey = strings.TrimSpace(adminID), strings.TrimSpace(groupID), strings.TrimSpace(idempotencyKey)
	if adminID == "" || groupID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return false, fmt.Errorf("%w: administrator, group_id, and idempotency_key are required", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.delete", "device_group", groupID, "succeeded", nil)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(struct {
		GroupID string `json:"group_id"`
	}{GroupID: groupID})
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(raw)
	return s.repository.DeleteDeviceGroup(ctx, DeleteInput{
		GroupID: groupID, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(sum[:]),
		DeletedBy: adminID, DeletedAt: now,
	}, event)
}

func (s *Service) ListMembers(ctx context.Context, groupID string) ([]Member, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, fmt.Errorf("%w: group_id is required", faults.ErrValidation)
	}
	return s.repository.ListGroupMembers(ctx, groupID)
}

func (s *Service) AddMember(ctx context.Context, adminID, groupID, nodeID string, weight, priority int) (Member, []generations.NodeConfigGeneration, error) {
	return s.AddMemberWithDialHost(ctx, adminID, groupID, nodeID, "", weight, priority)
}

func (s *Service) AddMemberWithDialHost(ctx context.Context, adminID, groupID, nodeID, dialHost string, weight, priority int) (Member, []generations.NodeConfigGeneration, error) {
	member, assignments, _, err := s.addMemberWithDialHost(ctx, adminID, groupID, nodeID, dialHost, weight, priority, "")
	return member, assignments, err
}

func (s *Service) AddMemberWithDialHostIdempotent(ctx context.Context, adminID, groupID, nodeID, dialHost string, weight, priority int, idempotencyKey string) (Member, []generations.NodeConfigGeneration, bool, error) {
	return s.addMemberWithDialHost(ctx, adminID, groupID, nodeID, dialHost, weight, priority, idempotencyKey)
}

func (s *Service) addMemberWithDialHost(ctx context.Context, adminID, groupID, nodeID, dialHost string, weight, priority int, idempotencyKey string) (Member, []generations.NodeConfigGeneration, bool, error) {
	groupID, nodeID, dialHost = strings.TrimSpace(groupID), strings.TrimSpace(nodeID), strings.TrimSpace(dialHost)
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(nodeID) == "" {
		return Member{}, nil, false, fmt.Errorf("%w: group_id and node_id are required", faults.ErrValidation)
	}
	if weight < 0 || weight > 1000 {
		return Member{}, nil, false, fmt.Errorf("%w: weight must be between 0 and 1000", faults.ErrValidation)
	}
	if priority < 0 || priority > 1000 {
		return Member{}, nil, false, fmt.Errorf("%w: priority must be between 0 and 1000", faults.ErrValidation)
	}
	if idempotencyKey != "" && !validIdempotencyKey(idempotencyKey) {
		return Member{}, nil, false, fmt.Errorf("%w: invalid Idempotency-Key", faults.ErrValidation)
	}
	if dialHost != "" {
		canonical, err := serviceaddress.NormalizeHost(dialHost)
		if err != nil {
			return Member{}, nil, false, fmt.Errorf("%w: dial_host: %s", faults.ErrValidation, err)
		}
		dialHost = canonical
	}
	request, err := json.Marshal(struct {
		GroupID, NodeID, DialHost string
		Weight, Priority          int
	}{groupID, nodeID, dialHost, weight, priority})
	if err != nil {
		return Member{}, nil, false, err
	}
	digest := sha256.Sum256(request)
	now := s.now().UTC()
	member := Member{GroupID: groupID, NodeID: nodeID, DialHost: dialHost, Weight: weight, Priority: priority, CreatedAt: now, UpdatedAt: now}
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.member_upsert", "device_group_member", groupID+":"+nodeID, "succeeded", map[string]any{
		"weight":    weight,
		"priority":  priority,
		"dial_host": dialHost,
	})
	if err != nil {
		return Member{}, nil, false, err
	}
	if idempotencyKey != "" {
		if repository, ok := s.repository.(IdempotentRepository); ok {
			member, assignments, replayed, err := repository.UpsertGroupMemberIdempotent(ctx, UpsertGroupMemberInput{
				Member: member, CreatedBy: adminID, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]),
			}, event)
			return member, assignments, replayed, err
		}
		return Member{}, nil, false, fmt.Errorf("%w: device group member repository lacks durable idempotency", faults.ErrValidation)
	}
	stored, assignments, err := s.repository.UpsertGroupMember(ctx, member, event)
	return stored, assignments, false, err
}

func (s *Service) UpdateMemberWeight(ctx context.Context, adminID, groupID, nodeID string, weight int, expectedUpdatedAt time.Time, idempotencyKey string) (UpdateMemberWeightResult, error) {
	adminID, groupID, nodeID, idempotencyKey = strings.TrimSpace(adminID), strings.TrimSpace(groupID), strings.TrimSpace(nodeID), strings.TrimSpace(idempotencyKey)
	if adminID == "" || groupID == "" || nodeID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 || expectedUpdatedAt.IsZero() || weight < 0 || weight > 1000 {
		return UpdateMemberWeightResult{}, fmt.Errorf("%w: administrator, group_id, node_id, updated_at, Idempotency-Key and weight between 0 and 1000 are required", faults.ErrValidation)
	}
	request, err := json.Marshal(struct {
		GroupID           string    `json:"group_id"`
		NodeID            string    `json:"node_id"`
		Weight            int       `json:"weight"`
		ExpectedUpdatedAt time.Time `json:"updated_at"`
	}{groupID, nodeID, weight, expectedUpdatedAt.UTC()})
	if err != nil {
		return UpdateMemberWeightResult{}, err
	}
	sum := sha256.Sum256(request)
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.member_weight_update", "device_group_member", groupID+":"+nodeID, "succeeded", map[string]any{"weight": weight})
	if err != nil {
		return UpdateMemberWeightResult{}, err
	}
	return s.repository.UpdateGroupMemberWeight(ctx, UpdateMemberWeightInput{
		GroupID: groupID, NodeID: nodeID, Weight: weight, ExpectedUpdatedAt: expectedUpdatedAt.UTC(), UpdatedAt: now,
		IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(sum[:]), UpdatedBy: adminID,
	}, event)
}

func (s *Service) RetireMember(ctx context.Context, adminID, groupID, nodeID string) (RetireMemberResult, error) {
	result, err := s.retireMember(ctx, adminID, groupID, nodeID, "")
	return result, err
}

func (s *Service) RetireMemberIdempotent(ctx context.Context, adminID, groupID, nodeID, idempotencyKey string) (RetireMemberResult, error) {
	return s.retireMember(ctx, adminID, groupID, nodeID, idempotencyKey)
}

func (s *Service) retireMember(ctx context.Context, adminID, groupID, nodeID, idempotencyKey string) (RetireMemberResult, error) {
	adminID, groupID, nodeID, idempotencyKey = strings.TrimSpace(adminID), strings.TrimSpace(groupID), strings.TrimSpace(nodeID), strings.TrimSpace(idempotencyKey)
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(nodeID) == "" {
		return RetireMemberResult{}, fmt.Errorf("%w: group_id and node_id are required", faults.ErrValidation)
	}
	if idempotencyKey != "" && !validIdempotencyKey(idempotencyKey) {
		return RetireMemberResult{}, fmt.Errorf("%w: invalid Idempotency-Key", faults.ErrValidation)
	}
	digest := sha256.Sum256([]byte("device_group.member_retire\x00" + groupID + "\x00" + nodeID))
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.member_retire", "device_group_member", groupID+":"+nodeID, "succeeded", nil)
	if err != nil {
		return RetireMemberResult{}, err
	}
	if idempotencyKey != "" {
		if repository, ok := s.repository.(IdempotentRepository); ok {
			return repository.RetireGroupMemberIdempotent(ctx, RetireGroupMemberInput{
				GroupID: groupID, NodeID: nodeID, RetiredAt: now, RetiredBy: adminID,
				IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]),
			}, event)
		}
		return RetireMemberResult{}, fmt.Errorf("%w: device group member repository lacks durable idempotency", faults.ErrValidation)
	}
	return s.repository.RetireGroupMember(ctx, groupID, nodeID, now, event)
}

func validIdempotencyKey(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r >= 127 }) < 0
}
