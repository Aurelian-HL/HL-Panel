package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestAgentApplyBatchAuthenticationShapeAndEvidence(t *testing.T) {
	f := newBusinessFixture(t)
	enroll := func(name string) agentv1.EnrollmentResponse {
		var issued enrollment.IssueResult
		decodeResponse(t, requestJSON(t, f.handler, "POST", "/api/v1/enrollment-tokens", f.token, map[string]any{"name": name, "expires_in_seconds": 900}, 201), &issued)
		var node agentv1.EnrollmentResponse
		decodeResponse(t, requestJSON(t, f.handler, "POST", "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{EnrollmentToken: issued.Token, Hostname: name, Platform: "linux", Architecture: "amd64", AgentVersion: "test"}, 201), &node)
		return node
	}
	one, two := enroll("one"), enroll("two")
	addMember(t, f.handler, f.token, f.entry.ID, one.NodeID)
	if _, err := generations.NewService(f.store, time.Now).CreateGroupRevision(context.Background(), "adm_business", f.entry.ID, agentv1.EngineGOST, []byte(`{"services":[]}`), "apply-batch-config"); err != nil {
		t.Fatal(err)
	}
	config, _, err := f.store.DesiredNodeConfig(context.Background(), one.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	commit := agentv1.ApplyResultRequest{Generation: agentv1.NodeConfigGeneration(config.Generation), AttemptID: strings.Repeat("a", 32), ConfigSHA256: config.ConfigSHA256, EngineMode: "gost", Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded}
	verify := commit
	verify.Phase = agentv1.ApplyPhaseVerify
	pair := []agentv1.ApplyResultRequest{commit, verify}
	path := "/api/v1/agent/apply-results/batch"
	requestJSON(t, f.handler, "POST", path, "", agentv1.ApplyResultsRequest{Results: pair}, 401)
	requestJSON(t, f.handler, "POST", path, two.NodeCredential, agentv1.ApplyResultsRequest{Results: pair}, 409)
	requestJSON(t, f.handler, "POST", path, one.NodeCredential, map[string]any{"results": pair, "node_id": two.NodeID}, 400)
	for _, bad := range [][]agentv1.ApplyResultRequest{nil, {verify, commit}, {commit}, {commit, verify, verify}} {
		requestJSON(t, f.handler, "POST", path, one.NodeCredential, agentv1.ApplyResultsRequest{Results: bad}, 400)
	}
	before := f.store.AuditEvents()
	for i := 0; i < 2; i++ {
		requestJSON(t, f.handler, http.MethodPost, path, one.NodeCredential, agentv1.ApplyResultsRequest{Results: pair}, 204)
	}
	after := f.store.AuditEvents()
	if len(after) != len(before)+2 {
		t.Fatal("missing phase evidence or duplicate audit", err)
	}
	requestJSON(t, f.handler, "GET", "/api/v1/agent/desired", one.NodeCredential, nil, 204)
}
