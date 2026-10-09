package httpapi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSubscriptionPackageURLsFetchMatchingPublicFeeds(t *testing.T) {
	f := newBusinessFixture(t)
	input := subscriptions.Request{Name: "导入包链接验证", CustomerID: f.customer.ID, Lines: []subscriptions.Line{{Name: "线路", URI: "socks5://user:test-password@edge.example.test:1080"}}}
	var created struct {
		Subscription subscriptions.Item `json:"subscription"`
	}
	decodeResponse(t, businessRequest(t, f, "POST", "/api/v1/subscriptions", "package-url-create", input, http.StatusOK), &created)
	businessRequest(t, f, "POST", "/api/v1/subscriptions/"+created.Subscription.ID+"/actions/publish", "package-url-publish", map[string]any{"revision": created.Subscription.Revision}, http.StatusOK)
	var pack struct {
		Data string `json:"data_base64"`
	}
	decodeResponse(t, businessRequest(t, f, "POST", "/api/v1/subscriptions/"+created.Subscription.ID+"/package", "", map[string]any{"base_url": "https://panel.example.test"}, http.StatusOK), &pack)
	data, err := base64.StdEncoding.DecodeString(pack.Data)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var packagedYAML []byte
	var onlineYAML []byte
	checked := 0
	for _, file := range archive.File {
		if !strings.HasSuffix(file.Name, "订阅链接.txt") && !strings.HasSuffix(file.Name, ".yaml") {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(file.Name, ".yaml") {
			packagedYAML = bytes.TrimPrefix(contents, []byte{0xef, 0xbb, 0xbf})
			continue
		}
		// Ordinary clipboard whitespace trimming must yield a usable URL.
		copied := strings.TrimSpace(string(contents))
		address, err := url.ParseRequestURI(copied)
		if err != nil || address.Scheme != "https" || address.Host != "panel.example.test" {
			t.Fatalf("copied %s is not a valid subscription URL", file.Name)
		}
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, address.RequestURI(), nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("public feed from %s returned %d", file.Name, recorder.Code)
		}
		if strings.HasSuffix(file.Name, "-Clash订阅链接.txt") {
			if !strings.HasSuffix(address.Path, ".yaml") || !strings.Contains(recorder.Header().Get("Content-Type"), "application/yaml") {
				t.Fatal("Clash URL does not return YAML")
			}
			onlineYAML = recorder.Body.Bytes()
		} else {
			decoded, err := base64.StdEncoding.DecodeString(recorder.Body.String())
			if err != nil || !bytes.HasPrefix(decoded, []byte("socks://")) {
				t.Fatal("TXT URL does not return the published proxy")
			}
		}
		checked++
	}
	if checked != 3 || len(onlineYAML) == 0 || !bytes.Equal(packagedYAML, onlineYAML) {
		t.Fatal("package links did not fetch the same configuration as the static YAML")
	}
}

func TestSubscriptionHTTPPublishDraftRotateRevokeAndAuthorization(t *testing.T) {
	f := newBusinessFixture(t)
	req := subscriptions.Request{Name: "客户订阅", CustomerID: f.customer.ID, Lines: []subscriptions.Line{{Name: "线路一", URI: "socks5://user:private-test-password@edge.example.test:1080"}}}
	var result struct {
		Subscription subscriptions.Item `json:"subscription"`
		Replayed     bool               `json:"replayed"`
	}
	raw := businessRequest(t, f, "POST", "/api/v1/subscriptions", "subscription-create", req, 200)
	decodeResponse(t, raw, &result)
	original := result.Subscription
	replay := businessRequest(t, f, "POST", "/api/v1/subscriptions", "subscription-create", req, 200)
	decodeResponse(t, replay, &result)
	if !result.Replayed || result.Subscription.ID != original.ID {
		t.Fatal("create retry not idempotent")
	}
	req.Name = "changed"
	businessRequest(t, f, "POST", "/api/v1/subscriptions", "subscription-create", req, 409)
	req.Name = "客户订阅"
	var detail struct {
		Links subscriptions.Links `json:"links"`
	}
	decodeResponse(t, requestJSON(t, f.handler, "GET", "/api/v1/subscriptions/"+original.ID, f.token, nil, 200), &detail)
	old := detail.Links
	requestJSON(t, f.handler, "GET", old.TXT, "", nil, 404)
	requestJSON(t, f.handler, "GET", "/api/v1/subscriptions", "", nil, 401)
	action := func(op string, revision int64) subscriptions.Item {
		raw := businessRequest(t, f, "POST", "/api/v1/subscriptions/"+original.ID+"/actions/"+op, "sub-"+op+"-"+string(rune('A'+revision)), map[string]any{"revision": revision}, 200)
		decodeResponse(t, raw, &result)
		return result.Subscription
	}
	current := action("publish", 1)
	feed := requestJSON(t, f.handler, "GET", old.TXT, "", nil, 200)
	decoded, err := base64.StdEncoding.DecodeString(string(feed))
	if err != nil || !bytes.Contains(decoded, []byte("edge.example.test")) {
		t.Fatal("invalid TXT")
	}
	req.Revision = current.Revision
	req.Lines[0].URI = "socks5://other:second-secret@new.example.test:1081"
	raw = businessRequest(t, f, "PUT", "/api/v1/subscriptions/"+original.ID, "sub-draft-update", req, 200)
	decodeResponse(t, raw, &result)
	current = result.Subscription
	if !bytes.Equal(feed, requestJSON(t, f.handler, "GET", old.TXT, "", nil, 200)) {
		t.Fatal("draft changed public feed")
	}
	businessRequest(t, f, "POST", "/api/v1/subscriptions/"+original.ID+"/actions/publish", "stale-revision", map[string]any{"revision": 1}, 409)
	current = action("publish", current.Revision)
	if bytes.Equal(feed, requestJSON(t, f.handler, "GET", old.TXT, "", nil, 200)) {
		t.Fatal("publish failed")
	}
	decodeResponse(t, requestJSON(t, f.handler, "GET", "/api/v1/subscriptions/"+original.ID, f.token, nil, 200), &detail)
	if detail.Links != old {
		t.Fatal("publish changed address")
	}
	list := requestJSON(t, f.handler, "GET", "/api/v1/subscriptions", f.token, nil, 200)
	events := f.store.AuditEvents()
	audits, _ := json.Marshal(events)
	for _, body := range [][]byte{list, audits, raw} {
		for _, secret := range []string{"sub_", "private-test-password", "second-secret", "socks5://"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatal("secret leaked in list/audit/mutation")
			}
		}
	}
	if _, err = f.store.Subscription(context.Background(), "other-admin", original.ID); err == nil {
		t.Fatal("cross administrator detail exposed")
	}
	other, _ := f.store.ListSubscriptions(context.Background(), "other-admin")
	if len(other) != 0 {
		t.Fatal("cross administrator list exposed")
	}
	current = action("rotate", current.Revision)
	requestJSON(t, f.handler, "GET", old.TXT, "", nil, 404)
	decodeResponse(t, requestJSON(t, f.handler, "GET", "/api/v1/subscriptions/"+original.ID, f.token, nil, 200), &detail)
	if detail.Links == old {
		t.Fatal("rotate did not change token")
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest("GET", detail.Links.YAML, nil))
	if recorder.Code != 200 || recorder.Header().Get("Cache-Control") != "no-store" || !strings.Contains(recorder.Body.String(), "new.example.test") {
		t.Fatal("invalid YAML response")
	}
	businessRequest(t, f, "POST", "/api/v1/subscriptions/"+original.ID+"/package", "sub-package", map[string]any{"base_url": "https://panel.example.test"}, http.StatusOK)
	qr := businessRequest(t, f, "POST", "/api/v1/subscriptions/"+original.ID+"/qrcode", "", map[string]any{"base_url": "https://panel.example.test", "format": "yaml"}, http.StatusOK)
	var qrResponse struct {
		PNGBase64 string `json:"png_base64"`
	}
	decodeResponse(t, qr, &qrResponse)
	qrBytes, err := base64.StdEncoding.DecodeString(qrResponse.PNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(qrBytes)); err != nil {
		t.Fatal(err)
	}
	businessRequest(t, f, "POST", "/api/v1/subscriptions/"+original.ID+"/qrcode", "", map[string]any{"base_url": "https://panel.example.test", "format": "json"}, http.StatusBadRequest)
	current = action("revoke", current.Revision)
	requestJSON(t, f.handler, "GET", detail.Links.TXT, "", nil, 404)
	action("restore", current.Revision)
	requestJSON(t, f.handler, "GET", detail.Links.TXT, "", nil, 200)
	// A binding from another customer or absent binding cannot be saved or published.
	req.Revision = 0
	req.Lines = []subscriptions.Line{{Name: "unauthorized", BindingID: "binding-missing"}}
	businessRequest(t, f, "POST", "/api/v1/subscriptions", "bad-native-binding", req, 404)
}
