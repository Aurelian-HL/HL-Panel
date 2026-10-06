package customers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEffectiveStatusUsesBoundaryAndExplicitPrecedence(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Second)
	for _, test := range []struct {
		name string
		item Customer
		want Status
	}{
		{"unlimited with usage", Customer{TrafficUsedBytes: 100}, StatusActive},
		{"under quota", Customer{TrafficLimitBytes: 100, TrafficUsedBytes: 99}, StatusActive},
		{"at quota", Customer{TrafficLimitBytes: 100, TrafficUsedBytes: 100}, StatusQuotaExhausted},
		{"future expiry", Customer{ExpiresAt: &future}, StatusActive},
		{"at expiry", Customer{ExpiresAt: &now}, StatusExpired},
		{"expiry before quota", Customer{ExpiresAt: &now, TrafficLimitBytes: 100, TrafficUsedBytes: 100}, StatusExpired},
		{"disabled before expiry", Customer{Disabled: true, ExpiresAt: &now}, StatusDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.item.EffectiveStatus(now); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCustomerJSONNeverContainsPasswordHash(t *testing.T) {
	raw, err := json.Marshal(Customer{ID: "cus_a", PasswordHash: []byte("secret-password-hash")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "secret") {
		t.Fatalf("customer API JSON leaked secret material: %s", raw)
	}
}
