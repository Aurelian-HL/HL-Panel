// Package scheduler selects one healthy backend for each new client
// connection. It is intentionally independent from SOCKS5, VLESS, GOST, and
// Xray so the public endpoint remains a single service endpoint while the
// implementation can change underneath it.
package scheduler

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
)

type SelectionPolicy string

const (
	PolicyWeightedRoundRobin       SelectionPolicy = "weighted_round_robin"
	PolicyWeightedLeastConnections SelectionPolicy = "weighted_least_connections"
	PolicyRendezvousHash           SelectionPolicy = "rendezvous_hash"
)

var (
	ErrInvalidPolicy       = errors.New("invalid selection policy")
	ErrNoHealthyMember     = errors.New("no healthy endpoint member")
	ErrInvalidCandidateSet = errors.New("invalid endpoint candidate set")
)

// Candidate is a point-in-time health and capacity snapshot. The router never
// treats an offline, failed, draining, or zero-weight member as selectable.
type Candidate struct {
	ID                string
	Weight            int
	ActiveConnections int64
	Healthy           bool
	Draining          bool
}

// Router is safe for concurrent connection accepts. The round-robin cursor is
// stateful; least-connections and rendezvous selection are deterministic from
// the supplied snapshot and key.
type Router struct {
	mu       sync.Mutex
	policy   SelectionPolicy
	current  map[string]int64
	sequence uint64
}

func New(policy SelectionPolicy) (*Router, error) {
	if !validPolicy(policy) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidPolicy, policy)
	}
	return &Router{policy: policy, current: make(map[string]int64)}, nil
}

func (r *Router) Policy() SelectionPolicy { return r.policy }

// Select chooses exactly one candidate for a new connection. key is required
// for stable rendezvous hashing; an internal sequence is used when it is empty.
func (r *Router) Select(candidates []Candidate, key string) (Candidate, error) {
	eligible, err := normalizeCandidates(candidates)
	if err != nil {
		return Candidate{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == "" {
		r.sequence++
		key = fmt.Sprintf("connection-%d", r.sequence)
	}
	switch r.policy {
	case PolicyWeightedRoundRobin:
		return r.selectWeightedRoundRobin(eligible)
	case PolicyWeightedLeastConnections:
		return selectWeightedLeastConnections(eligible), nil
	case PolicyRendezvousHash:
		return selectRendezvous(eligible, key), nil
	default:
		return Candidate{}, fmt.Errorf("%w: %q", ErrInvalidPolicy, r.policy)
	}
}

func (r *Router) selectWeightedRoundRobin(candidates []Candidate) (Candidate, error) {
	activeIDs := make(map[string]struct{}, len(candidates))
	var total int64
	for _, candidate := range candidates {
		activeIDs[candidate.ID] = struct{}{}
		total += int64(candidate.Weight)
		r.current[candidate.ID] += int64(candidate.Weight)
	}
	for id := range r.current {
		if _, ok := activeIDs[id]; !ok {
			delete(r.current, id)
		}
	}
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if r.current[candidate.ID] > r.current[selected.ID] ||
			(r.current[candidate.ID] == r.current[selected.ID] && candidate.ID < selected.ID) {
			selected = candidate
		}
	}
	r.current[selected.ID] -= total
	return selected, nil
}

func selectWeightedLeastConnections(candidates []Candidate) Candidate {
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		// Compare active/weight without floating point rounding or int64
		// multiplication overflow.
		leftHigh, leftLow := bits.Mul64(uint64(candidate.ActiveConnections), uint64(selected.Weight))
		rightHigh, rightLow := bits.Mul64(uint64(selected.ActiveConnections), uint64(candidate.Weight))
		if leftHigh < rightHigh || (leftHigh == rightHigh && leftLow < rightLow) ||
			(leftHigh == rightHigh && leftLow == rightLow && (candidate.Weight > selected.Weight ||
				(candidate.Weight == selected.Weight && candidate.ID < selected.ID))) {
			selected = candidate
		}
	}
	return selected
}

func selectRendezvous(candidates []Candidate, key string) Candidate {
	selected := candidates[0]
	selectedScore := rendezvousScore(key, selected)
	for _, candidate := range candidates[1:] {
		score := rendezvousScore(key, candidate)
		if score < selectedScore || (score == selectedScore && candidate.ID < selected.ID) {
			selected = candidate
			selectedScore = score
		}
	}
	return selected
}

func rendezvousScore(key string, candidate Candidate) float64 {
	hash := sha256.Sum256([]byte(key + "\x00" + candidate.ID))
	value := binary.BigEndian.Uint64(hash[:8])
	// Avoid log(0) while retaining the full uint64 ordering domain.
	u := (float64(value) + 1) / (float64(^uint64(0)) + 1)
	return -math.Log(u) / float64(candidate.Weight)
}

func normalizeCandidates(candidates []Candidate) ([]Candidate, error) {
	if len(candidates) == 0 {
		return nil, ErrNoHealthyMember
	}
	items := make([]Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate.ID = strings.TrimSpace(candidate.ID)
		if candidate.ID == "" || candidate.Weight < 0 || candidate.Weight > 100000 || candidate.ActiveConnections < 0 {
			return nil, fmt.Errorf("%w: invalid member %q", ErrInvalidCandidateSet, candidate.ID)
		}
		if _, exists := seen[candidate.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate member %q", ErrInvalidCandidateSet, candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		if candidate.Healthy && !candidate.Draining && candidate.Weight > 0 {
			items = append(items, candidate)
		}
	}
	if len(items) == 0 {
		return nil, ErrNoHealthyMember
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func validPolicy(policy SelectionPolicy) bool {
	switch policy {
	case PolicyWeightedRoundRobin, PolicyWeightedLeastConnections, PolicyRendezvousHash:
		return true
	default:
		return false
	}
}
