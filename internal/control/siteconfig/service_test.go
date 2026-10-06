package siteconfig

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type repositoryStub struct {
	settings Settings
	input    UpdateInput
	event    audit.Event
}

func (r *repositoryStub) SiteSettings(context.Context) (Settings, error) { return r.settings, nil }
func (r *repositoryStub) UpdateSiteSettings(_ context.Context, input UpdateInput, event audit.Event) (Settings, bool, error) {
	r.input, r.event, r.settings = input, event, input.Settings
	return input.Settings, false, nil
}

func TestUpdateNormalizesAndAuditsSettings(t *testing.T) {
	fixed := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	repository := &repositoryStub{}
	service := NewService(repository, func() time.Time { return fixed })
	settings, replayed, err := service.Update(context.Background(), "adm-1", Request{
		SiteName: "  Hongle  ", PanelTitle: "  线路管理  ", Theme: ThemeTransparent,
		SupportURL: "https://support.example.test/help", BackgroundImageURL: "https://cdn.example.test/panel.webp", Revision: 2,
	}, "settings-3")
	if err != nil || replayed {
		t.Fatalf("update failed: replayed=%v err=%v", replayed, err)
	}
	if settings.SiteName != "Hongle" || settings.Revision != 3 || !settings.UpdatedAt.Equal(fixed) {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	if repository.event.Action != "site_settings.update" || repository.event.Metadata["revision"] != int64(3) {
		t.Fatalf("unexpected audit event: %+v", repository.event)
	}
}

func TestNormalizeRejectsUnsafePresentationURLs(t *testing.T) {
	base := Request{SiteName: "XZPanel", PanelTitle: "线路管理", Theme: ThemeClassic}
	tests := []Request{
		func() Request { value := base; value.SupportURL = "javascript:alert(1)"; return value }(),
		func() Request {
			value := base
			value.BackgroundImageURL = "http://cdn.example.test/a.jpg"
			return value
		}(),
		func() Request { value := base; value.SupportURL = "https://user:pass@example.test"; return value }(),
	}
	for _, test := range tests {
		if _, err := NormalizeRequest(test); err == nil {
			t.Fatalf("expected invalid request for %+v", test)
		}
	}
}
