package postgressnapshot

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
)

var _ siteconfig.Repository = (*Store)(nil)
var _ announcements.Repository = (*Store)(nil)

func (s *Store) SiteSettings(ctx context.Context) (siteconfig.Settings, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (siteconfig.Settings, error) { return state.SiteSettings(ctx) })
}

func (s *Store) UpdateSiteSettings(ctx context.Context, input siteconfig.UpdateInput, event audit.Event) (siteconfig.Settings, bool, error) {
	type result struct {
		item     siteconfig.Settings
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.UpdateSiteSettings(ctx, input, event)
		return result{item: item, replayed: replayed}, err
	})
	return value.item, value.replayed, err
}

func (s *Store) ListAnnouncements(ctx context.Context) ([]announcements.Announcement, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]announcements.Announcement, error) {
		return state.ListAnnouncements(ctx)
	})
}

func (s *Store) Announcement(ctx context.Context, id string) (announcements.Announcement, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (announcements.Announcement, error) { return state.Announcement(ctx, id) })
}

func (s *Store) SaveAnnouncement(ctx context.Context, input announcements.SaveInput, event audit.Event) (announcements.Announcement, bool, error) {
	type result struct {
		item     announcements.Announcement
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		item, replayed, err := state.SaveAnnouncement(ctx, input, event)
		return result{item: item, replayed: replayed}, err
	})
	return value.item, value.replayed, err
}
