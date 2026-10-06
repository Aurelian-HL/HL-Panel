package siteconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) Get(ctx context.Context) (Settings, error) {
	return s.repository.SiteSettings(ctx)
}

func (s *Service) Update(ctx context.Context, administratorID string, request Request, idempotencyKey string) (Settings, bool, error) {
	request, err := NormalizeRequest(request)
	if err != nil {
		return Settings{}, false, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 || strings.IndexFunc(idempotencyKey, func(r rune) bool { return r < 0x20 }) >= 0 {
		return Settings{}, false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 printable characters", faults.ErrValidation)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return Settings{}, false, err
	}
	digest := sha256.Sum256(raw)
	now := s.now().UTC()
	settings := Settings{
		ID: GlobalSettingsID, SiteName: request.SiteName, PanelTitle: request.PanelTitle,
		PublicDescription: request.PublicDescription, SupportURL: request.SupportURL,
		Theme: request.Theme, BackgroundImageURL: request.BackgroundImageURL,
		Revision: request.Revision + 1, UpdatedAt: now,
	}
	event, err := audit.NewEvent(now, "administrator", administratorID, "site_settings.update", "site_settings", GlobalSettingsID, "succeeded", map[string]any{
		"revision": settings.Revision, "theme": settings.Theme,
		"support_url_configured": settings.SupportURL != "", "background_configured": settings.BackgroundImageURL != "",
	})
	if err != nil {
		return Settings{}, false, err
	}
	return s.repository.UpdateSiteSettings(ctx, UpdateInput{
		Settings: settings, ExpectedRevision: request.Revision, IdempotencyKey: idempotencyKey,
		RequestSHA256: hex.EncodeToString(digest[:]), UpdatedBy: administratorID,
	}, event)
}

func NormalizeRequest(input Request) (Request, error) {
	input.SiteName = strings.TrimSpace(input.SiteName)
	input.PanelTitle = strings.TrimSpace(input.PanelTitle)
	input.PublicDescription = strings.TrimSpace(input.PublicDescription)
	input.SupportURL = strings.TrimSpace(input.SupportURL)
	input.BackgroundImageURL = strings.TrimSpace(input.BackgroundImageURL)
	if input.Theme == "" {
		input.Theme = ThemeClassic
	}
	if utf8.RuneCountInString(input.SiteName) < 1 || utf8.RuneCountInString(input.SiteName) > 80 {
		return Request{}, fmt.Errorf("%w: site_name must contain 1 to 80 characters", faults.ErrValidation)
	}
	if utf8.RuneCountInString(input.PanelTitle) < 1 || utf8.RuneCountInString(input.PanelTitle) > 120 {
		return Request{}, fmt.Errorf("%w: panel_title must contain 1 to 120 characters", faults.ErrValidation)
	}
	if utf8.RuneCountInString(input.PublicDescription) > 2000 {
		return Request{}, fmt.Errorf("%w: public_description cannot exceed 2000 characters", faults.ErrValidation)
	}
	if !input.Theme.Valid() {
		return Request{}, fmt.Errorf("%w: theme must be classic or transparent", faults.ErrValidation)
	}
	if input.Revision < 0 {
		return Request{}, fmt.Errorf("%w: revision must be non-negative", faults.ErrValidation)
	}
	if err := validatePublicURL(input.SupportURL, false); err != nil {
		return Request{}, fmt.Errorf("%w: support_url %v", faults.ErrValidation, err)
	}
	if err := validatePublicURL(input.BackgroundImageURL, true); err != nil {
		return Request{}, fmt.Errorf("%w: background_image_url %v", faults.ErrValidation, err)
	}
	return input, nil
}

func ValidateStored(settings Settings) error {
	if settings.ID != GlobalSettingsID || settings.Revision < 0 {
		return fmt.Errorf("%w: invalid stored site settings identity", faults.ErrValidation)
	}
	_, err := NormalizeRequest(Request{
		SiteName: settings.SiteName, PanelTitle: settings.PanelTitle,
		PublicDescription: settings.PublicDescription, SupportURL: settings.SupportURL,
		Theme: settings.Theme, BackgroundImageURL: settings.BackgroundImageURL,
		Revision: settings.Revision,
	})
	return err
}

func validatePublicURL(raw string, requireHTTPS bool) error {
	if raw == "" {
		return nil
	}
	if len(raw) > 2048 {
		return fmt.Errorf("is too long")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute public URL without credentials or fragment")
	}
	if parsed.Scheme != "https" && (!(!requireHTTPS && parsed.Scheme == "http")) {
		return fmt.Errorf("must use %s", map[bool]string{true: "https", false: "http or https"}[requireHTTPS])
	}
	return nil
}
