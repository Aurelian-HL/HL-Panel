package announcements

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type repositoryStub struct{ items map[string]Announcement }

func (r *repositoryStub) ListAnnouncements(context.Context) ([]Announcement, error) {
	items := make([]Announcement, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	return items, nil
}
func (r *repositoryStub) Announcement(_ context.Context, id string) (Announcement, error) {
	item, ok := r.items[id]
	if !ok {
		return Announcement{}, faults.ErrNotFound
	}
	return item, nil
}
func (r *repositoryStub) SaveAnnouncement(_ context.Context, input SaveInput, _ audit.Event) (Announcement, bool, error) {
	r.items[input.Announcement.ID] = input.Announcement
	return input.Announcement, false, nil
}

func TestActiveFiltersAndSortsScheduledAnnouncements(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	before, after := now.Add(-time.Hour), now.Add(time.Hour)
	repository := &repositoryStub{items: map[string]Announcement{
		"second":   {ID: "second", Enabled: true, Title: "second", SortOrder: 20, StartsAt: &before, EndsAt: &after},
		"first":    {ID: "first", Enabled: true, Title: "first", SortOrder: 10},
		"disabled": {ID: "disabled", Enabled: false, Title: "disabled"},
		"expired":  {ID: "expired", Enabled: true, Title: "expired", EndsAt: &before},
	}}
	items, err := NewService(repository, func() time.Time { return now }).Active(context.Background())
	if err != nil || len(items) != 2 || items[0].ID != "first" || items[1].ID != "second" {
		t.Fatalf("unexpected active announcements: %+v err=%v", items, err)
	}
}

func TestCreateValidatesSchedule(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	before := now.Add(-time.Hour)
	_, _, err := NewService(&repositoryStub{items: map[string]Announcement{}}, func() time.Time { return now }).Create(context.Background(), "adm-1", Request{
		Title: "维护", Content: "维护说明", Enabled: true, StartsAt: &now, EndsAt: &before,
	}, "create-maintenance")
	if err == nil {
		t.Fatal("expected invalid schedule")
	}
}
