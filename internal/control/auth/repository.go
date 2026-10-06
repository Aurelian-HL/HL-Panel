package auth

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type Repository interface {
	AdministratorByUsername(context.Context, string) (Administrator, error)
	AdministratorByID(context.Context, string) (Administrator, error)
	UpdateAdministratorPassword(context.Context, string, []byte, []byte, audit.Event) error
	CreateSession(context.Context, Session, audit.Event) error
	SessionByTokenHash(context.Context, string, time.Time) (Session, error)
}
