package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type stubService struct {
	report       usage.Report
	ingestResult usage.IngestResult
	ingestErr    error
	query        usage.Query
	queryResult  usage.QueryResult
	queryErr     error
	command      *agentv1.EnforcementCommand
	commandNode  string
	commandErr   error
	resultInput  agentv1.EnforcementResultRequest
	resultNode   string
	resultErr    error
	revokeAdmin  string
	revokeID     string
	revokeKey    string
}

func (service *stubService) Ingest(_ context.Context, report usage.Report) (usage.IngestResult, error) {
	service.report = report
	return service.ingestResult, service.ingestErr
}

func (service *stubService) Query(_ context.Context, query usage.Query) (usage.QueryResult, error) {
	service.query = query
	return service.queryResult, service.queryErr
}

func (service *stubService) DesiredEnforcement(_ context.Context, nodeID string) (*agentv1.EnforcementCommand, error) {
	service.commandNode = nodeID
	return service.command, service.commandErr
}

func (service *stubService) RecordEnforcementResult(_ context.Context, nodeID string, input agentv1.EnforcementResultRequest) (usage.EnforcementDecision, bool, error) {
	service.resultNode = nodeID
	service.resultInput = input
	return usage.EnforcementDecision{}, false, service.resultErr
}

func (service *stubService) RequestRevoke(_ context.Context, administratorID, decisionID, key string) (usage.EnforcementDecision, bool, error) {
	service.revokeAdmin, service.revokeID, service.revokeKey = administratorID, decisionID, key
	return usage.EnforcementDecision{ID: decisionID, Status: usage.EnforcementRevokePending}, false, nil
}

func reportBody(nodeID string) string {
	return `{"node_id":"` + nodeID + `","boot_id":"boot-one","sequence":1,"customer_id":"customer-one","rule_id":"rule-one","entry_group_id":"entry-one","exit_group_id":"","protocol":"tcp","occurred_at":"2026-10-03T01:00:00Z","period_started_at":"2026-10-03T00:59:00Z","period_ended_at":"2026-10-03T01:00:00Z","rule_actual_bytes":100,"customer_actual_bytes":100,"entry_multiplier_micros":1000000,"exit_multiplier_micros":1000000}`
}

func testHandler(t *testing.T, service *stubService, nodeID string, adminError error) http.Handler {
	t.Helper()
	handler, err := New(service, func(*http.Request) (string, error) { return nodeID, nil }, func(*http.Request) (string, error) { return "admin-one", adminError })
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := handler.Register(mux); err != nil {
		t.Fatal(err)
	}
	return mux
}

func TestIngestUsesAuthenticatedNodeAndReturnsCreatedOrReplay(t *testing.T) {
	service := &stubService{ingestResult: usage.IngestResult{Event: usage.Event{Report: usage.Report{NodeID: "node-authenticated"}}}}
	handler := testHandler(t, service, "node-authenticated", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/usage/reports", strings.NewReader(reportBody("")))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || service.report.NodeID != "node-authenticated" {
		t.Fatalf("status=%d authenticated node=%q body=%s", response.Code, service.report.NodeID, response.Body.String())
	}
	service.ingestResult.Replayed = true
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/usage/reports", strings.NewReader(reportBody("node-authenticated"))))
	if response.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestIngestRejectsNodeSpoofingBeforeService(t *testing.T) {
	service := &stubService{}
	handler := testHandler(t, service, "node-authenticated", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/usage/reports", strings.NewReader(reportBody("node-other"))))
	if response.Code != http.StatusForbidden || service.report.BootID != "" {
		t.Fatalf("node spoof status=%d service report=%#v", response.Code, service.report)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Code != "node_identity_mismatch" {
		t.Fatalf("unexpected error body: %s", response.Body.String())
	}
}

func TestIngestMapsSequenceErrorsToStableContract(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{usage.ErrSequenceGap, http.StatusConflict, "sequence_gap"},
		{usage.ErrSequenceOutOfOrder, http.StatusConflict, "sequence_out_of_order"},
		{usage.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
		{usage.ErrNegativeBytes, http.StatusUnprocessableEntity, "negative_usage_bytes"},
		{usage.ErrInvalidMultiplier, http.StatusUnprocessableEntity, "invalid_usage_multiplier"},
		{usage.ErrByteOverflow, http.StatusUnprocessableEntity, "usage_byte_overflow"},
	}
	for _, test := range tests {
		service := &stubService{ingestErr: test.err}
		handler := testHandler(t, service, "node-one", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/usage/reports", strings.NewReader(reportBody("node-one"))))
		if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("error=%v status=%d body=%s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestQueryRequiresAdminAndParsesFilters(t *testing.T) {
	service := &stubService{queryResult: usage.QueryResult{Items: []usage.Record{}}}
	handler := testHandler(t, service, "node-one", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/usage?scope=customer&scope_id=customer-one&from=2026-10-01T00%3A00%3A00Z&page=2&page_size=25", nil))
	if response.Code != http.StatusOK || service.query.Scope != usage.ScopeCustomer || service.query.Page != 2 || service.query.PageSize != 25 {
		t.Fatalf("status=%d query=%#v body=%s", response.Code, service.query, response.Body.String())
	}
	if service.query.From == nil || !service.query.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("RFC3339 from filter was not parsed")
	}
	unauthorized := testHandler(t, &stubService{}, "node-one", faults.ErrUnauthorized)
	response = httptest.NewRecorder()
	unauthorized.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("admin auth status=%d", response.Code)
	}
}

func TestDesiredEnforcementUsesAuthenticatedNodeAndSupportsNoContent(t *testing.T) {
	service := &stubService{command: &agentv1.EnforcementCommand{
		DecisionID: "decision-one", CustomerID: "customer-one", RuleID: "rule-one", Protocol: "tcp",
		Action: agentv1.EnforcementDisableCustomer, Revision: 1,
	}}
	handler := testHandler(t, service, "node-authenticated", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/usage/enforcement/desired", nil))
	if response.Code != http.StatusOK || service.commandNode != "node-authenticated" || !strings.Contains(response.Body.String(), `"decision_id":"decision-one"`) {
		t.Fatalf("status=%d node=%q body=%s", response.Code, service.commandNode, response.Body.String())
	}
	service.command = nil
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/usage/enforcement/desired", nil))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("empty command status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRecordEnforcementResultUsesAuthenticatedNode(t *testing.T) {
	service := &stubService{}
	handler := testHandler(t, service, "node-authenticated", nil)
	body := `{"decision_id":"decision-one","action":"disable_customer_access","revision":1,"status":"succeeded"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/usage/enforcement/results", strings.NewReader(body)))
	if response.Code != http.StatusNoContent || service.resultNode != "node-authenticated" || service.resultInput.DecisionID != "decision-one" {
		t.Fatalf("status=%d node=%q input=%#v body=%s", response.Code, service.resultNode, service.resultInput, response.Body.String())
	}
}

func TestRevokeEnforcementRequiresAdminAndForwardsIdempotencyKey(t *testing.T) {
	service := &stubService{}
	handler := testHandler(t, service, "node-one", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/usage/enforcement/decision-one/revoke", nil)
	request.Header.Set("Idempotency-Key", "revoke-key-one")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.revokeAdmin != "admin-one" || service.revokeID != "decision-one" || service.revokeKey != "revoke-key-one" {
		t.Fatalf("status=%d admin=%q decision=%q key=%q body=%s", response.Code, service.revokeAdmin, service.revokeID, service.revokeKey, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"status":"revoke_pending"`) {
		t.Fatalf("missing revoke decision: %s", response.Body.String())
	}

	unauthorized := testHandler(t, &stubService{}, "node-one", faults.ErrUnauthorized)
	response = httptest.NewRecorder()
	unauthorized.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/usage/enforcement/decision-one/revoke", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("admin auth status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNewRefusesUnauthenticatedRegistration(t *testing.T) {
	if _, err := New(&stubService{}, nil, func(*http.Request) (string, error) { return "admin", nil }); err == nil {
		t.Fatal("missing node authentication callback accepted")
	}
	if _, err := New(&stubService{}, func(*http.Request) (string, error) { return "", errors.New("no") }, nil); err == nil {
		t.Fatal("missing administrator authentication callback accepted")
	}
}
