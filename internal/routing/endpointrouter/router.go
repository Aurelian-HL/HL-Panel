// Package endpointrouter owns the in-process candidate membership and selects
// one healthy backend for each new connection. It is shared by gateway
// deployables and deliberately contains no agent or protocol-engine lifecycle.
package endpointrouter

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/routing/scheduler"
)

// SelectionPolicy determines how a front-door connection is assigned to one
// healthy endpoint. The router returns one endpoint, never a subscription list.
type SelectionPolicy string

const (
	SelectionPolicyWeightedRoundRobin       SelectionPolicy = "weighted_round_robin"
	SelectionPolicyWeightedLeastConnections SelectionPolicy = "weighted_least_connections"
	SelectionPolicyRendezvousHash           SelectionPolicy = "rendezvous_hash"
)

type EndpointStatus string

const (
	EndpointReady    EndpointStatus = "ready"
	EndpointDraining EndpointStatus = "draining"
	EndpointOffline  EndpointStatus = "offline"
)

type Endpoint struct {
	ID                   string         `json:"id"`
	Address              string         `json:"address"`
	Weight               int            `json:"weight"`
	Status               EndpointStatus `json:"status"`
	HealthLeaseExpiresAt time.Time      `json:"health_lease_expires_at"`
}

var (
	ErrNoHealthyEndpoint       = errors.New("no healthy endpoint is available")
	ErrUnknownEndpoint         = errors.New("endpoint is not in the current membership")
	ErrStaleReachabilityResult = errors.New("reachability result does not match the current membership state")
	ErrMembershipConflict      = errors.New("membership content changed without advancing revision")
)

type Router struct {
	policy             SelectionPolicy
	scheduler          *scheduler.Router
	now                func() time.Time
	mu                 sync.Mutex
	membershipRevision uint64
	members            map[string]Endpoint
	active             map[string]int
	localUnreachable   map[string]string
}

func New(policy SelectionPolicy) (*Router, error) {
	schedulerPolicy, err := toSchedulerPolicy(policy)
	if err != nil {
		return nil, err
	}
	schedulerRouter, err := scheduler.New(schedulerPolicy)
	if err != nil {
		return nil, err
	}
	return &Router{
		policy:           policy,
		scheduler:        schedulerRouter,
		now:              time.Now,
		members:          make(map[string]Endpoint),
		active:           make(map[string]int),
		localUnreachable: make(map[string]string),
	}, nil
}

// ReplaceMembership atomically installs a complete candidate snapshot.
func (r *Router) ReplaceMembership(endpoints []Endpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	revision := r.membershipRevision + 1
	if revision == 0 {
		return errors.New("membership revision exhausted")
	}
	return r.replaceMembershipLocked(revision, endpoints)
}

// ReplaceVersionedMembership installs a snapshot and records its source
// revision. Reachability verdicts must match this revision as well as the
// endpoint address, so an old same-address result cannot mutate new state.
func (r *Router) ReplaceVersionedMembership(revision uint64, endpoints []Endpoint) error {
	if revision == 0 {
		return errors.New("membership revision must be greater than zero")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if revision < r.membershipRevision {
		return fmt.Errorf("membership revision %d is older than active revision %d", revision, r.membershipRevision)
	}
	return r.replaceMembershipLocked(revision, endpoints)
}

func (r *Router) replaceMembershipLocked(revision uint64, endpoints []Endpoint) error {
	if len(endpoints) > 4096 {
		return errors.New("endpoint membership exceeds 4096 members")
	}
	next := make(map[string]Endpoint, len(endpoints))
	for _, endpoint := range endpoints {
		if err := validateEndpoint(endpoint); err != nil {
			return err
		}
		if _, exists := next[endpoint.ID]; exists {
			return fmt.Errorf("duplicate endpoint %q", endpoint.ID)
		}
		next[endpoint.ID] = endpoint
	}
	if revision == r.membershipRevision {
		if membershipsEqual(r.members, next) {
			return nil
		}
		return fmt.Errorf("%w: revision %d", ErrMembershipConflict, revision)
	}
	for endpointID, observedAddress := range r.localUnreachable {
		endpoint, exists := next[endpointID]
		if !exists || endpoint.Address != observedAddress {
			delete(r.localUnreachable, endpointID)
		}
	}
	r.membershipRevision = revision
	r.members = next
	return nil
}

func membershipsEqual(current, next map[string]Endpoint) bool {
	if len(current) != len(next) {
		return false
	}
	for id, currentEndpoint := range current {
		nextEndpoint, exists := next[id]
		if !exists || currentEndpoint.ID != nextEndpoint.ID || currentEndpoint.Address != nextEndpoint.Address ||
			currentEndpoint.Weight != nextEndpoint.Weight || currentEndpoint.Status != nextEndpoint.Status ||
			!currentEndpoint.HealthLeaseExpiresAt.Equal(nextEndpoint.HealthLeaseExpiresAt) {
			return false
		}
	}
	return true
}

// ReplaceCompiledMembership adapts the versioned control-plane DTO without
// exposing the complete candidate list to a client connection.
func (r *Router) ReplaceCompiledMembership(members []agentv1.EndpointMembership) error {
	endpoints := make([]Endpoint, 0, len(members))
	for _, member := range members {
		endpoints = append(endpoints, Endpoint{
			ID:                   member.ID,
			Address:              member.Address,
			Weight:               member.Weight,
			Status:               EndpointStatus(member.Status),
			HealthLeaseExpiresAt: member.HealthLeaseExpiresAt,
		})
	}
	return r.ReplaceMembership(endpoints)
}

// Select reserves one endpoint for a new connection. It is equivalent to
// SelectKey with an empty affinity key.
func (r *Router) Select() (Endpoint, error) {
	return r.SelectKey("")
}

// SelectKey reserves one endpoint for a new connection. Rendezvous routing
// uses the key for stable affinity; the other policies ignore it.
func (r *Router) SelectKey(key string) (Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	eligible := r.eligibleLocked()
	if len(eligible) == 0 {
		return Endpoint{}, ErrNoHealthyEndpoint
	}
	candidates := make([]scheduler.Candidate, 0, len(eligible))
	byID := make(map[string]Endpoint, len(eligible))
	for _, endpoint := range eligible {
		candidates = append(candidates, scheduler.Candidate{
			ID:                endpoint.ID,
			Weight:            endpoint.Weight,
			ActiveConnections: int64(r.active[endpoint.ID]),
			Healthy:           true,
		})
		byID[endpoint.ID] = endpoint
	}
	selectedCandidate, err := r.scheduler.Select(candidates, key)
	if err != nil {
		return Endpoint{}, err
	}
	selected := byID[selectedCandidate.ID]
	r.active[selected.ID]++
	return selected, nil
}

// Release returns a completed connection reservation to the selected member.
func (r *Router) Release(endpointID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[endpointID] > 0 {
		r.active[endpointID]--
		if r.active[endpointID] == 0 {
			delete(r.active, endpointID)
		}
		return nil
	}
	if _, exists := r.members[endpointID]; !exists {
		return ErrUnknownEndpoint
	}
	return nil
}

func (r *Router) ActiveConnections(endpointID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.members[endpointID]; !exists && r.active[endpointID] == 0 {
		return 0, ErrUnknownEndpoint
	}
	return r.active[endpointID], nil
}

// SetLocalReachability updates only the gateway's local TCP reachability veto.
// A reachable observation can remove a prior veto, but it cannot make an
// offline, draining, or expired authoritative endpoint eligible.
func (r *Router) SetLocalReachability(endpointID, observedAddress string, reachable bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setLocalReachabilityLocked(endpointID, observedAddress, reachable)
}

// SetLocalReachabilityAtRevision rejects verdicts produced for an older
// membership snapshot, including a same-ID, same-address endpoint whose
// authoritative status or lease changed in a newer snapshot.
func (r *Router) SetLocalReachabilityAtRevision(endpointID, observedAddress string, observedRevision uint64, reachable bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if observedRevision == 0 || observedRevision != r.membershipRevision {
		return ErrStaleReachabilityResult
	}
	return r.setLocalReachabilityLocked(endpointID, observedAddress, reachable)
}

func (r *Router) setLocalReachabilityLocked(endpointID, observedAddress string, reachable bool) error {
	endpoint, exists := r.members[endpointID]
	if !exists {
		return ErrUnknownEndpoint
	}
	if endpoint.Address != observedAddress {
		return ErrStaleReachabilityResult
	}
	if reachable {
		delete(r.localUnreachable, endpointID)
	} else {
		r.localUnreachable[endpointID] = observedAddress
	}
	return nil
}

func (r *Router) LocallyBlocked(endpointID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	endpoint, exists := r.members[endpointID]
	if !exists {
		return false, ErrUnknownEndpoint
	}
	return r.localUnreachable[endpointID] == endpoint.Address, nil
}

func (r *Router) eligibleLocked() []Endpoint {
	current := r.now()
	eligible := make([]Endpoint, 0, len(r.members))
	for _, endpoint := range r.members {
		if endpoint.Status != EndpointReady || endpoint.Weight == 0 || !endpoint.HealthLeaseExpiresAt.After(current) {
			continue
		}
		if r.localUnreachable[endpoint.ID] == endpoint.Address {
			continue
		}
		eligible = append(eligible, endpoint)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return eligible
}

func toSchedulerPolicy(policy SelectionPolicy) (scheduler.SelectionPolicy, error) {
	switch policy {
	case SelectionPolicyWeightedRoundRobin:
		return scheduler.PolicyWeightedRoundRobin, nil
	case SelectionPolicyWeightedLeastConnections:
		return scheduler.PolicyWeightedLeastConnections, nil
	case SelectionPolicyRendezvousHash:
		return scheduler.PolicyRendezvousHash, nil
	default:
		return "", fmt.Errorf("unsupported selection policy %q", policy)
	}
}

func validateEndpoint(endpoint Endpoint) error {
	if strings.TrimSpace(endpoint.ID) == "" {
		return errors.New("endpoint id is required")
	}
	if err := validateAddress(endpoint.Address); err != nil {
		return fmt.Errorf("endpoint %q address: %w", endpoint.ID, err)
	}
	if endpoint.Weight < 0 || endpoint.Weight > 10000 {
		return fmt.Errorf("endpoint %q weight must be between 0 and 10000", endpoint.ID)
	}
	switch endpoint.Status {
	case EndpointReady, EndpointDraining, EndpointOffline:
	default:
		return fmt.Errorf("endpoint %q has unsupported status %q", endpoint.ID, endpoint.Status)
	}
	if endpoint.HealthLeaseExpiresAt.IsZero() {
		return fmt.Errorf("endpoint %q is missing a health lease", endpoint.ID)
	}
	return nil
}

func validateAddress(address string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("is empty")
	}
	if strings.TrimSpace(address) != address || strings.ContainsAny(address, "\r\n\t @?#") {
		return errors.New("contains whitespace, credential, or URL characters")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("must be a host:port pair")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}
