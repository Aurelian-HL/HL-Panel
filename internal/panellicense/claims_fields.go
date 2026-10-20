package panellicense

import (
	"encoding/json"
	"errors"
	"strings"
)

// claimsEntitlementFields is called only after signature verification. Value
// structs alone cannot distinguish omitted/null limits from licensed zeroes.
// Only a cache with both fields entirely absent is a recoverable old identity.
func claimsEntitlementFields(envelope Envelope) (legacy bool, err error) {
	var fields map[string]json.RawMessage
	if err := Decode(envelope.Payload, &fields); err != nil {
		return false, err
	}
	for name := range fields {
		if (strings.EqualFold(name, "tier") && name != "tier") || (strings.EqualFold(name, "limits") && name != "limits") {
			return false, errors.New("noncanonical signed entitlement fields")
		}
	}
	tierRaw, hasTier := fields["tier"]
	limitsRaw, hasLimits := fields["limits"]
	if !hasTier && !hasLimits {
		return true, nil
	}
	var tier *string
	var limits map[string]json.RawMessage
	if !hasTier || !hasLimits || json.Unmarshal(tierRaw, &tier) != nil || tier == nil || *tier == "" || json.Unmarshal(limitsRaw, &limits) != nil || limits == nil || len(limits) != 2 {
		return false, errors.New("incomplete signed license entitlements")
	}
	for _, name := range []string{"device_groups", "rules"} {
		var value *int
		raw, exists := limits[name]
		if !exists || json.Unmarshal(raw, &value) != nil || value == nil {
			return false, errors.New("incomplete signed license limit")
		}
	}
	return false, nil
}
