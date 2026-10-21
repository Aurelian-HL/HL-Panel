package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func TestDeletionContractJSONRevisionAndIfMatch(t *testing.T) {
	f := newBusinessFixture(t)
	var rule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules", "delete-contract-create", f.ruleRequest(), http.StatusCreated), "rule", &rule)
	path := "/api/v1/forwarding-rules/batch"
	request := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{rule.ID}}
	assertDeletionProblem(t, businessRequest(t, f, http.MethodPost, path, "delete-contract-retry", request, http.StatusBadRequest), "validation_error", "revision")
	request.ExpectedRevisions = map[string]int64{rule.ID: rule.Revision + 1}
	assertDeletionProblem(t, businessRequest(t, f, http.MethodPost, path, "delete-contract-retry", request, http.StatusConflict), "conflict", "changed")
	if _, err := f.store.ForwardingRule(context.Background(), rule.ID); err != nil {
		t.Fatal("a rejected deletion changed the rule")
	}
	request.ExpectedRevisions[rule.ID] = rule.Revision
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+f.token)
	httpRequest.Header.Set("Idempotency-Key", "delete-contract-retry")
	httpRequest.Header.Set("If-Match", `"deliberately-unrelated-etag"`)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != "" {
		t.Fatalf("the JSON revision must govern deletion without an ETag: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	auditCount := len(f.store.AuditEvents())
	var replay struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, path, "delete-contract-retry", request, http.StatusOK), &replay)
	if !replay.Replayed || len(f.store.AuditEvents()) != auditCount {
		t.Fatal("an exact deletion replay must not add another audit")
	}
	request.ExpectedRevisions[rule.ID]++
	assertDeletionProblem(t, businessRequest(t, f, http.MethodPost, path, "delete-contract-retry", request, http.StatusConflict), "idempotency_conflict", "")
}

func TestDeletionContractSingleRuleDeleteRemainsUnsupported(t *testing.T) {
	f := newBusinessFixture(t)
	businessRequest(t, f, http.MethodDelete, "/api/v1/forwarding-rules/example", "unsupported-delete-contract", map[string]any{"revision": 1}, http.StatusMethodNotAllowed)
	if _, err := f.store.Customer(context.Background(), f.customer.ID); err != nil {
		t.Fatal("an unsupported DELETE route changed its customer")
	}
}

func assertDeletionProblem(t *testing.T, body []byte, code, contains string) {
	t.Helper()
	var response struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeResponse(t, body, &response)
	if response.Error.Code != code || !strings.Contains(response.Error.Message, contains) {
		t.Fatalf("unexpected deletion problem: %s", body)
	}
}
