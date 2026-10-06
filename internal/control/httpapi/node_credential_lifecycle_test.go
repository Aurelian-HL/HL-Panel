package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestNodeCredentialRotationAndEnrollmentTokenRevocation(t *testing.T) {
	fixture := newBusinessFixture(t)
	issuedBody := requestJSON(t, fixture.handler, http.MethodPost, "/api/v1/enrollment-tokens", fixture.token, map[string]any{
		"name": "rotation-node", "group_id": fixture.entry.ID, "expires_in_seconds": 900,
	}, http.StatusCreated)
	var issued enrollment.IssueResult
	decodeResponse(t, issuedBody, &issued)
	if issued.Token == "" || issued.ID == "" {
		t.Fatal("missing enrollment token")
	}
	revokePath := "/api/v1/enrollment-tokens/" + issued.ID + "/revoke"
	requestWithKey(t, fixture.handler, revokePath, "", "revoke-token", nil, http.StatusUnauthorized)
	requestWithKey(t, fixture.handler, revokePath, fixture.token, "", nil, http.StatusBadRequest)
	firstRevoke := requestWithKey(t, fixture.handler, revokePath, fixture.token, "revoke-token", nil, http.StatusCreated)
	var revoked struct {
		Token    enrollment.RevokeResult `json:"token"`
		Replayed bool                    `json:"replayed"`
	}
	decodeResponse(t, firstRevoke, &revoked)
	if revoked.Replayed || revoked.Token.ID != issued.ID || bytes.Contains(firstRevoke, []byte(issued.Token)) {
		t.Fatalf("invalid token revocation response: %s", firstRevoke)
	}
	replayRevoke := requestWithKey(t, fixture.handler, revokePath, fixture.token, "revoke-token", nil, http.StatusOK)
	decodeResponse(t, replayRevoke, &revoked)
	if !revoked.Replayed {
		t.Fatal("token revocation did not replay")
	}
	enrollInput := agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token, Hostname: "edge.example.test", Platform: "linux", Architecture: "amd64", AgentVersion: "1.0.0",
	}
	requestJSON(t, fixture.handler, http.MethodPost, "/api/v1/agent/enroll", "", enrollInput, http.StatusConflict)

	activeBody := requestJSON(t, fixture.handler, http.MethodPost, "/api/v1/enrollment-tokens", fixture.token, map[string]any{
		"name": "active-node", "expires_in_seconds": 900,
	}, http.StatusCreated)
	var active enrollment.IssueResult
	decodeResponse(t, activeBody, &active)
	enrollInput.EnrollmentToken = active.Token
	enrolledBody := requestJSON(t, fixture.handler, http.MethodPost, "/api/v1/agent/enroll", "", enrollInput, http.StatusCreated)
	var enrolled agentv1.EnrollmentResponse
	decodeResponse(t, enrolledBody, &enrolled)
	requestWithKey(t, fixture.handler, "/api/v1/enrollment-tokens/"+active.ID+"/revoke", fixture.token, "used-token", nil, http.StatusConflict)

	rotatePath := "/api/v1/nodes/" + enrolled.NodeID + "/credential/rotate"
	requestWithKey(t, fixture.handler, rotatePath, "", "rotate-node", map[string]any{}, http.StatusUnauthorized)
	requestWithKey(t, fixture.handler, rotatePath, fixture.token, "", map[string]any{}, http.StatusBadRequest)
	firstRotation := requestWithKey(t, fixture.handler, rotatePath, fixture.token, "rotate-node", map[string]any{}, http.StatusCreated)
	var rotated struct {
		Credential nodes.CredentialRotationResult `json:"credential"`
		Replayed   bool                           `json:"replayed"`
	}
	decodeResponse(t, firstRotation, &rotated)
	if rotated.Replayed || rotated.Credential.NodeID != enrolled.NodeID || rotated.Credential.NodeCredential == "" || rotated.Credential.NodeCredential == enrolled.NodeCredential {
		t.Fatalf("invalid first rotation response: %s", firstRotation)
	}
	newCredential := rotated.Credential.NodeCredential
	requestJSON(t, fixture.handler, http.MethodGet, "/api/v1/agent/desired", enrolled.NodeCredential, nil, http.StatusUnauthorized)
	requestJSON(t, fixture.handler, http.MethodGet, "/api/v1/agent/desired", newCredential, nil, http.StatusNoContent)
	replayRotation := requestWithKey(t, fixture.handler, rotatePath, fixture.token, "rotate-node", map[string]any{}, http.StatusOK)
	var replayedRotation struct {
		Credential nodes.CredentialRotationResult `json:"credential"`
		Replayed   bool                           `json:"replayed"`
	}
	decodeResponse(t, replayRotation, &replayedRotation)
	if !replayedRotation.Replayed || replayedRotation.Credential.NodeCredential != "" || bytes.Contains(replayRotation, []byte(enrolled.NodeCredential)) || bytes.Contains(replayRotation, []byte(newCredential)) {
		t.Fatalf("rotation replay leaked credential: %s", replayRotation)
	}
	snapshot, err := fixture.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snapshot, []byte(newCredential)) || bytes.Contains(snapshot, []byte(enrolled.NodeCredential)) || bytes.Contains(snapshot, []byte(active.Token)) {
		t.Fatal("snapshot leaked raw bearer credential")
	}
	auditEvents, err := json.Marshal(fixture.store.AuditEvents())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{issued.Token, active.Token, enrolled.NodeCredential, newCredential} {
		if bytes.Contains(auditEvents, []byte(secret)) {
			t.Fatal("audit event leaked raw bearer credential")
		}
	}
	if bytes.Contains(firstRevoke, []byte(issued.Token)) {
		t.Fatal("revocation leaked raw enrollment token")
	}
	restored, err := memoryrepo.DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredNodes := nodes.NewService(restored, time.Now, time.Minute)
	if _, err := restoredNodes.AuthenticateCredential(context.Background(), newCredential); err != nil {
		t.Fatalf("rotated credential not durable: %v", err)
	}
	if _, err := restoredNodes.AuthenticateCredential(context.Background(), enrolled.NodeCredential); err == nil {
		t.Fatal("retired node credential became valid after restore")
	}
	restoredEnrollment := enrollment.NewService(restored, time.Now, time.Hour)
	if _, err := restoredEnrollment.Enroll(context.Background(), enrollment.EnrollInput{
		RawToken: issued.Token, Hostname: "edge.example.test", Platform: "linux", Architecture: "amd64", AgentVersion: "1.0.0",
	}); err == nil {
		t.Fatal("revoked group token became valid after restore")
	}
}

func requestWithKey(t *testing.T, handler http.Handler, path, bearer, key string, body any, expectedStatus int) []byte {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != expectedStatus {
		t.Fatalf("POST %s status=%d, want=%d, body=%s", path, response.Code, expectedStatus, response.Body.String())
	}
	return response.Body.Bytes()
}
