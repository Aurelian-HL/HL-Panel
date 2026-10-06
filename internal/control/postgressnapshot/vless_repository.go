package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

var _ vlessidentity.Repository = (*Store)(nil)
var _ vlessruntime.Repository = (*Store)(nil)

func (s *Store) Provision(ctx context.Context, input vlessidentity.ProvisionInput, event audit.Event) (vlessidentity.Binding, bool, error) {
	type result struct {
		binding  vlessidentity.Binding
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		binding, replayed, err := state.Provision(ctx, input, event)
		return result{binding: binding, replayed: replayed}, err
	})
	return value.binding, value.replayed, err
}

func (s *Store) List(ctx context.Context) ([]vlessidentity.Binding, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]vlessidentity.Binding, error) {
		return state.List(ctx)
	})
}

func (s *Store) ListForAdministrator(ctx context.Context, administratorID string) ([]vlessidentity.Binding, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]vlessidentity.Binding, error) {
		return state.ListForAdministrator(ctx, administratorID)
	})
}

func (s *Store) CredentialForAdministrator(ctx context.Context, administratorID, ruleID string) (vlessidentity.CredentialRecord, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (vlessidentity.CredentialRecord, error) {
		return state.CredentialForAdministrator(ctx, administratorID, ruleID)
	})
}

func (s *Store) Rotate(ctx context.Context, input vlessidentity.RotateInput, event audit.Event) (vlessidentity.Binding, bool, error) {
	type result struct {
		binding  vlessidentity.Binding
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		binding, replayed, err := state.Rotate(ctx, input, event)
		return result{binding: binding, replayed: replayed}, err
	})
	return value.binding, value.replayed, err
}

func (s *Store) Revoke(ctx context.Context, input vlessidentity.RevokeInput, event audit.Event) (vlessidentity.Binding, bool, error) {
	type result struct {
		binding  vlessidentity.Binding
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		binding, replayed, err := state.Revoke(ctx, input, event)
		return result{binding: binding, replayed: replayed}, err
	})
	return value.binding, value.replayed, err
}

func (s *Store) ListPublicRuntimeMaterials(ctx context.Context) ([]vlessruntime.PublicMaterial, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]vlessruntime.PublicMaterial, error) {
		return state.ListPublicRuntimeMaterials(ctx)
	})
}

func (s *Store) ListPublicRuntimeMaterialsForAdministrator(ctx context.Context, administratorID string) ([]vlessruntime.PublicMaterial, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]vlessruntime.PublicMaterial, error) {
		return state.ListPublicRuntimeMaterialsForAdministrator(ctx, administratorID)
	})
}

func (s *Store) RuntimeMaterialForNode(ctx context.Context, nodeID, bindingID string) (vlessruntime.Material, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (vlessruntime.Material, error) {
		return state.RuntimeMaterialForNode(ctx, nodeID, bindingID)
	})
}

func (s *Store) SaveRuntimeMaterial(ctx context.Context, input vlessruntime.SaveInput, event audit.Event) (vlessruntime.PublicMaterial, bool, error) {
	type result struct {
		material vlessruntime.PublicMaterial
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		material, replayed, err := state.SaveRuntimeMaterial(ctx, input, event)
		return result{material: material, replayed: replayed}, err
	})
	return value.material, value.replayed, err
}

func (s *Store) RevokeRuntimeMaterial(ctx context.Context, input vlessruntime.RevokeInput, event audit.Event) (vlessruntime.PublicMaterial, bool, error) {
	type result struct {
		material vlessruntime.PublicMaterial
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		material, replayed, err := state.RevokeRuntimeMaterial(ctx, input, event)
		return result{material: material, replayed: replayed}, err
	})
	return value.material, value.replayed, err
}
