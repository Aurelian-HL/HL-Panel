package memoryrepo

import (
	"context"
	"fmt"
	"sort"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
)

var _ siteconfig.Repository = (*Store)(nil)
var _ announcements.Repository = (*Store)(nil)

func (s *Store) SiteSettings(context.Context) (siteconfig.Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.siteSettings, nil
}

func (s *Store) UpdateSiteSettings(_ context.Context, input siteconfig.UpdateInput, event audit.Event) (siteconfig.Settings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := input.UpdatedBy + "\x00site_settings.update\x00" + input.IdempotencyKey
	var replay siteconfig.Settings
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	if siteconfig.ValidateStored(input.Settings) != nil || input.Settings.Revision != input.ExpectedRevision+1 || s.siteSettings.Revision != input.ExpectedRevision {
		return siteconfig.Settings{}, false, fmt.Errorf("%w: site settings changed; reload before saving", faults.ErrConflict)
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, siteconfig.GlobalSettingsID, input.Settings); err != nil {
		return siteconfig.Settings{}, false, err
	}
	s.siteSettings = input.Settings
	s.appendAuditLocked(event)
	return input.Settings, false, nil
}

func (s *Store) ListAnnouncements(context.Context) ([]announcements.Announcement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]announcements.Announcement, 0, len(s.announcements))
	for _, item := range s.announcements {
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SortOrder != items[j].SortOrder {
			return items[i].SortOrder < items[j].SortOrder
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func (s *Store) Announcement(_ context.Context, id string) (announcements.Announcement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.announcements[id]
	if !ok {
		return announcements.Announcement{}, faults.ErrNotFound
	}
	return item, nil
}

func (s *Store) SaveAnnouncement(_ context.Context, input announcements.SaveInput, event audit.Event) (announcements.Announcement, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := input.Announcement
	key := input.UpdatedBy + "\x00announcement.save\x00" + item.ID + "\x00" + input.IdempotencyKey
	var replay announcements.Announcement
	if ok, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); ok || err != nil {
		return replay, ok, err
	}
	if err := announcements.ValidateStored(item); err != nil {
		return announcements.Announcement{}, false, err
	}
	previous, exists := s.announcements[item.ID]
	if (!exists && input.ExpectedRevision != 0) || (exists && previous.Revision != input.ExpectedRevision) || item.Revision != input.ExpectedRevision+1 {
		return announcements.Announcement{}, false, fmt.Errorf("%w: announcement changed; reload before saving", faults.ErrConflict)
	}
	if exists && !item.CreatedAt.Equal(previous.CreatedAt) {
		return announcements.Announcement{}, false, fmt.Errorf("%w: announcement creation time changed", faults.ErrConflict)
	}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, item.ID, item); err != nil {
		return announcements.Announcement{}, false, err
	}
	s.announcements[item.ID] = item
	s.appendAuditLocked(event)
	return item, false, nil
}
