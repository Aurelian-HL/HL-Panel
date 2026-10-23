package engine

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Removing the final customer fragment must not also change process-wide
// settings inherited from that fragment. Keep a local, empty projection with
// those settings. The canonical desired configuration and its hash stay intact;
// Bundle is the native compiler input retained for validation and rollback.
func (a *XrayProcessAdapter) preserveIdleSettings(prepared *PreparedConfiguration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.process == nil || !a.process.Running() {
		return nil
	}
	if a.liveStateUncertain {
		return ErrLiveStateUncertain
	}
	active, exists, err := readOptionalRegularFile(a.activePath)
	if err != nil || !exists || a.processConfigSHA256 != sha256.Sum256(active) {
		return ErrLiveUpdateRequiresMaintenance
	}
	var document, routing map[string]json.RawMessage
	if json.Unmarshal(active, &document) != nil || document == nil ||
		json.Unmarshal(document["routing"], &routing) != nil || routing == nil {
		return ErrLiveUpdateRequiresMaintenance
	}
	document["inbounds"], document["outbounds"] = json.RawMessage(`[]`), json.RawMessage(`[]`)
	routing["rules"] = json.RawMessage(`[]`)
	document["routing"], err = json.Marshal(routing)
	if err != nil {
		return ErrInvalidConfiguration
	}
	// The compiler recreates its singleton API entries with the same settings.
	delete(document, "api")
	delete(document, "stats")
	config, err := json.Marshal(document)
	if err != nil {
		return ErrInvalidConfiguration
	}
	prepared.Bundle.Fragments = []agentv1.ConfigurationFragment{{
		GroupID: "local-idle-xray", GroupRevision: 1, Engine: agentv1.EngineXray, Config: config,
	}}
	return nil
}
