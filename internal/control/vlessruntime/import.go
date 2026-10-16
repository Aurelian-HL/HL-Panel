package vlessruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/idgen"
)

// BindImported is a maintenance-only in-process operation. It is not exposed
// through HTTP. Repository validation requires imported binding provenance and
// exact agreement between native credentials, port and the authoritative rule.
func (s *Service) BindImported(ctx context.Context, administratorID, bindingID, nodeID string, raw []byte, key string) (PublicMaterial, bool, error) {
	if !validIdentifier(administratorID) || !validIdentifier(bindingID) || !validIdentifier(nodeID) || !validIdempotencyKey(key) {
		return PublicMaterial{}, false, fmt.Errorf("%w: invalid imported runtime references", faults.ErrValidation)
	}
	profile, err := importedconfig.InspectNative(raw)
	if err != nil {
		return PublicMaterial{}, false, fmt.Errorf("%w: invalid imported runtime configuration", faults.ErrValidation)
	}
	id, err := idgen.New("vrm")
	if err != nil {
		return PublicMaterial{}, false, err
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "local_operator", administratorID, "vless_runtime.bind", "vless_runtime_material", id, "succeeded", map[string]any{"binding_id": bindingID, "node_id": nodeID, "revision": 1})
	if err != nil {
		return PublicMaterial{}, false, err
	}
	return s.repository.SaveRuntimeMaterial(ctx, SaveInput{ID: id, BindingID: bindingID, NodeID: nodeID, RealityPrivateKey: profile.PrivateKey, NativeConfig: profile.Config, IdempotencyKey: key, RequestSHA256: digest("import-native", bindingID, nodeID, strings.TrimSpace(string(raw))), CreatedBy: administratorID, CreatedAt: now}, event)
}
