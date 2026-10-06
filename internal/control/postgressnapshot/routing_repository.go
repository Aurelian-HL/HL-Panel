package postgressnapshot

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

var _ groups.Repository = (*Store)(nil)
var _ endpoints.Repository = (*Store)(nil)
var _ generations.Repository = (*Store)(nil)

func (s *Store) CreateDeviceGroup(ctx context.Context, group groups.DeviceGroup, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.CreateDeviceGroup(ctx, group, event) })
}

func (s *Store) UpdateDeviceGroup(ctx context.Context, input groups.UpdateInput, event audit.Event) (groups.DeviceGroup, bool, error) {
	type result struct {
		group    groups.DeviceGroup
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		group, replayed, err := state.UpdateDeviceGroup(ctx, input, event)
		return result{group: group, replayed: replayed}, err
	})
	return value.group, value.replayed, err
}

func (s *Store) DeleteDeviceGroup(ctx context.Context, input groups.DeleteInput, event audit.Event) (bool, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (bool, error) {
		return state.DeleteDeviceGroup(ctx, input, event)
	})
}

func (s *Store) ListDeviceGroups(ctx context.Context) ([]groups.DeviceGroup, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]groups.DeviceGroup, error) { return state.ListDeviceGroups(ctx) })
}

func (s *Store) ListGroupMembers(ctx context.Context, groupID string) ([]groups.Member, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]groups.Member, error) { return state.ListGroupMembers(ctx, groupID) })
}

func (s *Store) UpsertGroupMember(ctx context.Context, member groups.Member, event audit.Event) (groups.Member, []generations.NodeConfigGeneration, error) {
	type result struct {
		member  groups.Member
		configs []generations.NodeConfigGeneration
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		member, configs, err := state.UpsertGroupMember(ctx, member, event)
		return result{member, configs}, err
	})
	return value.member, value.configs, err
}

func (s *Store) UpdateGroupMemberWeight(ctx context.Context, input groups.UpdateMemberWeightInput, event audit.Event) (groups.UpdateMemberWeightResult, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (groups.UpdateMemberWeightResult, error) {
		return state.UpdateGroupMemberWeight(ctx, input, event)
	})
}

func (s *Store) RetireGroupMember(ctx context.Context, groupID, nodeID string, retiredAt time.Time, event audit.Event) (groups.RetireMemberResult, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (groups.RetireMemberResult, error) {
		return state.RetireGroupMember(ctx, groupID, nodeID, retiredAt, event)
	})
}

func (s *Store) CreateEndpointPool(ctx context.Context, input endpoints.CreatePoolInput, event audit.Event) (endpoints.EndpointPool, bool, error) {
	type result struct {
		pool     endpoints.EndpointPool
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		pool, replayed, err := state.CreateEndpointPool(ctx, input, event)
		return result{pool, replayed}, err
	})
	return value.pool, value.replayed, err
}

func (s *Store) DeleteEndpointPool(ctx context.Context, input endpoints.DeletePoolInput, event audit.Event) (bool, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (bool, error) {
		return state.DeleteEndpointPool(ctx, input, event)
	})
}

func (s *Store) ListEndpointPools(ctx context.Context) ([]endpoints.EndpointPool, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]endpoints.EndpointPool, error) { return state.ListEndpointPools(ctx) })
}

func (s *Store) EndpointPool(ctx context.Context, id string) (endpoints.EndpointPool, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (endpoints.EndpointPool, error) { return state.EndpointPool(ctx, id) })
}

func (s *Store) EndpointPoolMembers(ctx context.Context, id string) ([]endpoints.EndpointPoolMember, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]endpoints.EndpointPoolMember, error) {
		return state.EndpointPoolMembers(ctx, id)
	})
}

func (s *Store) AddEndpointPoolMember(ctx context.Context, input endpoints.AddMemberInput, event audit.Event) (endpoints.EndpointPoolMember, bool, error) {
	type result struct {
		member   endpoints.EndpointPoolMember
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		member, replayed, err := state.AddEndpointPoolMember(ctx, input, event)
		return result{member, replayed}, err
	})
	return value.member, value.replayed, err
}

func (s *Store) CreateGroupRevision(ctx context.Context, input generations.CreateGroupRevisionInput, event audit.Event) (generations.CreateGroupRevisionResult, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (generations.CreateGroupRevisionResult, error) {
		return state.CreateGroupRevision(ctx, input, event)
	})
}

func (s *Store) DesiredNodeConfig(ctx context.Context, id string) (generations.NodeConfigGeneration, int64, error) {
	type result struct {
		config  generations.NodeConfigGeneration
		applied int64
	}
	value, err := transact(ctx, s, false, func(state *memoryrepo.Store) (result, error) {
		config, applied, err := state.DesiredNodeConfig(ctx, id)
		return result{config, applied}, err
	})
	return value.config, value.applied, err
}

func (s *Store) RecordApplyResult(ctx context.Context, input generations.ApplyResult, event audit.Event) (nodes.Node, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (nodes.Node, error) { return state.RecordApplyResult(ctx, input, event) })
}
