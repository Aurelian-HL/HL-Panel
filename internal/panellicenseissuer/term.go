package panellicenseissuer

import (
	p "github.com/hongle/hl-panel/internal/panellicense"
	"time"
)

// FixedZone avoids depending on host tzdata while retaining Shanghai business time.
var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

func addTerm(base time.Time, term p.Term) (*time.Time, error) {
	if err := term.Validate(); err != nil {
		return nil, invalid("invalid license term")
	}
	var end time.Time
	switch term.Kind {
	case "perpetual":
		return nil, nil
	case "days":
		end = base.Add(time.Duration(term.Days) * 24 * time.Hour)
	case "until":
		end = term.ExpiresAt.UTC()
	case "calendar_months":
		local := base.In(shanghai)
		first := time.Date(local.Year(), local.Month()+time.Month(term.Months), 1, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), shanghai)
		lastDay := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, shanghai).Day()
		day := local.Day()
		if day > lastDay {
			day = lastDay
		}
		end = time.Date(first.Year(), first.Month(), day, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), shanghai)
	}
	end = end.UTC()
	return &end, nil
}

func extendTerm(record *p.LicenseRecord, term p.Term, now time.Time) error {
	if err := term.Validate(); err != nil {
		return invalid("invalid license term")
	}
	if record.RevokedAt != nil {
		return ErrRevoked
	}
	if record.Term.Kind == "perpetual" {
		return problem("operation_not_allowed", "perpetual licenses cannot be shortened", 409)
	}
	if record.ActivatedAt == nil {
		switch {
		case term.Kind == "perpetual":
			record.Term = term
			record.LicenseExpiresAt = nil
		case term.Kind == "until" && record.Term.Kind == "until" && term.ExpiresAt.After(*record.Term.ExpiresAt):
			record.Term = term
			record.LicenseExpiresAt = term.ExpiresAt
		case term.Kind == "days" && record.Term.Kind == "days":
			term.Days += record.Term.Days
			if term.Validate() != nil {
				return invalid("combined pending term exceeds limit")
			}
			record.Term = term
		case term.Kind == "calendar_months" && record.Term.Kind == "calendar_months":
			term.Months += record.Term.Months
			if term.Validate() != nil {
				return invalid("combined pending term exceeds limit")
			}
			record.Term = term
		default:
			return problem("operation_not_allowed", "pending licenses require a matching longer term", 409)
		}
		return nil
	}
	base := now
	if record.LicenseExpiresAt != nil && record.LicenseExpiresAt.After(base) {
		base = *record.LicenseExpiresAt
	}
	end, err := addTerm(base, term)
	if err != nil {
		return err
	}
	if end != nil && !end.After(base) {
		return invalid("extension must move expiry forward")
	}
	record.Term = term
	record.LicenseExpiresAt = end
	return nil
}

func status(record p.LicenseRecord, now time.Time) string {
	if record.RevokedAt != nil {
		return "revoked"
	}
	if record.InstanceID == "" {
		return "pending"
	}
	if record.LicenseExpiresAt != nil && !now.Before(*record.LicenseExpiresAt) {
		return "expired"
	}
	return "active"
}
