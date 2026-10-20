package panellicense

import "testing"

func TestEntitlementsRejectMissingUnknownAndMismatchedLimits(t *testing.T) {
	for _, tc := range []struct {
		tier   string
		limits Limits
	}{
		{"", Limits{}}, {"unknown", Limits{}}, {"FREE", Limits{1, 10}},
		{TierFree, Limits{}}, {TierFree, Limits{1, 11}}, {TierFree, Limits{2, 10}},
		{TierFree, Limits{-1, 10}}, {TierLicensed, Limits{1, 10}},
	} {
		if tc.limits.Validate(tc.tier) == nil {
			t.Fatalf("invalid entitlements accepted: %+v", tc)
		}
	}
	for _, tier := range []string{TierFree, TierLicensed} {
		limits, err := LimitsForTier(tier)
		if err != nil || limits.Validate(tier) != nil {
			t.Fatalf("canonical entitlements rejected for %q", tier)
		}
	}
}
