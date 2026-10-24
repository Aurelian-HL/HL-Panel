package customerapi

import (
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
)

func TestReminderConnectionsRetainIdentityAndEnforcement(t *testing.T) {
	now := time.Now().UTC()
	evidence := validConnectionEvidence(t, now)
	service := NewConnectionService(nil, nil, func() time.Time { return now })
	active := ConnectionView{Status: "unavailable"}
	profile := customeridentity.Profile{ID: "cus_api", EffectiveStatus: customers.StatusActive}
	service.populate(&active, profile, evidence)
	if active.Status != "ready" {
		t.Fatal("fixture is not ready")
	}
	profile.ExpiresAt, profile.EffectiveStatus = &now, customers.StatusExpired
	reminder := ConnectionView{Status: "unavailable"}
	service.populate(&reminder, profile, evidence)
	if reminder.Status != "ready" || reminder.URI != active.URI {
		t.Fatal("reminder changed the usable client URI")
	}
	for _, status := range []customers.Status{customers.StatusDisabled, customers.StatusQuotaExhausted, "unknown"} {
		profile.EffectiveStatus = status
		view := ConnectionView{Status: "unavailable"}
		service.populate(&view, profile, evidence)
		if view.URI != "" {
			t.Fatal("restriction was bypassed")
		}
	}
	profile.EffectiveStatus = customers.StatusExpired
	profile.TrafficLimitBytes, profile.TrafficUsedBytes = 1, 1
	view := ConnectionView{Status: "unavailable"}
	service.populate(&view, profile, evidence)
	if view.URI != "" {
		t.Fatal("expired display status concealed an exhausted quota")
	}
}
