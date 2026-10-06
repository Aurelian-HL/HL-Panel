package announcements

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
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

func (s *Service) List(ctx context.Context) ([]Announcement, error) {
	items, err := s.repository.ListAnnouncements(ctx)
	if items == nil && err == nil {
		items = []Announcement{}
	}
	return items, err
}

func (s *Service) Active(ctx context.Context) ([]Announcement, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	active := make([]Announcement, 0, len(items))
	for _, item := range items {
		if item.Active(now) {
			active = append(active, item)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].SortOrder == active[j].SortOrder {
			return active[i].CreatedAt.Before(active[j].CreatedAt)
		}
		return active[i].SortOrder < active[j].SortOrder
	})
	return active, nil
}

func (s *Service) Create(ctx context.Context, administratorID string, request Request, idempotencyKey string) (Announcement, bool, error) {
	request, err := NormalizeRequest(request, true)
	if err != nil {
		return Announcement{}, false, err
	}
	id, err := idgen.New("ann")
	if err != nil {
		return Announcement{}, false, err
	}
	return s.save(ctx, administratorID, id, request, idempotencyKey, time.Time{})
}

func (s *Service) Update(ctx context.Context, administratorID, id string, request Request, idempotencyKey string) (Announcement, bool, error) {
	request, err := NormalizeRequest(request, false)
	if err != nil {
		return Announcement{}, false, err
	}
	id = strings.TrimSpace(id)
	if !validIdentifier(id) {
		return Announcement{}, false, fmt.Errorf("%w: invalid announcement id", faults.ErrValidation)
	}
	previous, err := s.repository.Announcement(ctx, id)
	if err != nil {
		return Announcement{}, false, err
	}
	return s.save(ctx, administratorID, id, request, idempotencyKey, previous.CreatedAt)
}

func (s *Service) save(ctx context.Context, administratorID, id string, request Request, idempotencyKey string, createdAt time.Time) (Announcement, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdentifier(idempotencyKey) {
		return Announcement{}, false, fmt.Errorf("%w: invalid Idempotency-Key", faults.ErrValidation)
	}
	fingerprint, err := json.Marshal(struct {
		ID      string
		Request Request
	}{id, request})
	if err != nil {
		return Announcement{}, false, err
	}
	digest := sha256.Sum256(fingerprint)
	now := s.now().UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	item := Announcement{ID: id, Title: request.Title, Content: request.Content, Level: request.Level, Enabled: request.Enabled,
		StartsAt: request.StartsAt, EndsAt: request.EndsAt, SortOrder: request.SortOrder,
		Revision: request.Revision + 1, CreatedAt: createdAt, UpdatedAt: now}
	action := "announcement.update"
	if request.Revision == 0 {
		action = "announcement.create"
	}
	event, err := audit.NewEvent(now, "administrator", administratorID, action, "announcement", id, "succeeded", map[string]any{
		"enabled": item.Enabled, "level": item.Level, "sort_order": item.SortOrder, "revision": item.Revision,
	})
	if err != nil {
		return Announcement{}, false, err
	}
	return s.repository.SaveAnnouncement(ctx, SaveInput{Announcement: item, ExpectedRevision: request.Revision,
		IdempotencyKey: idempotencyKey, RequestSHA256: hex.EncodeToString(digest[:]), UpdatedBy: administratorID}, event)
}

func NormalizeRequest(input Request, create bool) (Request, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	if input.Level == "" {
		input.Level = LevelInfo
	}
	if utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 120 {
		return Request{}, fmt.Errorf("%w: title must contain 1 to 120 characters", faults.ErrValidation)
	}
	if utf8.RuneCountInString(input.Content) < 1 || utf8.RuneCountInString(input.Content) > 10000 {
		return Request{}, fmt.Errorf("%w: content must contain 1 to 10000 characters", faults.ErrValidation)
	}
	if !input.Level.Valid() {
		return Request{}, fmt.Errorf("%w: level must be info, warning or maintenance", faults.ErrValidation)
	}
	if input.SortOrder < -100000 || input.SortOrder > 100000 {
		return Request{}, fmt.Errorf("%w: sort_order must be between -100000 and 100000", faults.ErrValidation)
	}
	if input.Revision < 0 || (create && input.Revision != 0) || (!create && input.Revision < 1) {
		return Request{}, fmt.Errorf("%w: invalid revision", faults.ErrValidation)
	}
	if input.StartsAt != nil {
		value := input.StartsAt.UTC()
		input.StartsAt = &value
	}
	if input.EndsAt != nil {
		value := input.EndsAt.UTC()
		input.EndsAt = &value
	}
	if input.StartsAt != nil && input.EndsAt != nil && !input.EndsAt.After(*input.StartsAt) {
		return Request{}, fmt.Errorf("%w: ends_at must be after starts_at", faults.ErrValidation)
	}
	return input, nil
}

func ValidateStored(item Announcement) error {
	if !validIdentifier(item.ID) || item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) {
		return fmt.Errorf("%w: invalid stored announcement identity", faults.ErrValidation)
	}
	_, err := NormalizeRequest(Request{
		Title: item.Title, Content: item.Content, Level: item.Level, Enabled: item.Enabled,
		StartsAt: item.StartsAt, EndsAt: item.EndsAt, SortOrder: item.SortOrder, Revision: item.Revision,
	}, false)
	return err
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
