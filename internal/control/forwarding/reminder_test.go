package forwarding

import (
	"testing"
	"time"
)

func TestReminderDoesNotOverrideRuleAvailability(t *testing.T) {
	now := time.Now().UTC()
	customer := CustomerAvailability{Enabled: true, ExpiresAt: &now}
	for _, protocol := range []Protocol{ProtocolTCP, ProtocolUDP} {
		rule := Rule{Deployed: true, Protocol: protocol}
		if DeriveStatus(rule, customer, now) != StatusActive {
			t.Fatal("reminder disabled a deployed listener")
		}
		rule.Paused = true
		if DeriveStatus(rule, customer, now) != StatusPaused {
			t.Fatal("reminder bypassed a paused listener")
		}
		rule.Paused, rule.TrafficLimitBytes, rule.TrafficUsedBytes = false, 10, 10
		if DeriveStatus(rule, customer, now) != StatusQuotaExhausted {
			t.Fatal("reminder bypassed the rule quota")
		}
	}
}
