package siteconfig

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type UpdateInput struct {
	Settings         Settings
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	UpdatedBy        string
}

// Repository commits settings, idempotency state, and the audit event atomically.
type Repository interface {
	SiteSettings(context.Context) (Settings, error)
	UpdateSiteSettings(context.Context, UpdateInput, audit.Event) (Settings, bool, error)
}
