package panellicense

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cacheEnvelope(t *testing.T, f *signedFixture, edit func(map[string]any)) Envelope {
	t.Helper()
	raw, err := json.Marshal(f.claims("cached-request"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	edit(fields)
	envelope, err := Sign(fields, "test", f.key)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func reloadEnvelope(t *testing.T, f *signedFixture, envelope Envelope) *Client {
	t.Helper()
	raw, err := json.Marshal(cachedState{Envelope: &envelope})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.client.config.Directory, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(f.client.config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func removeEntitlements(fields map[string]any) { delete(fields, "tier"); delete(fields, "limits") }

func TestLegacyCacheIdentityOnlyOfflineRestartAndSync(t *testing.T) {
	f := newSignedFixture(t)
	envelope := cacheEnvelope(t, f, removeEntitlements)
	client := reloadEnvelope(t, f, envelope)
	status := client.Status("admin")
	if status.Status != "upgrade_required" || status.LicenseID != "lic-test" || status.ManagementAllowed || status.Tier != "" || status.Limits != nil {
		t.Fatalf("legacy must retain identity without entitlement: %+v", status)
	}
	if _, err := client.RegisterFree(context.Background()); err == nil || f.calls.Load() != 0 {
		t.Fatal("legacy identity could register free")
	}
	f.fail = true
	if err := client.Sync(context.Background()); err == nil {
		t.Fatal("offline sync succeeded")
	}
	if client.Status("").ManagementAllowed {
		t.Fatal("offline legacy granted management")
	}
	// Restart must preserve the signed identity even after an offline attempt.
	client, err := NewClient(f.client.config)
	if err != nil {
		t.Fatal(err)
	}
	f.fail = false
	if err := client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = client.Status("")
	if !status.ManagementAllowed || status.Tier != TierLicensed || status.Limits == nil || *status.Limits != (Limits{}) {
		t.Fatalf("explicit synchronized entitlement missing: %+v", status)
	}
	restarted, err := NewClient(f.client.config)
	if err != nil || !restarted.Status("").ManagementAllowed {
		t.Fatalf("new entitlement did not persist: %v", err)
	}
}

func TestSignedCacheRejectsMissingNullAndMismatchedEntitlements(t *testing.T) {
	cases := map[string]func(map[string]any){
		"tier missing":               func(v map[string]any) { delete(v, "tier") },
		"limits missing":             func(v map[string]any) { delete(v, "limits") },
		"tier null":                  func(v map[string]any) { v["tier"] = nil },
		"tier empty":                 func(v map[string]any) { v["tier"] = "" },
		"tier unknown":               func(v map[string]any) { v["tier"] = "other" },
		"limits null":                func(v map[string]any) { v["limits"] = nil },
		"limits empty":               func(v map[string]any) { v["limits"] = map[string]any{} },
		"rules missing":              func(v map[string]any) { v["limits"] = map[string]any{"device_groups": 0} },
		"groups null":                func(v map[string]any) { v["limits"] = map[string]any{"device_groups": nil, "rules": 0} },
		"rules null":                 func(v map[string]any) { v["limits"] = map[string]any{"device_groups": 0, "rules": nil} },
		"groups missing":             func(v map[string]any) { v["limits"] = map[string]any{"rules": 0} },
		"tier uppercase":             func(v map[string]any) { v["Tier"] = v["tier"]; delete(v, "tier") },
		"limits uppercase":           func(v map[string]any) { v["Limits"] = v["limits"]; delete(v, "limits") },
		"legacy lookalike uppercase": func(v map[string]any) { v["Tier"], v["Limits"] = v["tier"], v["limits"]; removeEntitlements(v) },
		"limit uppercase":            func(v map[string]any) { v["limits"] = map[string]any{"Device_Groups": 0, "rules": 0} },
		"free unlimited":             func(v map[string]any) { v["tier"] = TierFree },
		"wrong legacy identity":      func(v map[string]any) { removeEntitlements(v); v["instance_id"] = "hlp-wrong-instance" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSignedFixture(t)
			envelope := cacheEnvelope(t, f, edit)
			if err := f.client.accept(envelope, "cached-request"); err == nil {
				t.Fatal("fresh malformed entitlement accepted")
			}
			client := reloadEnvelope(t, f, envelope)
			status := client.Status("")
			if status.ManagementAllowed || status.LicenseID != "" || status.LastError == "" || client.legacyClaims != nil {
				t.Fatalf("malformed cache restored: %+v", status)
			}
		})
	}
	t.Run("damaged signature", func(t *testing.T) {
		f := newSignedFixture(t)
		envelope := cacheEnvelope(t, f, removeEntitlements)
		envelope.Signature = "corrupt"
		client := reloadEnvelope(t, f, envelope)
		if client.legacyClaims != nil || client.Status("").LicenseID != "" {
			t.Fatal("bad signature restored")
		}
	})
	t.Run("fresh legacy response", func(t *testing.T) {
		f := newSignedFixture(t)
		if err := f.client.accept(cacheEnvelope(t, f, removeEntitlements), "cached-request"); err == nil {
			t.Fatal("fresh legacy granted management")
		}
	})
}

func TestLegacyCacheSyncRejectsIdentityRevisionAndBusinessRollback(t *testing.T) {
	for _, change := range []string{"license", "revision", "business", "clock"} {
		t.Run(change, func(t *testing.T) {
			f := newSignedFixture(t)
			f.revision = 2
			client := reloadEnvelope(t, f, cacheEnvelope(t, f, removeEntitlements))
			claims := f.claims("new-request")
			switch change {
			case "license":
				claims.LicenseID = "lic-other"
			case "revision":
				claims.Revision = 1
			case "business":
				expiry := f.expires.Add(24 * time.Hour)
				claims.LicenseExpiresAt = &expiry
			case "clock":
				claims.IssuedAt = f.now.Add(-time.Minute)
			}
			envelope, err := Sign(claims, "test", f.key)
			if err != nil {
				t.Fatal(err)
			}
			if err = client.accept(envelope, "new-request"); err == nil {
				t.Fatal("legacy rollback accepted")
			}
			if client.Status("").ManagementAllowed || client.legacyClaims == nil {
				t.Fatal("failed synchronization changed legacy identity")
			}
		})
	}
}
