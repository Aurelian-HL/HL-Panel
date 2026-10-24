package customers

import (
	"testing"
	"time"
)

func TestReminderDoesNotGrantOrRemoveForwarding(t *testing.T) {
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-time.Hour), now, now.Add(time.Hour)} {
		c := Customer{ExpiresAt: &at, TrafficLimitBytes: 10, TrafficUsedBytes: 9}
		if !c.ForwardingAllowed() || !c.ExpiresAt.Equal(at) {
			t.Fatal("reminder blocked forwarding or changed the date")
		}
		c.TrafficUsedBytes = 10
		if c.ForwardingAllowed() {
			t.Fatal("reminder bypassed the quota")
		}
		c.TrafficUsedBytes, c.Disabled = 0, true
		if c.ForwardingAllowed() {
			t.Fatal("reminder bypassed explicit suspension")
		}
	}
}
