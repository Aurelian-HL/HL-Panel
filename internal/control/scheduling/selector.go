package scheduling

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/routing/scheduler"
)

var ErrNoEligibleCandidates = errors.New("no eligible endpoint candidates")

// Selector owns control-plane health/priority filtering and delegates the
// pure weighted selection algorithms to the shared routing scheduler used by
// the data plane. This keeps one implementation of each policy.
type Selector struct {
	mu      sync.Mutex
	routers map[string]*scheduler.Router
}

func NewSelector() *Selector {
	return &Selector{routers: make(map[string]*scheduler.Router)}
}

// Select performs server-side admission selection. It never consults DNS and
// never returns a customer-facing URI; the caller receives a node candidate and
// must acquire a connection lease transactionally before dialing it.
func (s *Selector) Select(poolID string, policy endpoints.SelectionPolicy, candidates []endpoints.EndpointPoolMember, key string, now time.Time, healthTTL time.Duration) (endpoints.EndpointPoolMember, error) {
	eligible := eligibleCandidates(candidates, now, healthTTL)
	if len(eligible) == 0 {
		return endpoints.EndpointPoolMember{}, ErrNoEligibleCandidates
	}
	sort.Slice(eligible, func(left, right int) bool {
		if eligible[left].Priority == eligible[right].Priority {
			return eligible[left].NodeID < eligible[right].NodeID
		}
		return eligible[left].Priority < eligible[right].Priority
	})
	bestPriority := eligible[0].Priority
	priorityCandidates := eligible[:0]
	for _, candidate := range eligible {
		if candidate.Priority == bestPriority {
			priorityCandidates = append(priorityCandidates, candidate)
		}
	}
	router, err := s.router(poolID, policy)
	if err != nil {
		return endpoints.EndpointPoolMember{}, err
	}
	routingCandidates := make([]scheduler.Candidate, 0, len(priorityCandidates))
	byID := make(map[string]endpoints.EndpointPoolMember, len(priorityCandidates))
	for _, candidate := range priorityCandidates {
		routingCandidates = append(routingCandidates, scheduler.Candidate{
			ID:                candidate.NodeID,
			Weight:            candidate.Weight,
			ActiveConnections: candidate.ActiveConnections,
			Healthy:           true,
		})
		byID[candidate.NodeID] = candidate
	}
	selected, err := router.Select(routingCandidates, key)
	if errors.Is(err, scheduler.ErrNoHealthyMember) {
		return endpoints.EndpointPoolMember{}, ErrNoEligibleCandidates
	}
	if err != nil {
		return endpoints.EndpointPoolMember{}, err
	}
	member, ok := byID[selected.ID]
	if !ok {
		return endpoints.EndpointPoolMember{}, fmt.Errorf("selected unknown endpoint candidate %q", selected.ID)
	}
	return member, nil
}

func (s *Selector) router(poolID string, policy endpoints.SelectionPolicy) (*scheduler.Router, error) {
	if !endpoints.ValidSelectionPolicy(policy) {
		return nil, fmt.Errorf("unsupported endpoint selection policy %q", policy)
	}
	key := poolID + "\x00" + string(policy)
	s.mu.Lock()
	defer s.mu.Unlock()
	if router, ok := s.routers[key]; ok {
		return router, nil
	}
	router, err := scheduler.New(policy)
	if err != nil {
		return nil, err
	}
	s.routers[key] = router
	return router, nil
}

func eligibleCandidates(candidates []endpoints.EndpointPoolMember, now time.Time, healthTTL time.Duration) []endpoints.EndpointPoolMember {
	eligible := make([]endpoints.EndpointPoolMember, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.CandidateEligibleForNewConnection(now, healthTTL) {
			eligible = append(eligible, candidate)
		}
	}
	return eligible
}
