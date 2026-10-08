package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type failingRepository struct {
	auth.Repository
	err error
}

func (r failingRepository) AdministratorByID(context.Context, string) (auth.Administrator, error) {
	return auth.Administrator{}, r.err
}

func (r failingRepository) AdministratorByUsername(context.Context, string) (auth.Administrator, error) {
	return auth.Administrator{}, r.err
}

func (r failingRepository) SessionByTokenHash(context.Context, string, time.Time) (auth.Session, error) {
	return auth.Session{AdminID: "admin"}, nil
}

func TestRepositoryOutageDoesNotInvalidateSessionOrPassword(t *testing.T) {
	outage := errors.New("database unavailable")
	service := auth.NewService(failingRepository{err: outage}, nil, time.Now, time.Hour)
	if _, err := service.Authenticate(t.Context(), "existing-token"); !errors.Is(err, outage) || errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("authentication hid repository failure: %v", err)
	}
	if _, err := service.Login(t.Context(), "admin", "password"); !errors.Is(err, outage) {
		t.Fatalf("login hid repository failure: %v", err)
	}
	if _, err := service.ConfirmAdministratorPassword(t.Context(), "admin", "password"); !errors.Is(err, outage) {
		t.Fatalf("confirmation hid repository failure: %v", err)
	}
}

func TestMissingAdministratorStillRejectsSession(t *testing.T) {
	service := auth.NewService(failingRepository{err: faults.ErrNotFound}, nil, time.Now, time.Hour)
	if _, err := service.Authenticate(t.Context(), "existing-token"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("missing administrator accepted: %v", err)
	}
}
