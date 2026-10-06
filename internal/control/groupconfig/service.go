package groupconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

var ErrInvalidDirectPolicy = fmt.Errorf("%w: direct_policy must be DISABLED, OPTIONAL or FORCED", faults.ErrValidation)

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

func NormalizeRequest(input Request) (Request, error) {
	input.ConnectHost = strings.TrimSpace(input.ConnectHost)
	if input.ConnectHost != "" {
		host, err := serviceaddress.NormalizeHost(input.ConnectHost)
		if err != nil {
			return Request{}, fmt.Errorf("%w: connect_host must be one DNS name or IP without protocol, credentials, path or port", faults.ErrValidation)
		}
		input.ConnectHost = host
	}
	ranges, err := normalizePortRanges(input.PortRanges, input.PortStart, input.PortEnd)
	if err != nil {
		return Request{}, err
	}
	input.PortRanges = ranges
	if len(ranges) > 0 {
		input.PortStart = ranges[0].Start
		input.PortEnd = ranges[len(ranges)-1].End
	} else {
		input.PortStart, input.PortEnd = 0, 0
	}
	if math.IsNaN(input.TrafficMultiplier) || math.IsInf(input.TrafficMultiplier, 0) || input.TrafficMultiplier < 0 || input.TrafficMultiplier > 1000 {
		return Request{}, fmt.Errorf("%w: traffic_multiplier must be finite and between 0 and 1000", faults.ErrValidation)
	}
	if input.Revision < 0 || input.Revision == math.MaxInt64 {
		return Request{}, fmt.Errorf("%w: revision must be non-negative and incrementable", faults.ErrValidation)
	}
	if len(input.AllowedExitGroupIDs) > 1024 {
		return Request{}, fmt.Errorf("%w: at most 1024 allowed exit groups", faults.ErrValidation)
	}
	policy := input.EffectiveDirectPolicy()
	if !policy.Valid() {
		return Request{}, ErrInvalidDirectPolicy
	}
	// Keep OPTIONAL and DISABLED byte-compatible with the former boolean-only
	// request so a retry can replay an operation created before this upgrade.
	switch policy {
	case DirectPolicyDisabled:
		input.DirectPolicy = ""
		input.AllowDirect = false
	case DirectPolicyOptional:
		input.DirectPolicy = ""
		input.AllowDirect = true
	case DirectPolicyForced:
		input.AllowDirect = true
	}
	input.AllowedUserGroupIDs, err = normalizeIdentifiers(input.AllowedUserGroupIDs, "allowed_user_group_ids")
	if err != nil {
		return Request{}, err
	}
	input.AllowedEntryGroupIDs, err = normalizeIdentifiers(input.AllowedEntryGroupIDs, "allowed_entry_group_ids")
	if err != nil {
		return Request{}, err
	}
	input.AllowedExitGroupIDs, err = normalizeIdentifiers(input.AllowedExitGroupIDs, "allowed_exit_group_ids")
	if err != nil {
		return Request{}, err
	}
	input.FallbackExitGroupID = strings.TrimSpace(input.FallbackExitGroupID)
	if input.FallbackExitGroupID != "" && !validIdentifier(input.FallbackExitGroupID) {
		return Request{}, fmt.Errorf("%w: fallback_exit_group_id is invalid", faults.ErrValidation)
	}
	if input.FallbackExitGroupID != "" && !slices.Contains(input.AllowedExitGroupIDs, input.FallbackExitGroupID) {
		return Request{}, fmt.Errorf("%w: fallback_exit_group_id must be one of allowed_exit_group_ids", faults.ErrValidation)
	}
	if policy == DirectPolicyForced && len(input.AllowedExitGroupIDs) != 0 {
		return Request{}, fmt.Errorf("%w: FORCED direct policy cannot authorize exit groups", faults.ErrValidation)
	}
	return input, nil
}

func normalizePortRanges(explicit []PortRange, legacyStart, legacyEnd int) ([]PortRange, error) {
	if len(explicit) == 0 {
		if legacyStart == 0 && legacyEnd == 0 {
			return nil, nil
		}
		explicit = []PortRange{{Start: legacyStart, End: legacyEnd}}
	}
	if len(explicit) > 32 {
		return nil, fmt.Errorf("%w: at most 32 port ranges", faults.ErrValidation)
	}
	ranges := append([]PortRange(nil), explicit...)
	for _, item := range ranges {
		if item.Start < 1 || item.End > 65535 || item.End < item.Start {
			return nil, fmt.Errorf("%w: every port range must be within 1 to 65535", faults.ErrValidation)
		}
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})
	merged := make([]PortRange, 0, len(ranges))
	for _, item := range ranges {
		if len(merged) == 0 || item.Start > merged[len(merged)-1].End+1 {
			merged = append(merged, item)
			continue
		}
		if item.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = item.End
		}
	}
	return merged, nil
}

func normalizeIdentifiers(values []string, field string) ([]string, error) {
	if len(values) > 1024 {
		return nil, fmt.Errorf("%w: %s has too many entries", faults.ErrValidation, field)
	}
	ids := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		id := strings.TrimSpace(raw)
		if !validIdentifier(id) {
			return nil, fmt.Errorf("%w: %s contains an invalid identifier", faults.ErrValidation, field)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: %s contains a duplicate", faults.ErrValidation, field)
		}
		seen[id] = struct{}{}
		ids[index] = id
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Service) Update(ctx context.Context, adminID, groupID string, request Request, idempotencyKey string) (GroupNetwork, bool, error) {
	groupID = strings.TrimSpace(groupID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdentifier(groupID) || !validIdentifier(idempotencyKey) {
		return GroupNetwork{}, false, fmt.Errorf("%w: group id and Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	request, err := NormalizeRequest(request)
	if err != nil {
		return GroupNetwork{}, false, err
	}
	for _, exitID := range request.AllowedExitGroupIDs {
		if exitID == groupID {
			return GroupNetwork{}, false, fmt.Errorf("%w: a group cannot reference itself as an exit", faults.ErrValidation)
		}
	}
	for _, entryID := range request.AllowedEntryGroupIDs {
		if entryID == groupID {
			return GroupNetwork{}, false, fmt.Errorf("%w: a group cannot reference itself as an entry", faults.ErrValidation)
		}
	}
	if request.FallbackExitGroupID == groupID {
		return GroupNetwork{}, false, fmt.Errorf("%w: a group cannot use itself as fallback", faults.ErrValidation)
	}
	data, err := json.Marshal(struct {
		GroupID string
		Request Request
	}{groupID, request})
	if err != nil {
		return GroupNetwork{}, false, err
	}
	digest := sha256.Sum256(data)
	now := s.now().UTC()
	policy := request.EffectiveDirectPolicy()
	network := GroupNetwork{GroupID: groupID, ConnectHost: request.ConnectHost, PortStart: request.PortStart, PortEnd: request.PortEnd, PortRanges: request.PortRanges, DirectPolicy: policy, AllowDirect: policy.AllowsDirect(),
		AllowedUserGroupIDs: request.AllowedUserGroupIDs, AllowedEntryGroupIDs: request.AllowedEntryGroupIDs, AllowedExitGroupIDs: request.AllowedExitGroupIDs,
		FallbackExitGroupID: request.FallbackExitGroupID, TrafficMultiplier: request.TrafficMultiplier, Revision: request.Revision + 1, UpdatedAt: now}
	event, err := audit.NewEvent(now, "administrator", adminID, "device_group.network_update", "device_group", groupID, "succeeded", map[string]any{
		"port_start": network.PortStart, "port_end": network.PortEnd, "direct_policy": network.DirectPolicy, "allow_direct": network.AllowDirect,
		"port_range_count": len(network.PortRanges), "allowed_user_group_count": len(network.AllowedUserGroupIDs),
		"allowed_entry_group_count": len(network.AllowedEntryGroupIDs), "allowed_exit_group_count": len(network.AllowedExitGroupIDs),
		"has_fallback_exit": network.FallbackExitGroupID != "", "traffic_multiplier": network.TrafficMultiplier, "revision": network.Revision,
	})
	if err != nil {
		return GroupNetwork{}, false, err
	}
	return s.repository.UpdateGroupNetwork(ctx, UpdateInput{Network: network, ExpectedRevision: request.Revision, IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]), CreatedBy: adminID}, event)
}

func (s *Service) List(ctx context.Context) ([]GroupNetwork, error) {
	return s.repository.ListGroupNetworks(ctx)
}
func (s *Service) Get(ctx context.Context, groupID string) (GroupNetwork, error) {
	return s.repository.GroupNetwork(ctx, strings.TrimSpace(groupID))
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
