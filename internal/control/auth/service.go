package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/securetoken"
)

type Service struct {
	repository   Repository
	auditService *audit.Service
	now          func() time.Time
	sessionTTL   time.Duration
}

// ConfirmAdministratorPassword returns a confidential concurrency guard for
// privileged backup/restore; it must never be encoded in an HTTP response.
func (s *Service) ConfirmAdministratorPassword(ctx context.Context, id, password string) ([]byte, error) {
	if password == "" || len(password) > 4096 {
		return nil, fmt.Errorf("%w: 请填写当前管理员密码", faults.ErrValidation)
	}
	a, err := s.repository.AdministratorByID(ctx, id)
	if err != nil && !errors.Is(err, faults.ErrNotFound) {
		return nil, err
	}
	if err != nil || checkPassword(a.PasswordHash, password) != nil {
		return nil, fmt.Errorf("%w: 当前管理员密码错误", faults.ErrValidation)
	}
	return append([]byte(nil), a.PasswordHash...), nil
}

func (s *Service) CurrentAdministrator(ctx context.Context, adminID string) (AdministratorView, error) {
	admin, err := s.repository.AdministratorByID(ctx, adminID)
	if err != nil {
		return AdministratorView{}, err
	}
	return AdministratorView{ID: admin.ID, Username: admin.Username, CreatedAt: admin.CreatedAt, MustChangePassword: admin.MustChangePassword}, nil
}

func (s *Service) ChangePassword(ctx context.Context, adminID, currentPassword, newPassword string) error {
	if currentPassword == "" || len(newPassword) < 8 || len(newPassword) > 256 {
		return fmt.Errorf("%w: 请填写当前密码，新密码需为 8 到 256 个字符", faults.ErrValidation)
	}
	if currentPassword == newPassword {
		return fmt.Errorf("%w: 新密码不能与当前密码相同", faults.ErrValidation)
	}
	admin, err := s.repository.AdministratorByID(ctx, adminID)
	if err != nil {
		return err
	}
	if checkPassword(admin.PasswordHash, currentPassword) != nil {
		return fmt.Errorf("%w: 当前密码错误", faults.ErrValidation)
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	event, err := audit.NewEvent(s.now().UTC(), "administrator", adminID, "auth.password.change", "administrator", adminID, "succeeded", nil)
	if err != nil {
		return err
	}
	return s.repository.UpdateAdministratorPassword(ctx, adminID, admin.PasswordHash, newHash, false, event)
}

func NewService(repository Repository, auditService *audit.Service, now func() time.Time, sessionTTL time.Duration) *Service {
	if now == nil {
		now = time.Now
	}
	if sessionTTL <= 0 {
		sessionTTL = 8 * time.Hour
	}
	return &Service{repository: repository, auditService: auditService, now: now, sessionTTL: sessionTTL}
}

func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	now := s.now().UTC()
	admin, err := s.repository.AdministratorByUsername(ctx, username)
	if err != nil && !errors.Is(err, faults.ErrNotFound) && !errors.Is(err, faults.ErrUnauthorized) {
		return LoginResult{}, err
	}
	if err != nil || checkPassword(admin.PasswordHash, password) != nil {
		event, eventErr := audit.NewEvent(now, "administrator", username, "auth.login", "session", "", "failed", nil)
		if eventErr == nil && s.auditService != nil {
			_ = s.auditService.Record(ctx, event)
		}
		return LoginResult{}, faults.ErrUnauthorized
	}
	rawToken, err := securetoken.Generate("adm")
	if err != nil {
		return LoginResult{}, err
	}
	sessionID, err := idgen.New("ses")
	if err != nil {
		return LoginResult{}, err
	}
	session := Session{
		ID:        sessionID,
		AdminID:   admin.ID,
		TokenHash: securetoken.Hash(rawToken),
		ExpiresAt: now.Add(s.sessionTTL),
		CreatedAt: now,
	}
	event, err := audit.NewEvent(now, "administrator", admin.ID, "auth.login", "session", session.ID, "succeeded", nil)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.repository.CreateSession(ctx, session, event); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		AccessToken: rawToken,
		ExpiresAt:   session.ExpiresAt,
		User: AdministratorView{
			ID:                 admin.ID,
			Username:           admin.Username,
			CreatedAt:          admin.CreatedAt,
			MustChangePassword: admin.MustChangePassword,
		},
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (Session, error) {
	if rawToken == "" {
		return Session{}, faults.ErrUnauthorized
	}
	session, err := s.repository.SessionByTokenHash(ctx, securetoken.Hash(rawToken), s.now().UTC())
	if err != nil {
		if errors.Is(err, faults.ErrNotFound) {
			return Session{}, faults.ErrUnauthorized
		}
		return Session{}, err
	}
	admin, err := s.repository.AdministratorByID(ctx, session.AdminID)
	if err != nil {
		if errors.Is(err, faults.ErrNotFound) {
			return Session{}, faults.ErrUnauthorized
		}
		return Session{}, err
	}
	session.MustChangePassword = admin.MustChangePassword
	return session, nil
}
