package memoryrepo

import (
	"encoding/json"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Canonicalize each engine document independently, then encode the small wire
// envelopes in lexical field order. This preserves the exact canonical bytes
// without decoding a second, complete copy of every engine document at once.
// Contract tests compare against CanonicalJSON on the public protocol types.
func canonicalNodeBundle(bundle agentv1.ConfigurationBundle) (json.RawMessage, error) {
	type fragment struct {
		Config          json.RawMessage       `json:"config"`
		Engine          agentv1.Engine        `json:"engine"`
		GroupGeneration agentv1.GroupRevision `json:"group_generation"`
		GroupID         string                `json:"group_id"`
		Usage           json.RawMessage       `json:"usage,omitempty"`
	}
	var fragments []fragment
	if bundle.Fragments != nil {
		fragments = make([]fragment, len(bundle.Fragments))
	}
	for i, input := range bundle.Fragments {
		raw := input.Config
		if len(raw) == 0 {
			// Match json.Marshal's nil RawMessage behavior. A non-nil empty
			// RawMessage is invalid JSON and must still fail validation.
			if raw == nil {
				raw = json.RawMessage("null")
			}
		}
		config, err := generations.CanonicalJSON(raw)
		if err != nil {
			return nil, err
		}
		var usage json.RawMessage
		if input.Usage != nil {
			raw, err := json.Marshal(input.Usage)
			if err != nil {
				return nil, err
			}
			usage, err = generations.CanonicalJSON(raw)
			if err != nil {
				return nil, err
			}
		}
		fragments[i] = fragment{Config: config, Engine: input.Engine, GroupGeneration: input.GroupRevision, GroupID: input.GroupID, Usage: usage}
	}
	return json.Marshal(struct {
		Fragments     []fragment `json:"fragments"`
		SchemaVersion int        `json:"schema_version"`
	}{Fragments: fragments, SchemaVersion: bundle.SchemaVersion})
}
