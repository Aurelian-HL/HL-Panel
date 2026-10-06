package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
)

func TestSiteRepositoriesPersistAndReplayAtomically(t *testing.T) {
	store := snapshotFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	event := audit.Event{ID: "audit-site", Action: "site_settings.update", CreatedAt: now}
	input := siteconfig.UpdateInput{Settings: siteconfig.Settings{
		ID: siteconfig.GlobalSettingsID, SiteName: "Hongle", PanelTitle: "线路管理", Theme: siteconfig.ThemeClassic,
		Revision: 1, UpdatedAt: now,
	}, ExpectedRevision: 0, IdempotencyKey: "site-1", RequestSHA256: "site-hash", UpdatedBy: "adm-1"}
	saved, replayed, err := store.UpdateSiteSettings(context.Background(), input, event)
	if err != nil || replayed || saved.Revision != 1 {
		t.Fatalf("save settings: %+v %v %v", saved, replayed, err)
	}
	if _, replayed, err = store.UpdateSiteSettings(context.Background(), input, event); err != nil || !replayed {
		t.Fatalf("settings retry did not replay: %v %v", replayed, err)
	}
	conflict := input
	conflict.RequestSHA256 = "different"
	if _, _, err = store.UpdateSiteSettings(context.Background(), conflict, event); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	announcement := announcements.Announcement{ID: "ann-1", Title: "维护", Content: "维护说明", Level: announcements.LevelMaintenance, Enabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	noticeInput := announcements.SaveInput{Announcement: announcement, ExpectedRevision: 0, IdempotencyKey: "ann-create", RequestSHA256: "ann-hash", UpdatedBy: "adm-1"}
	if _, replayed, err = store.SaveAnnouncement(context.Background(), noticeInput, event); err != nil || replayed {
		t.Fatalf("save announcement: %v %v", replayed, err)
	}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := restored.SiteSettings(context.Background())
	if err != nil || settings.SiteName != "Hongle" {
		t.Fatalf("settings lost: %+v %v", settings, err)
	}
	items, err := restored.ListAnnouncements(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != announcement.ID {
		t.Fatalf("announcement lost: %+v %v", items, err)
	}
}

func TestSnapshotVersionFourUpgradesSiteDefaults(t *testing.T) {
	raw, err := snapshotFixture(t).EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["Version"] = float64(ruleGroupsSnapshotVersion)
	delete(legacy, "SiteSettings")
	delete(legacy, "Announcements")
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("legacy v4 snapshot rejected: %v", err)
	}
	settings, err := restored.SiteSettings(context.Background())
	if err != nil || settings.SiteName != "XZPanel" || settings.Revision != 0 {
		t.Fatalf("legacy defaults missing: %+v %v", settings, err)
	}
	items, err := restored.ListAnnouncements(context.Background())
	if err != nil || len(items) != 0 {
		t.Fatalf("legacy announcements: %+v %v", items, err)
	}
}
