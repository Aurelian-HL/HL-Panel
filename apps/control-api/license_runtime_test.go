package main

import (
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/licensing"
)

func TestValidateRemoteLeaseBindsIssuerResponse(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	configuration := runtimeConfig{LicenseDomain: "panel.example.com", LicenseInstanceID: "instance-1"}
	lease := licensing.Lease{
		LicenseID:   "lic-1",
		InstanceID:  "instance-1",
		Domain:      "PANEL.EXAMPLE.COM.",
		ActivatedAt: now.Add(-24 * time.Hour),
		ExpiresAt:   now.AddDate(0, 1, 0),
		CheckedAt:   now,
	}
	if err := validateRemoteLeaseAt(lease, configuration, "lic-1", now); err != nil {
		t.Fatalf("valid lease rejected: %v", err)
	}
	lease.InstanceID = "other-instance"
	if err := validateRemoteLeaseAt(lease, configuration, "lic-1", now); !errors.Is(err, licensing.ErrBound) {
		t.Fatalf("instance mismatch = %v", err)
	}
	lease.InstanceID = configuration.LicenseInstanceID
	lease.Domain = "other.example.com"
	if err := validateRemoteLeaseAt(lease, configuration, "lic-1", now); !errors.Is(err, licensing.ErrDomainMismatch) {
		t.Fatalf("domain mismatch = %v", err)
	}
	lease.Domain = configuration.LicenseDomain
	lease.LicenseID = "lic-2"
	if err := validateRemoteLeaseAt(lease, configuration, "lic-1", now); !errors.Is(err, licensing.ErrBound) {
		t.Fatalf("license mismatch = %v", err)
	}
}

func TestValidateRemoteLeaseRejectsInvalidTimes(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	configuration := runtimeConfig{LicenseDomain: "panel.example.com", LicenseInstanceID: "instance-1"}
	base := licensing.Lease{
		LicenseID:   "lic-1",
		InstanceID:  "instance-1",
		Domain:      "panel.example.com",
		ActivatedAt: now.Add(-time.Minute),
		ExpiresAt:   now.Add(time.Hour),
		CheckedAt:   now,
	}
	for name, mutate := range map[string]func(*licensing.Lease){
		"missing checked at": func(lease *licensing.Lease) { lease.CheckedAt = time.Time{} },
		"expired":            func(lease *licensing.Lease) { lease.ExpiresAt = now },
		"activation after expiry": func(lease *licensing.Lease) {
			lease.ActivatedAt = now.Add(2 * time.Hour)
			lease.ExpiresAt = now.Add(3 * time.Hour)
		},
		"future checked at": func(lease *licensing.Lease) { lease.CheckedAt = now.Add(remoteLeaseClockSkew + time.Second) },
		"checked before activation": func(lease *licensing.Lease) {
			lease.ActivatedAt = now.Add(time.Minute)
			lease.CheckedAt = now
		},
	} {
		t.Run(name, func(t *testing.T) {
			lease := base
			mutate(&lease)
			err := validateRemoteLeaseAt(lease, configuration, "lic-1", now)
			if name == "expired" {
				if !errors.Is(err, licensing.ErrExpired) {
					t.Fatalf("error = %v, want ErrExpired", err)
				}
				return
			}
			if !errors.Is(err, licensing.ErrInvalidCode) {
				t.Fatalf("error = %v, want ErrInvalidCode", err)
			}
		})
	}
}

func TestLicenseRenewalDelayUsesLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if got := licenseRenewalDelay(now, now.Add(time.Hour), 12*time.Hour); got != 30*time.Minute {
		t.Fatalf("short lease delay = %s, want 30m", got)
	}
	if got := licenseRenewalDelay(now, now.Add(48*time.Hour), 12*time.Hour); got != 12*time.Hour {
		t.Fatalf("long lease delay = %s, want 12h", got)
	}
	if got := licenseRenewalDelay(now, now.Add(500*time.Millisecond), 12*time.Hour); got != time.Second {
		t.Fatalf("near-expiry delay = %s, want 1s", got)
	}
}

func TestLicenseRenewalRetryBacksOffBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if got := licenseRenewalRetryDelay(now, now.Add(2*time.Minute), 1); got != 40*time.Second {
		t.Fatalf("first retry delay = %s, want 40s", got)
	}
	if got := licenseRenewalRetryDelay(now, now.Add(2*time.Hour), 5); got != 15*time.Minute {
		t.Fatalf("capped retry delay = %s, want 15m", got)
	}
	if got := licenseRenewalRetryDelay(now, now.Add(-time.Minute), 1); got != time.Minute {
		t.Fatalf("expired first retry delay = %s, want 1m", got)
	}
	if got := licenseRenewalRetryDelay(now, now.Add(-time.Minute), 5); got != 15*time.Minute {
		t.Fatalf("expired capped retry delay = %s, want 15m", got)
	}
}
