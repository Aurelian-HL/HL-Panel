package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/panellicense"
)

func TestBatchCreateHTTPAuthorizationAtomicValidationAndDurableReplay(t *testing.T) {
	f := newBusinessFixture(t, forwarding.WithRealityTargets(forwarding.DefaultRealityTargets()))
	path := "/api/v1/forwarding-rules/batch-create"
	rule := forwarding.Request{CustomerID: f.customer.ID, Name: "", EntryGroupID: f.entry.ID,
		EgressMode: forwarding.EgressDirect, IngressProtocol: forwarding.IngressVLESSReality,
		Protocol: forwarding.ProtocolTCP, VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5,
		VLESSSOCKS5Host: "192.0.2.10", VLESSSOCKS5Port: 1080,
		VLESSSOCKS5Username: "isolated-sk5-user", VLESSSOCKS5Password: "isolated-sk5-password", SelectionPolicy: forwarding.SelectionRoundRobin}
	input := forwarding.BatchCreateRequest{Rules: []forwarding.Request{rule, rule}}
	input.Rules[1].Name = "客户昵称"
	requestJSON(t, f.handler, http.MethodPost, path, "", input, http.StatusUnauthorized)
	identity, err := customeridentity.NewService(f.store.CustomerIdentityRepository("test"), nil, customeridentity.Options{PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	login, err := identity.Login(context.Background(), f.customer.Username, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	other := f
	other.token = login.AccessToken
	businessRequest(t, other, http.MethodPost, path, "batch-denied", input, http.StatusUnauthorized)
	before, _ := f.store.EncodeSnapshot()
	businessRequest(t, f, http.MethodPost, path, "", input, http.StatusBadRequest)
	input.Rules[1].VLESSSOCKS5Password = ""
	businessRequest(t, f, http.MethodPost, path, "batch-validation", input, http.StatusBadRequest)
	input.Rules[1].VLESSSOCKS5Password = rule.VLESSSOCKS5Password
	input.Rules[1].EntryGroupID = "missing-entry"
	businessRequest(t, f, http.MethodPost, path, "batch-validation", input, http.StatusBadRequest)
	after, _ := f.store.EncodeSnapshot()
	if !bytes.Equal(before, after) {
		t.Fatal("failed HTTP request altered durable state")
	}
	input.Rules[1].EntryGroupID = f.entry.ID
	count := len(f.store.AuditEvents())
	created := businessRequest(t, f, http.MethodPost, path, "batch-create", input, http.StatusCreated)
	var result forwarding.BatchCreateResult
	decodeBusinessEntity(t, created, "result", &result)
	if result.Created != 2 || len(result.Items) != 2 || result.Items[0].Name != rule.VLESSSOCKS5Host || result.Items[1].Name != "客户昵称" {
		t.Fatal("wrong batch result or fallback nickname")
	}
	for _, item := range result.Items {
		if !item.OwnedByAdministrator("adm_business") || item.CustomerID == f.customer.ID {
			t.Fatal("batch hijacked a panel customer")
		}
	}
	if bytes.Contains(created, []byte(rule.VLESSSOCKS5Password)) || len(f.store.AuditEvents()) != count+1 {
		t.Fatal("batch leaked upstream password or appended duplicate audits")
	}
	var envelope struct {
		Replayed bool `json:"replayed"`
	}
	replayed := businessRequest(t, f, http.MethodPost, path, "batch-create", input, http.StatusOK)
	decodeResponse(t, replayed, &envelope)
	if !envelope.Replayed || len(f.store.AuditEvents()) != count+1 {
		t.Fatal("HTTP retry was not idempotent")
	}
	input.Rules[1].Name = "修改后的昵称"
	businessRequest(t, f, http.MethodPost, path, "batch-create", input, http.StatusConflict)
}

func TestBatchCreateHTTPHonorsLicenseRestrictions(t *testing.T) {
	f := newBusinessFixture(t)
	f.handler = panellicense.Guard(panellicense.RestrictedClient(), f.handler)
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch-create", "restricted-batch", forwarding.BatchCreateRequest{}, http.StatusForbidden)
}
