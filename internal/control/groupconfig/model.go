// Package groupconfig owns network and forwarding policy for a device group.
package groupconfig

import "time"

type DirectPolicy string

type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

const (
	DirectPolicyDisabled DirectPolicy = "DISABLED"
	DirectPolicyOptional DirectPolicy = "OPTIONAL"
	DirectPolicyForced   DirectPolicy = "FORCED"
)

func (p DirectPolicy) Valid() bool {
	return p == DirectPolicyDisabled || p == DirectPolicyOptional || p == DirectPolicyForced
}

func (p DirectPolicy) AllowsDirect() bool {
	return p == DirectPolicyOptional || p == DirectPolicyForced
}

func (p DirectPolicy) AllowsExitGroup() bool {
	return p == DirectPolicyDisabled || p == DirectPolicyOptional
}

func directPolicyFromLegacy(allowDirect bool) DirectPolicy {
	if allowDirect {
		return DirectPolicyOptional
	}
	return DirectPolicyDisabled
}

type GroupNetwork struct {
	GroupID      string       `json:"group_id"`
	ConnectHost  string       `json:"connect_host"`
	PortStart    int          `json:"port_start"`
	PortEnd      int          `json:"port_end"`
	DirectPolicy DirectPolicy `json:"direct_policy"`
	// AllowDirect is retained as a read compatibility projection for v2
	// snapshots and clients. DirectPolicy is authoritative for new writes.
	AllowDirect          bool        `json:"allow_direct"`
	PortRanges           []PortRange `json:"port_ranges,omitempty"`
	AllowedUserGroupIDs  []string    `json:"allowed_user_group_ids,omitempty"`
	AllowedEntryGroupIDs []string    `json:"allowed_entry_group_ids,omitempty"`
	AllowedExitGroupIDs  []string    `json:"allowed_exit_group_ids"`
	FallbackExitGroupID  string      `json:"fallback_exit_group_id,omitempty"`
	TrafficMultiplier    float64     `json:"traffic_multiplier"`
	Revision             int64       `json:"revision"`
	UpdatedAt            time.Time   `json:"updated_at"`
}

func (n GroupNetwork) EffectivePortRanges() []PortRange {
	if len(n.PortRanges) > 0 {
		return append([]PortRange(nil), n.PortRanges...)
	}
	if n.PortStart > 0 && n.PortEnd >= n.PortStart {
		return []PortRange{{Start: n.PortStart, End: n.PortEnd}}
	}
	return nil
}

func (n GroupNetwork) ContainsPort(port int) bool {
	for _, item := range n.EffectivePortRanges() {
		if port >= item.Start && port <= item.End {
			return true
		}
	}
	return false
}

func (n GroupNetwork) EffectiveDirectPolicy() DirectPolicy {
	if n.DirectPolicy == "" {
		return directPolicyFromLegacy(n.AllowDirect)
	}
	return n.DirectPolicy
}

func NormalizeStoredNetwork(input GroupNetwork) (GroupNetwork, error) {
	policy := input.EffectiveDirectPolicy()
	if !policy.Valid() {
		return GroupNetwork{}, ErrInvalidDirectPolicy
	}
	input.DirectPolicy = policy
	input.AllowDirect = policy.AllowsDirect()
	ranges, err := normalizePortRanges(input.PortRanges, input.PortStart, input.PortEnd)
	if err != nil {
		return GroupNetwork{}, err
	}
	input.PortRanges = ranges
	if len(ranges) > 0 {
		input.PortStart = ranges[0].Start
		input.PortEnd = ranges[len(ranges)-1].End
	} else {
		input.PortStart, input.PortEnd = 0, 0
	}
	return input, nil
}

// Request is a full network-policy replacement. Revision zero creates the
// first policy; later updates compare the previous revision atomically.
type Request struct {
	ConnectHost string      `json:"connect_host"`
	PortStart   int         `json:"port_start"`
	PortEnd     int         `json:"port_end"`
	PortRanges  []PortRange `json:"port_ranges,omitempty"`
	// DirectPolicy is omitted by legacy clients. DISABLED and OPTIONAL are
	// normalized to the legacy boolean representation so old idempotency
	// fingerprints remain stable across the v2-to-v3 migration.
	DirectPolicy         DirectPolicy `json:"direct_policy,omitempty"`
	AllowDirect          bool         `json:"allow_direct"`
	AllowedUserGroupIDs  []string     `json:"allowed_user_group_ids,omitempty"`
	AllowedEntryGroupIDs []string     `json:"allowed_entry_group_ids,omitempty"`
	AllowedExitGroupIDs  []string     `json:"allowed_exit_group_ids"`
	FallbackExitGroupID  string       `json:"fallback_exit_group_id,omitempty"`
	TrafficMultiplier    float64      `json:"traffic_multiplier"`
	Revision             int64        `json:"revision"`
}

func (r Request) EffectiveDirectPolicy() DirectPolicy {
	if r.DirectPolicy == "" {
		return directPolicyFromLegacy(r.AllowDirect)
	}
	return r.DirectPolicy
}
