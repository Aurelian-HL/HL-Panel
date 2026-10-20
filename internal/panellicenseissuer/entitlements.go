package panellicenseissuer

import (
	p "github.com/hongle/hl-panel/internal/panellicense"
	"time"
)

// A tier change starts a new entitlement term while retaining the original
// activation identity and history. Renewal semantics belong to extendTerm.
func changeTier(record *p.LicenseRecord, tier string, term p.Term, now time.Time) error {
	if record.RevokedAt != nil {
		return ErrRevoked
	}
	if record.Tier == tier {
		return problem("operation_not_allowed", "license already has this tier", 409)
	}
	limits, err := validateTierTerm(tier, term)
	if err != nil {
		return err
	}
	if term.Kind == "until" && !term.ExpiresAt.After(now) {
		return invalid("expiry must be in the future")
	}
	var end *time.Time
	if record.ActivatedAt != nil || term.Kind == "until" {
		end, err = addTerm(now, term)
		if err != nil {
			return err
		}
	}
	record.Tier, record.Limits, record.Term = tier, limits, term
	record.LicenseExpiresAt = end
	return nil
}

func validateTierTerm(tier string, term p.Term) (p.Limits, error) {
	limits, err := p.LimitsForTier(tier)
	if err != nil || term.Validate() != nil || tier == p.TierFree && term.Kind != "perpetual" {
		return p.Limits{}, invalid("free registration requires a perpetual term and fixed panel limits")
	}
	return limits, nil
}

// Legacy normalization applies only to trusted issuer records created before
// tiers existed. Never use it on signed claims at the panel or user input.
// Existing replay documents are left immutable; no domain, identity or code is
// replaced and the next audited mutation persists the explicit entitlements.
func normalizeStoredEntitlements(record *p.LicenseRecord) error {
	if record.Tier == "" && record.Limits == (p.Limits{}) {
		record.Tier = p.TierLicensed
	}
	if err := record.Limits.Validate(record.Tier); err != nil {
		return err
	}
	_, err := validateTierTerm(record.Tier, record.Term)
	return err
}
