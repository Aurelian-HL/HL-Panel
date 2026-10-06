package groupconfig

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type captureRepository struct {
	input UpdateInput
	event audit.Event
	calls int
}

func (r *captureRepository) UpdateGroupNetwork(_ context.Context, input UpdateInput, event audit.Event) (GroupNetwork, bool, error) {
	r.input = input
	r.event = event
	r.calls++
	return input.Network, false, nil
}
func (*captureRepository) ListGroupNetworks(context.Context) ([]GroupNetwork, error) {
	return []GroupNetwork{}, nil
}
func (*captureRepository) GroupNetwork(context.Context, string) (GroupNetwork, error) {
	return GroupNetwork{}, faults.ErrNotFound
}

func TestNetworkFirstRevisionCanonicalIdempotencyAndNoAlias(t *testing.T) {
	repo := &captureRepository{}
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	service := NewService(repo, func() time.Time { return now })
	request := Request{ConnectHost: " ENTRY.Example. ", PortStart: 10000, PortEnd: 20000, AllowDirect: true, AllowedExitGroupIDs: []string{"exit-z", "exit-a"}, TrafficMultiplier: 1}
	network, replayed, err := service.Update(context.Background(), "admin", "entry-1", request, "network-create")
	if err != nil || replayed || network.Revision != 1 || network.ConnectHost != "entry.example" || network.DirectPolicy != DirectPolicyOptional || !network.AllowDirect || !network.UpdatedAt.Equal(now) || repo.input.ExpectedRevision != 0 {
		t.Fatalf("first revision failed: %+v %v", network, err)
	}
	if request.AllowedExitGroupIDs[0] != "exit-z" {
		t.Fatal("caller input was mutated")
	}
	digest := repo.input.RequestSHA256
	request.AllowedExitGroupIDs = []string{"exit-a", "exit-z"}
	_, _, err = service.Update(context.Background(), "admin", "entry-1", request, "network-create")
	if err != nil || repo.input.RequestSHA256 != digest {
		t.Fatal("exit set ordering should not change digest")
	}
	request.Revision = 1
	_, _, err = service.Update(context.Background(), "admin", "entry-1", request, "network-update")
	if err != nil || repo.input.ExpectedRevision != 1 || repo.input.Network.Revision != 2 || repo.input.RequestSHA256 == digest {
		t.Fatal("replacement must carry revision and new digest")
	}
}

func TestDirectPolicyCompatibilityAndValidation(t *testing.T) {
	cases := []struct {
		name       string
		request    Request
		wantPolicy DirectPolicy
		wantLegacy bool
	}{
		{"legacy disabled", Request{}, DirectPolicyDisabled, false},
		{"legacy optional", Request{AllowDirect: true}, DirectPolicyOptional, true},
		{"explicit disabled", Request{DirectPolicy: DirectPolicyDisabled, AllowDirect: true}, DirectPolicyDisabled, false},
		{"explicit optional", Request{DirectPolicy: DirectPolicyOptional}, DirectPolicyOptional, true},
		{"explicit forced", Request{DirectPolicy: DirectPolicyForced}, DirectPolicyForced, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			request.TrafficMultiplier = 1
			normalized, err := NormalizeRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if normalized.EffectiveDirectPolicy() != tc.wantPolicy || normalized.AllowDirect != tc.wantLegacy {
				t.Fatalf("got policy %s allow_direct=%v", normalized.EffectiveDirectPolicy(), normalized.AllowDirect)
			}
			if tc.wantPolicy != DirectPolicyForced && normalized.DirectPolicy != "" {
				t.Fatal("legacy-equivalent policy changed the idempotency request shape")
			}
		})
	}
	for name, request := range map[string]Request{
		"unknown policy":    {DirectPolicy: "AUTOMATIC", TrafficMultiplier: 1},
		"forced with exits": {DirectPolicy: DirectPolicyForced, AllowedExitGroupIDs: []string{"exit-1"}, TrafficMultiplier: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("invalid direct policy accepted: %v", err)
			}
		})
	}
}

func TestRejectInvalidNetworkPolicy(t *testing.T) {
	cases := []Request{
		{ConnectHost: "https://entry.example", TrafficMultiplier: 1},
		{PortStart: 0, PortEnd: 10, TrafficMultiplier: 1},
		{PortStart: 100, PortEnd: 99, TrafficMultiplier: 1},
		{PortStart: 1, PortEnd: 65536, TrafficMultiplier: 1},
		{TrafficMultiplier: math.NaN()},
		{TrafficMultiplier: math.Inf(1)},
		{TrafficMultiplier: -1},
		{TrafficMultiplier: 1001},
		{TrafficMultiplier: 1, AllowedExitGroupIDs: []string{"a", " a "}},
		{TrafficMultiplier: 1, Revision: math.MaxInt64},
	}
	for index, request := range cases {
		if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("case %d accepted", index)
		}
	}
	repo := &captureRepository{}
	_, _, err := NewService(repo, nil).Update(context.Background(), "admin", "entry-1", Request{TrafficMultiplier: 1, AllowedExitGroupIDs: []string{"entry-1"}}, "key")
	if !errors.Is(err, faults.ErrValidation) || repo.calls != 0 {
		t.Fatal("self exit reference reached repository")
	}
	if _, err := NormalizeRequest(Request{TrafficMultiplier: 0}); err != nil {
		t.Fatal("zero multiplier and EXIT-only zero range must be supported; group type is repository-validated")
	}
}

func TestNormalizeMultipleRangesAndNetworkAuthorization(t *testing.T) {
	request := Request{
		ConnectHost:         "entry.example.test",
		PortRanges:          []PortRange{{Start: 13000, End: 13002}, {Start: 12003, End: 12005}, {Start: 12000, End: 12002}},
		AllowedUserGroupIDs: []string{"users-b", "users-a"},
		AllowedExitGroupIDs: []string{"exit-b", "exit-a"},
		FallbackExitGroupID: "exit-b",
		TrafficMultiplier:   1,
	}
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.PortStart != 12000 || normalized.PortEnd != 13002 || len(normalized.PortRanges) != 2 || normalized.PortRanges[0] != (PortRange{Start: 12000, End: 12005}) {
		t.Fatalf("unexpected port range normalization: %+v", normalized.PortRanges)
	}
	if normalized.AllowedUserGroupIDs[0] != "users-a" || normalized.AllowedExitGroupIDs[0] != "exit-a" {
		t.Fatalf("authorization identifiers were not canonical: %+v %+v", normalized.AllowedUserGroupIDs, normalized.AllowedExitGroupIDs)
	}
	if request.PortRanges[0].Start != 13000 || request.AllowedUserGroupIDs[0] != "users-b" {
		t.Fatal("normalization mutated caller slices")
	}

	invalid := []Request{
		{PortRanges: []PortRange{{Start: 100, End: 99}}, TrafficMultiplier: 1},
		{AllowedUserGroupIDs: []string{"users", "users"}, TrafficMultiplier: 1},
		{AllowedEntryGroupIDs: []string{"bad id"}, TrafficMultiplier: 1},
		{AllowedExitGroupIDs: []string{"exit-a"}, FallbackExitGroupID: "exit-b", TrafficMultiplier: 1},
	}
	for index, item := range invalid {
		if _, err := NormalizeRequest(item); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("invalid authorization case %d accepted: %v", index, err)
		}
	}
}
