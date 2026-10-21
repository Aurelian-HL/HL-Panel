package panellicense

import "errors"

const (
	TierFree     = "free"
	TierLicensed = "licensed"
)

// Limits caps the entire panel, across owners, protocols and enabled states.
// Zeroes are valid only for licensed panels; existing business quotas still apply.
type Limits struct {
	DeviceGroups int `json:"device_groups"`
	Rules        int `json:"rules"`
}

func LimitsForTier(tier string) (Limits, error) {
	switch tier {
	case TierFree:
		return Limits{DeviceGroups: 1, Rules: 10}, nil
	case TierLicensed:
		return Limits{}, nil
	default:
		return Limits{}, errors.New("unknown panel license tier")
	}
}

// Validate rejects missing tiers and forged or inconsistent entitlements.
// Only the authenticated issuer administration boundary may default an old
// issue request to licensed; a client must never default signed claims.
func (limits Limits) Validate(tier string) error {
	expected, err := LimitsForTier(tier)
	if err != nil || limits != expected {
		return errors.New("invalid panel license entitlements")
	}
	return nil
}

func ValidateEntitlements(tier string, limits Limits) error {
	return limits.Validate(tier)
}
