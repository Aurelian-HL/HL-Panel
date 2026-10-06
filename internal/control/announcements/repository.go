package announcements

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type SaveInput struct {
	Announcement     Announcement
	ExpectedRevision int64
	IdempotencyKey   string
	RequestSHA256    string
	UpdatedBy        string
}

type Repository interface {
	ListAnnouncements(context.Context) ([]Announcement, error)
	Announcement(context.Context, string) (Announcement, error)
	SaveAnnouncement(context.Context, SaveInput, audit.Event) (Announcement, bool, error)
}
