package memoryrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// RotateNativeSOCKS5Credentials changes only the selected SOCKS server's
// credentials. The normal native rule editor remains transport-immutable.
func (s *Store) RotateNativeSOCKS5Credentials(_ context.Context, in vlessruntime.NativeCredentialsRotation) (vlessruntime.NativeCredentialsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero vlessruntime.NativeCredentialsResult
	rule, ok := s.forwardRules[in.RuleID]
	admin := false
	for _, a := range s.adminsByUsername {
		if a.ID == in.AdministratorID {
			admin = true
		}
	}
	if !ok || !admin || !(rule.OwnedByAdministrator(in.AdministratorID) || s.importedRuleManagedByLocked(rule, in.AdministratorID)) {
		return zero, faults.ErrNotFound
	}
	if rule.VLESSOutboundMode != forwarding.VLESSOutboundNative || in.ExpectedRevision < 1 || len(in.MaterialRevisions) == 0 || len(in.MaterialRevisions) > 1000 || strings.TrimSpace(in.Host) == "" || in.Port < 1 || in.Port > 65535 || in.Username == "" || in.Password == "" || len(in.Username) > 255 || len(in.Password) > 255 || len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 200 {
		return zero, faults.ErrValidation
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	key := in.AdministratorID + "\x00native_socks5.rotate\x00" + in.IdempotencyKey
	if replay, err := s.replayBusinessLocked(key, hash, &zero); err != nil || replay {
		zero.Replayed = replay
		return zero, err
	}
	if rule.Revision != in.ExpectedRevision {
		return zero, faults.ErrConflict
	}
	before := map[string]vlessruntime.Material{}
	after := map[string]vlessruntime.Material{}
	now := time.Now().UTC()
	for id, material := range s.vlessRuntimeMaterials {
		if material.ForwardingRuleID != rule.ID || material.State != vlessruntime.StateActive {
			continue
		}
		if in.MaterialRevisions[id] != material.Revision {
			return zero, faults.ErrConflict
		}
		updated := cloneVLESSRuntimeMaterial(material)
		config, err := rotateNativeSOCKS5JSON(material.NativeConfig, in.Host, in.Port, in.Username, in.Password)
		if err != nil {
			return zero, err
		}
		updated.NativeConfig = config
		updated.Revision++
		updated.UpdatedAt = now
		// Retired/moved members retain historical material for rejoining. Rotate
		// that material too, without requiring current scheduling eligibility or
		// granting it access to a desired configuration or secret read.
		if err := s.validatePersistedVLESSRuntimeMaterialLocked(updated, nil); err != nil {
			return zero, faults.ErrValidation
		}
		before[id], after[id] = material, updated
	}
	if len(after) == 0 || len(after) != len(in.MaterialRevisions) {
		return zero, faults.ErrConflict
	}
	result := vlessruntime.NativeCredentialsResult{RuleID: rule.ID, Revision: rule.Revision + 1, Materials: len(after)}
	event, err := audit.NewEvent(now, "administrator", in.AdministratorID, "vless_native.socks5_credentials_rotated", "forwarding_rule", rule.ID, "success", map[string]any{"revision": result.Revision, "materials": result.Materials})
	if err != nil {
		return zero, err
	}
	if err = s.recordBusinessLocked(key, hash, rule.ID, result); err != nil {
		return zero, err
	}
	for id, m := range after {
		s.vlessRuntimeMaterials[id] = m
	}
	updatedRule := rule
	updatedRule.Revision = result.Revision
	updatedRule.RuntimeRevision = updatedRule.Revision
	updatedRule.UpdatedAt = now
	s.forwardRules[rule.ID] = updatedRule
	if err = s.recompileForwardingGroupsLocked([]string{rule.EntryGroupID}, now); err != nil {
		for id, m := range before {
			s.vlessRuntimeMaterials[id] = m
		}
		s.forwardRules[rule.ID] = rule
		delete(s.businessIdempotency, key)
		return zero, err
	}
	s.appendAuditLocked(event)
	return result, nil
}

// RawMessage avoids converting unrelated large counters to float64. Every
// other field (especially ingress identities, keys and routing) is preserved.
func rotateNativeSOCKS5JSON(raw json.RawMessage, host string, port int, username, password string) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, faults.ErrValidation
	}
	var outs []map[string]json.RawMessage
	if json.Unmarshal(doc["outbounds"], &outs) != nil {
		return nil, faults.ErrValidation
	}
	matches := 0
	for _, out := range outs {
		var protocol string
		json.Unmarshal(out["protocol"], &protocol)
		if protocol != "socks" {
			continue
		}
		var settings map[string]json.RawMessage
		if json.Unmarshal(out["settings"], &settings) != nil {
			return nil, faults.ErrValidation
		}
		var servers []map[string]json.RawMessage
		if json.Unmarshal(settings["servers"], &servers) != nil {
			return nil, faults.ErrValidation
		}
		for _, server := range servers {
			var address string
			var serverPort int
			json.Unmarshal(server["address"], &address)
			json.Unmarshal(server["port"], &serverPort)
			if address != host || serverPort != port {
				continue
			}
			var users []map[string]json.RawMessage
			if json.Unmarshal(server["users"], &users) != nil || len(users) != 1 {
				return nil, faults.ErrConflict
			}
			users[0]["user"], _ = json.Marshal(username)
			users[0]["pass"], _ = json.Marshal(password)
			server["users"], _ = json.Marshal(users)
			matches++
		}
		settings["servers"], _ = json.Marshal(servers)
		out["settings"], _ = json.Marshal(settings)
	}
	if matches != 1 {
		return nil, faults.ErrConflict
	}
	doc["outbounds"], _ = json.Marshal(outs)
	return json.Marshal(doc)
}
