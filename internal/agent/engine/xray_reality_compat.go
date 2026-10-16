package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Adapt legacy LKG fragments at execution time without changing their signed
// generation/hash or credentials. Xray 26.7.28 treats an omitted/empty minimum
// as 26.3.27; third-party clients use unrelated version numbers. Explicit
// administrator restrictions must survive this compatibility default.
func compatibleRealityInbound(raw json.RawMessage) (json.RawMessage, error) {
	var inbound map[string]json.RawMessage
	if err := decodeJSON(raw, &inbound); err != nil || inbound == nil {
		return nil, fmt.Errorf("%w: invalid inbound", ErrInvalidConfiguration)
	}
	var tag string
	if json.Unmarshal(inbound["tag"], &tag) == nil && strings.HasPrefix(tag, agentv1.NativeRealityStatsTagPrefix) {
		return raw, nil
	}
	if len(inbound["streamSettings"]) == 0 {
		return raw, nil
	}
	var stream map[string]json.RawMessage
	if err := decodeJSON(inbound["streamSettings"], &stream); err != nil || stream == nil {
		return nil, fmt.Errorf("%w: invalid inbound stream settings", ErrInvalidConfiguration)
	}
	var security string
	if json.Unmarshal(stream["security"], &security) != nil || security != "reality" {
		return raw, nil
	}
	var reality map[string]json.RawMessage
	if err := decodeJSON(stream["realitySettings"], &reality); err != nil || reality == nil {
		return nil, fmt.Errorf("%w: invalid Reality settings", ErrInvalidConfiguration)
	}
	if minimum, exists := reality["minClientVer"]; exists {
		var value string
		if json.Unmarshal(minimum, &value) != nil {
			return nil, fmt.Errorf("%w: invalid Reality minimum version", ErrInvalidConfiguration)
		}
		if value != "" {
			return raw, nil
		}
	}
	reality["minClientVer"] = json.RawMessage(`"0.0.0"`)
	encoded, err := json.Marshal(reality)
	if err != nil {
		return nil, err
	}
	stream["realitySettings"] = encoded
	encoded, err = json.Marshal(stream)
	if err != nil {
		return nil, err
	}
	inbound["streamSettings"] = encoded
	return json.Marshal(inbound)
}
