package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
)

func TestConfigurationHTTPPreservesPausedIdentityAndPrivacy(t *testing.T) {
	f := newBusinessFixture(t)
	ctx := context.Background()
	identities, err := vlessidentity.NewService(f.store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := forwarding.NewService(f.store, time.Now)
	input := customerVLESSRequest(f)
	rule, _, err := rules.CreateForCustomerByAdministrator(ctx, "adm_business", f.customer.ID, input, "configuration-fixture")
	if err != nil {
		t.Fatal(err)
	}
	endpointService := endpoints.NewService(f.store, time.Now)
	setup := vlesssetup.NewService(groupconfig.NewService(f.store, time.Now), endpointService, identities, vlesssetup.WithRuleAccess(rules))
	if err := setup.Ensure(ctx, "adm_business", rule); err != nil {
		t.Fatal(err)
	}
	record, err := identities.CredentialForAdministrator(ctx, "adm_business", rule.ID)
	if err != nil {
		t.Fatal("missing fixture identity", err)
	}
	_, _, err = rulegroups.NewService(f.store, time.Now).BatchForAdministrator(ctx, "adm_business", rulegroups.BatchRequest{
		Operation: rulegroups.BatchPause, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}, "configuration-pause")
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	resolver := vlessconnection.NewService(identities, rules, endpointService, f.store)
	handler := httpapi.New(auth.NewService(f.store, audit.NewService(f.store), time.Now, time.Hour),
		enrollment.NewService(f.store, time.Now, time.Hour), nodes.NewService(f.store, time.Now, time.Minute), groups.NewService(f.store, time.Now), endpointService,
		generations.NewService(f.store, time.Now), slog.New(slog.NewTextHandler(&logs, nil)),
		httpapi.WithBusiness(customers.NewService(f.store, time.Now), rules, groupconfig.NewService(f.store, time.Now)),
		httpapi.WithVLESS(identities, nil), httpapi.WithVLESSConnections(resolver))
	before, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	request := func(h http.Handler, path, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("configuration HTTP status %d; expected %d", w.Code, status)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("credential response can be cached or referred")
		}
		return w
	}
	path := "/api/v1/forwarding-rules/" + rule.ID + "/configuration"
	var want vlessconnection.Configuration
	for i := 0; i < 2; i++ {
		w := request(handler, path, f.token, http.StatusOK)
		if w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatal("configuration response is indexable")
		}
		var got vlessconnection.Configuration
		decoder := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&got); err != nil {
			t.Fatal("unexpected configuration response fields")
		}
		u, err := url.Parse(got.URI)
		if err != nil || u.User == nil || u.User.Username() != record.CredentialUUID || u.Scheme != "vless" || u.Query().Get("security") != "reality" || u.Query().Get("sni") != input.RealityServerName || got.Status != "paused" || !got.Paused || got.RuleStatus != forwarding.StatusPaused || got.ServiceReady || got.LiveReadinessChecked {
			t.Fatal("paused configuration changed credential or granted readiness")
		}
		if i > 0 && got != want {
			t.Fatal("repeated configuration read changed identity")
		}
		want = got
		for _, secret := range []string{input.VLESSSOCKS5Password, input.VLESSSOCKS5Username, "reality_private_key", "password_hash"} {
			if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
				t.Fatal("configuration leaked server-only material")
			}
		}
	}
	for _, p := range []string{path, "/api/v1/forwarding-rules/missing/configuration"} {
		request(handler, p, "", http.StatusUnauthorized)
		request(handler, p, "invalid-session", http.StatusUnauthorized)
	}
	for _, tc := range []struct {
		handler http.Handler
		path    string
		status  int
	}{
		{handler, "/api/v1/forwarding-rules/missing/configuration", http.StatusNotFound},
		{f.handler, path, http.StatusConflict}, // Source-less fallback must refuse credentials.
		{handler, "/api/v1/forwarding-rules/" + rule.ID + "/connection", http.StatusConflict},
		{handler, "/api/v1/vless/identities/" + record.Binding.ID + "/connection", http.StatusConflict},
	} {
		w := request(tc.handler, tc.path, f.token, tc.status)
		if bytes.Contains(w.Body.Bytes(), []byte(record.CredentialUUID)) || strings.Contains(w.Body.String(), "vless://") {
			t.Fatal("failed read leaked configuration")
		}
	}
	after, err := f.store.EncodeSnapshot()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("HTTP configuration reads mutated persisted state")
	}
	if strings.Contains(logs.String(), record.CredentialUUID) || strings.Contains(logs.String(), input.VLESSSOCKS5Password) {
		t.Fatal("configuration secret entered HTTP logs")
	}
}
