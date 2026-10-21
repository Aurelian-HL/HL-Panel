package panellicenseissuer

import (
	p "github.com/hongle/hl-panel/internal/panellicense"
	"testing"
	"time"
)

func TestChangeTierStartsNewTermAndPreservesActivation(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	activated := now.Add(-30 * 24 * time.Hour)
	r := p.LicenseRecord{ID: "lic_history", Domain: "free.example.test", InstanceID: "instance_history", Tier: p.TierFree,
		Limits: p.Limits{DeviceGroups: 1, Rules: 10}, Term: p.Term{Kind: "perpetual"}, ActivatedAt: &activated}
	if err := changeTier(&r, p.TierLicensed, p.Term{Kind: "days", Days: 7}, now); err != nil {
		t.Fatal(err)
	}
	if r.LicenseExpiresAt == nil || !r.LicenseExpiresAt.Equal(now.Add(7*24*time.Hour)) ||
		!r.ActivatedAt.Equal(activated) || r.ID != "lic_history" || r.InstanceID != "instance_history" || r.Domain != "free.example.test" || r.Limits != (p.Limits{}) {
		t.Fatal("upgrade changed binding/history or used original activation as term start")
	}
	if err := changeTier(&r, p.TierFree, p.Term{Kind: "perpetual"}, now); err != nil {
		t.Fatal(err)
	}
	if r.Tier != p.TierFree || r.LicenseExpiresAt != nil || r.Limits != (p.Limits{DeviceGroups: 1, Rules: 10}) || !r.ActivatedAt.Equal(activated) {
		t.Fatal("downgrade did not retain identity and fixed free entitlements")
	}
}

func TestChangeTierValidationAndPendingTerms(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	base := p.LicenseRecord{Tier: p.TierFree, Limits: p.Limits{DeviceGroups: 1, Rules: 10}, Term: p.Term{Kind: "perpetual"}}
	r := base
	if err := changeTier(&r, p.TierLicensed, p.Term{Kind: "calendar_months", Months: 1}, now); err != nil || r.LicenseExpiresAt != nil || r.ActivatedAt != nil {
		t.Fatal("pending relative term must wait for first activation", err)
	}
	for _, tc := range []struct {
		name, tier string
		term       p.Term
	}{
		{"same tier", p.TierFree, p.Term{Kind: "perpetual"}},
		{"unknown", "other", p.Term{Kind: "perpetual"}},
		{"past expiry", p.TierLicensed, p.Term{Kind: "until", ExpiresAt: &now}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			if err := changeTier(&r, tc.tier, tc.term, now); err == nil || r.Tier != base.Tier || r.Term != base.Term {
				t.Fatal("invalid change accepted or record mutated before validation")
			}
		})
	}
	r = base
	r.RevokedAt = &now
	if err := changeTier(&r, p.TierLicensed, p.Term{Kind: "perpetual"}, now); err == nil {
		t.Fatal("tier change revived revoked license")
	}
	r = p.LicenseRecord{Tier: p.TierLicensed, Term: p.Term{Kind: "perpetual"}}
	if err := changeTier(&r, p.TierLicensed, p.Term{Kind: "days", Days: 1}, now); err == nil {
		t.Fatal("same-tier change shortened perpetual formal license")
	}
}

func TestTrustedLegacyEntitlementsProjection(t *testing.T) {
	r := p.LicenseRecord{Term: p.Term{Kind: "perpetual"}}
	if err := normalizeStoredEntitlements(&r); err != nil || r.Tier != p.TierLicensed || r.Limits != (p.Limits{}) {
		t.Fatal("trusted pre-tier issuer record rejected", err)
	}
	for _, r := range []p.LicenseRecord{
		{Tier: "unknown", Term: p.Term{Kind: "perpetual"}},
		{Limits: p.Limits{Rules: 10}, Term: p.Term{Kind: "perpetual"}},
		{Tier: p.TierFree, Limits: p.Limits{}, Term: p.Term{Kind: "perpetual"}},
		{Tier: p.TierFree, Limits: p.Limits{DeviceGroups: 1, Rules: 10}, Term: p.Term{Kind: "days", Days: 1}},
	} {
		if err := normalizeStoredEntitlements(&r); err == nil {
			t.Fatal("malformed entitlement accepted as legacy")
		}
	}
}
