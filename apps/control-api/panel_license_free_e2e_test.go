package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/panelquota"
	p "github.com/hongle/hl-panel/internal/panellicense"
)

// verifyFreePanelRuntime is part of the opt-in isolated runtime acceptance.
// It uses the actual API process, PostgreSQL, client cache and HT handler.
func verifyFreePanelRuntime(t *testing.T,
	call func(string, string, any, ...int) []byte,
	status func() p.Status,
	ht func(string, string, any) map[string]json.RawMessage,
	restart func(), dns *panelE2EDNS, state string,
) {
	t.Helper()
	if s := status(); s.Status != "unactivated" || s.ManagementAllowed {
		t.Fatal("fresh free runtime must require domain registration")
	}
	group := func(name string) map[string]any {
		return map[string]any{"name": name, "kind": "ENTRY", "selection_policy": "weighted_round_robin"}
	}
	call("POST", "/device-groups", group("登记前拒绝"), 403)
	var challenge p.Challenge
	if err := json.Unmarshal(call("POST", "/license/free/prepare", map[string]any{}, 200), &challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Purpose != "free-registration" || challenge.Domain != "free-runtime.example.test" || challenge.DNSName == "" || challenge.DNSValue == "" {
		t.Fatal("free runtime returned an invalid domain challenge")
	}
	call("POST", "/license/activation/commit", map[string]any{}, 502)
	if status().ManagementAllowed {
		t.Fatal("unverified free runtime granted management")
	}
	// Retry after a process restart must reuse the saved signed challenge.
	restart()
	dns.mu.Lock()
	dns.records[challenge.DNSName] = []string{challenge.DNSValue}
	dns.mu.Unlock()
	call("POST", "/license/activation/commit", map[string]any{}, 200)
	var licenseID string
	assertTier := func(tier string) p.Status {
		t.Helper()
		s := status()
		limits, _ := p.LimitsForTier(tier)
		if !s.ManagementAllowed || s.Status != "active" || s.Tier != tier || s.Limits == nil || *s.Limits != limits || s.ExpiresAt != nil || s.ReminderDue || s.LicenseID == "" {
			t.Fatal("runtime did not apply explicit perpetual tier and limits")
		}
		if licenseID != "" && s.LicenseID != licenseID {
			t.Fatal("runtime changed license identity during synchronization")
		}
		return s
	}
	licenseID = assertTier(p.TierFree).LicenseID
	keyBefore, err := os.ReadFile(filepath.Join(state, "instance-key"))
	if err != nil {
		t.Fatal(err)
	}
	// The client deliberately throttles manual refreshes. A busy response is
	// retried only for its documented code, and must eventually succeed.
	syncLicense := func() {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); ; {
			raw := call("POST", "/license/sync", map[string]any{}, 200, 429)
			var result struct {
				Status string `json:"status"`
				Error  struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.Error.Code == "" && result.Status != "" {
				return
			}
			if result.Error.Code != "license_sync_busy" || time.Now().After(deadline) {
				t.Fatal("free runtime synchronization did not complete")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	syncLicense()
	assertTier(p.TierFree)
	restart()
	assertTier(p.TierFree)
	call("POST", "/device-groups", group("免费额度内设备组"), 201)
	assertQuotaRejected := func(name string) {
		t.Helper()
		var result p.ErrorResponse
		if err := json.Unmarshal(call("POST", "/device-groups", group(name), 409), &result); err != nil || result.Error.Code != "panel_quota_exceeded" {
			t.Fatal("real API did not reject panel-wide group quota")
		}
	}
	assertQuotaRejected("免费第二个设备组")
	assertUsage := func(groups int) {
		t.Helper()
		var response struct {
			Usage panelquota.Usage `json:"usage"`
		}
		if err := json.Unmarshal(call("GET", "/license/status", nil, 200), &response); err != nil || response.Usage.DeviceGroups != groups || response.Usage.Rules != 0 {
			t.Fatal("real runtime quota usage incorrect")
		}
	}
	assertUsage(1)
	changeTier := func(tier string) {
		t.Helper()
		s := status()
		result := ht("POST", "/"+licenseID+"/change-tier", map[string]any{"expected_revision": s.Revision, "tier": tier, "term": p.Term{Kind: "perpetual"}, "reason": "免费档位真实运行时隔离验收"})
		var record p.LicenseRecord
		if err := json.Unmarshal(result["license"], &record); err != nil || record.ID != licenseID || record.Tier != tier || record.Revision <= s.Revision {
			t.Fatal("HT tier mutation did not preserve identity and advance revision")
		}
		restart()
		for deadline := time.Now().Add(20 * time.Second); ; {
			if current := status(); current.Tier == tier && current.Revision == record.Revision {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("runtime did not follow HT tier mutation")
			}
			time.Sleep(100 * time.Millisecond)
		}
		assertTier(tier)
	}
	changeTier(p.TierLicensed)
	call("POST", "/device-groups", group("正式授权新增设备组"), 201)
	assertUsage(2)
	call("POST", "/license/free/prepare", map[string]any{}, 502)
	assertTier(p.TierLicensed)
	before := call("GET", "/device-groups", nil, 200)
	changeTier(p.TierFree)
	assertUsage(2)
	assertQuotaRejected("降档后不能扩容")
	if !bytes.Equal(before, call("GET", "/device-groups", nil, 200)) {
		t.Fatal("free downgrade changed existing over-quota groups")
	}
	if keyAfter, err := os.ReadFile(filepath.Join(state, "instance-key")); err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("free registration or tier changes replaced instance key")
	}
	t.Log("actual free registration: DNS failure/restart recovery, signed free 1/10, sync/restart, real group quota, HT upgrade/downgrade, over-quota preservation passed")
}
