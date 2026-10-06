package endpoints

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestCreateRejectsCredentialsAndMultipleHostsBeforePersistence(t *testing.T) {
	service := NewService(nil, time.Now)
	for _, hostname := range []string{"user:password@example.test", "vless://example.test", "first.test\nsecond.test", "host:443", "-bad.test"} {
		_, _, err := service.Create(context.Background(), "admin", "service", "group", ModeSingleServiceEndpoint, "vless", hostname, 443, SelectionWeightedRoundRobin, "key")
		if !errors.Is(err, faults.ErrValidation) {
			t.Fatal("invalid public endpoint reached persistence")
		}
	}
}

type listRepository struct {
	pools   []EndpointPool
	members map[string][]EndpointPoolMember
}

func (r *listRepository) CreateEndpointPool(context.Context, CreatePoolInput, audit.Event) (EndpointPool, bool, error) {
	panic("not used")
}

func (r *listRepository) ListEndpointPools(context.Context) ([]EndpointPool, error) {
	return append([]EndpointPool(nil), r.pools...), nil
}

func (r *listRepository) EndpointPool(_ context.Context, poolID string) (EndpointPool, error) {
	for _, pool := range r.pools {
		if pool.ID == poolID {
			return pool, nil
		}
	}
	panic("not used")
}

func (r *listRepository) EndpointPoolMembers(_ context.Context, poolID string) ([]EndpointPoolMember, error) {
	return append([]EndpointPoolMember(nil), r.members[poolID]...), nil
}

func (r *listRepository) AddEndpointPoolMember(context.Context, AddMemberInput, audit.Event) (EndpointPoolMember, bool, error) {
	panic("not used")
}

func (r *listRepository) DeleteEndpointPool(context.Context, DeletePoolInput, audit.Event) (bool, error) {
	panic("not used")
}

type readyCandidateSource struct {
	counts map[string]int
	err    error
}

func (s readyCandidateSource) ReadyCandidateCount(_ context.Context, poolID string) (int, error) {
	return s.counts[poolID], s.err
}

func TestListPoolsDoesNotInferProtocolHealthFromMemberTimestamps(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Second)
	repository := &listRepository{
		pools: []EndpointPool{{ID: "pool-a", MemberCount: 2, HealthyCandidateCount: 99}},
		members: map[string][]EndpointPoolMember{
			"pool-a": {
				{NodeID: "recent", Weight: 100, State: CandidateEligible, LastHealthAt: &recent},
				{NodeID: "unverified", Weight: 100, State: CandidateEligible},
			},
		},
	}

	items, err := NewService(repository, func() time.Time { return now }).ListPools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].MemberCount != 2 || items[0].HealthyCandidateCount != 0 {
		t.Fatalf("unverified member was counted healthy: %+v", items)
	}

	items, err = NewServiceWithReadyCandidateSource(repository, func() time.Time { return now }, readyCandidateSource{
		counts: map[string]int{"pool-a": 1},
	}).ListPools(context.Background())
	if err != nil || len(items) != 1 || items[0].HealthyCandidateCount != 1 {
		t.Fatalf("gateway readiness was not used: items=%+v err=%v", items, err)
	}

	probeErr := errors.New("gateway readiness unavailable")
	_, err = NewServiceWithReadyCandidateSource(repository, func() time.Time { return now }, readyCandidateSource{err: probeErr}).ListPools(context.Background())
	if !errors.Is(err, probeErr) {
		t.Fatalf("readiness failure was hidden: %v", err)
	}
}
