package controlclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestClientUsesAgentPathsAndBearerCredential(t *testing.T) {
	t.Parallel()
	const credential = "node-secret"
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		switch request.URL.Path {
		case "/api/v1/agent/enroll":
			if request.Header.Get("Authorization") != "" {
				t.Errorf("enrollment unexpectedly sent Authorization header")
			}
			var enrollment agentv1.EnrollmentRequest
			if err := json.NewDecoder(request.Body).Decode(&enrollment); err != nil {
				t.Errorf("decode enrollment request: %v", err)
			}
			if enrollment.EnrollmentToken != "one-time" {
				t.Errorf("enrollment token = %q", enrollment.EnrollmentToken)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.EnrollmentResponse{NodeID: "node-1", NodeCredential: credential})
		case "/api/v1/agent/heartbeat":
			assertBearer(t, request, credential)
			writer.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/desired":
			assertBearer(t, request, credential)
			writer.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/apply-results":
			assertBearer(t, request, credential)
			var result agentv1.ApplyResultRequest
			if err := json.NewDecoder(request.Body).Decode(&result); err != nil {
				t.Errorf("decode apply result: %v", err)
			}
			if result.Phase != agentv1.ApplyPhaseVerify {
				t.Errorf("apply phase = %q", result.Phase)
			}
			writer.WriteHeader(http.StatusAccepted)
		case "/api/v1/usage/reports":
			assertBearer(t, request, credential)
			var report agentv1.UsageReport
			if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
				t.Errorf("decode usage report: %v", err)
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"acknowledgement": agentv1.UsageAcknowledgement{
				NodeID: report.NodeID, BootID: report.BootID, Sequence: report.Sequence,
			}})
		case "/api/v1/usage/enforcement/desired":
			assertBearer(t, request, credential)
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(agentv1.EnforcementCommand{
				DecisionID: "decision-one", CustomerID: "customer-one", RuleID: "rule-one", Protocol: "tcp",
				Action: agentv1.EnforcementDisableCustomer, Revision: 1,
			})
		case "/api/v1/usage/enforcement/results":
			assertBearer(t, request, credential)
			var result agentv1.EnforcementResultRequest
			if err := json.NewDecoder(request.Body).Decode(&result); err != nil {
				t.Errorf("decode enforcement result: %v", err)
			}
			if result.DecisionID != "decision-one" || result.Status != agentv1.EnforcementResultSucceeded {
				t.Errorf("enforcement result = %#v", result)
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response, err := client.Enroll(context.Background(), agentv1.EnrollmentRequest{EnrollmentToken: "one-time"})
	if err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	if response.NodeCredential != credential {
		t.Fatalf("node credential = %q", response.NodeCredential)
	}
	if err := client.Heartbeat(context.Background(), credential, agentv1.HeartbeatRequest{}); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	desired, err := client.Desired(context.Background(), credential)
	if err != nil {
		t.Fatalf("Desired() error = %v", err)
	}
	if desired != nil {
		t.Fatalf("Desired() = %#v, want nil for HTTP 204", desired)
	}
	if err := client.ReportApplyResult(context.Background(), credential, agentv1.ApplyResultRequest{Phase: agentv1.ApplyPhaseVerify}); err != nil {
		t.Fatalf("ReportApplyResult() error = %v", err)
	}
	acknowledgement, err := client.ReportUsage(context.Background(), credential, agentv1.UsageReport{NodeID: "node-1", BootID: "boot-1", Sequence: 1})
	if err != nil || acknowledgement.Sequence != 1 {
		t.Fatalf("ReportUsage() = (%#v, %v)", acknowledgement, err)
	}
	command, err := client.DesiredEnforcement(context.Background(), credential)
	if err != nil || command == nil || command.DecisionID != "decision-one" {
		t.Fatalf("DesiredEnforcement() = (%#v, %v)", command, err)
	}
	if err := client.ReportEnforcementResult(context.Background(), credential, agentv1.EnforcementResultRequest{
		DecisionID: command.DecisionID, Action: command.Action, Revision: command.Revision, Status: agentv1.EnforcementResultSucceeded,
	}); err != nil {
		t.Fatalf("ReportEnforcementResult() error = %v", err)
	}

	wantPaths := []string{
		"/api/v1/agent/enroll",
		"/api/v1/agent/heartbeat",
		"/api/v1/agent/desired",
		"/api/v1/agent/apply-results",
		"/api/v1/usage/reports",
		"/api/v1/usage/enforcement/desired",
		"/api/v1/usage/enforcement/results",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestDesiredDecodesCompleteNodeBundle(t *testing.T) {
	t.Parallel()
	want := agentv1.DesiredNodeConfig{
		Generation:   7,
		Engine:       agentv1.EngineNodeBundle,
		ConfigSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Config:       json.RawMessage(`{"schema_version":1,"fragments":[]}`),
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(want)
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got, err := client.Desired(context.Background(), "credential")
	if err != nil {
		t.Fatalf("Desired() error = %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("Desired() = %#v, want %#v", *got, want)
	}
}

func TestHTTPErrorRedactsCredentialLikeValues(t *testing.T) {
	t.Parallel()
	err := httpError(http.StatusUnauthorized, []byte("Bearer node-secret token=bootstrap password=hunter2"))
	message := err.Error()
	for _, secret := range []string{"node-secret", "bootstrap", "hunter2"} {
		if strings.Contains(message, secret) {
			t.Fatalf("HTTP error leaked %q: %s", secret, message)
		}
	}
}

func assertBearer(t *testing.T, request *http.Request, credential string) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer "+credential {
		t.Errorf("Authorization = %q", got)
	}
}
