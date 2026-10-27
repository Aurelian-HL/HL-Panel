package memoryrepo

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/idgen"
)

// Imported identities belong to the service endpoint. Joining its group must
// provision the same validated import on the new member, just as automatic
// Reality rules do. Persist a separate record so node-specific revocation and
// subsequent credential rotations retain their normal semantics. Callers own
// the mutation snapshot/transaction and establish endpoint membership first.
func (s *Store) provisionMemberNativeMaterialsLocked(member groups.Member) (bool, error) {
	if member.RetiredAt != nil {
		return false, nil
	}
	byBinding := make(map[string][]vlessruntime.Material)
	for _, material := range s.vlessRuntimeMaterials {
		byBinding[material.BindingID] = append(byBinding[material.BindingID], material)
	}
	ids := make([]string, 0)
	for id, binding := range s.vlessBindings {
		rule, exists := s.forwardRules[binding.Binding.ForwardingRuleID]
		if exists && rule.EntryGroupID == member.GroupID && rule.VLESSOutboundMode == forwarding.VLESSOutboundNative &&
			binding.Binding.State == vlessidentity.StateActive && binding.Binding.ImportedFrom != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	changed := false
	for _, bindingID := range ids {
		materials := byBinding[bindingID]
		hasLocal := false
		for _, material := range materials {
			if material.NodeID == member.NodeID {
				// An explicit local record, including a revocation, always wins.
				hasLocal = true
				break
			}
		}
		if hasLocal {
			continue
		}
		if err := s.validateVLESSCredentialRecordLocked(s.vlessBindings[bindingID]); err != nil {
			return false, fmt.Errorf("%w: invalid imported identity for group member", faults.ErrConflict)
		}
		liveSources := make([]vlessruntime.Material, 0, len(materials))
		retainedSources := make([]vlessruntime.Material, 0, len(materials))
		for _, material := range materials {
			if material.State != vlessruntime.StateActive {
				continue
			}
			retainedSources = append(retainedSources, material)
			peer, exists := s.membersByGroup[member.GroupID][material.NodeID]
			node, nodeExists := s.nodes[material.NodeID]
			if !exists || peer.RetiredAt != nil || !nodeExists || node.DeletedAt != nil {
				continue
			}
			liveSources = append(liveSources, material)
		}
		// The import belongs to the service, including while its group is empty.
		// Prefer current peers; only fall back to retained, non-revoked material
		// when no live source exists. Never restore the old peer's authorization.
		sources := liveSources
		if len(sources) == 0 {
			sources = retainedSources
		}
		var source *vlessruntime.Material
		var canonical []byte
		for _, material := range sources {
			var err error
			if len(liveSources) == 0 {
				err = s.validatePersistedVLESSRuntimeMaterialLocked(material, nil)
			} else {
				err = s.validateVLESSRuntimeMaterialLocked(material)
			}
			if err != nil {
				return false, fmt.Errorf("%w: invalid imported material for group member", faults.ErrConflict)
			}
			raw, err := generations.CanonicalJSON(material.NativeConfig)
			if err != nil {
				return false, fmt.Errorf("%w: invalid imported configuration", faults.ErrConflict)
			}
			if source != nil && !bytes.Equal(canonical, raw) {
				return false, fmt.Errorf("%w: group imported materials differ; reconcile before adding a member", faults.ErrConflict)
			}
			if source == nil || material.ID < source.ID {
				value := material
				source, canonical = &value, raw
			}
		}
		if source == nil {
			continue
		}
		id, err := idgen.New("vrm")
		if err != nil {
			return false, err
		}
		material := cloneVLESSRuntimeMaterial(*source)
		material.ID, material.NodeID, material.Revision = id, member.NodeID, 1
		material.CreatedAt, material.UpdatedAt = member.UpdatedAt.UTC(), member.UpdatedAt.UTC()
		if err := s.validateVLESSRuntimeMaterialLocked(material); err != nil {
			return false, err
		}
		event, err := audit.NewEvent(member.UpdatedAt, "system", "device_group", "vless_runtime.member_provision",
			"vless_runtime_material", id, "success", map[string]any{
				"group_id": member.GroupID, "node_id": member.NodeID, "binding_id": bindingID, "source_material_id": source.ID,
			})
		if err != nil {
			return false, err
		}
		s.vlessRuntimeMaterials[id] = material
		s.appendAuditLocked(event)
		changed = true
	}
	return changed, nil
}
