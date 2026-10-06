package httpapi_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestPendingGroupEnrollmentTokensAreScopedAndSecretFree(t *testing.T) {
	fixture := newBusinessFixture(t)
	issue := func(name, groupID string) enrollment.IssueResult {
		t.Helper()
		body := requestJSON(t, fixture.handler, http.MethodPost, "/api/v1/enrollment-tokens", fixture.token, map[string]any{
			"name": name, "group_id": groupID, "expires_in_seconds": 900,
		}, http.StatusCreated)
		var issued enrollment.IssueResult
		decodeResponse(t, body, &issued)
		return issued
	}
	entryToken := issue("entry-node", fixture.entry.ID)
	otherToken := issue("exit-node", fixture.exit.ID)
	path := "/api/v1/device-groups/" + fixture.entry.ID + "/enrollment-tokens"
	requestJSON(t, fixture.handler, http.MethodGet, path, "", nil, http.StatusUnauthorized)
	body := requestJSON(t, fixture.handler, http.MethodGet, path, fixture.token, nil, http.StatusOK)
	var response struct {
		Items []enrollment.PendingToken `json:"items"`
	}
	decodeResponse(t, body, &response)
	if len(response.Items) != 1 || response.Items[0].ID != entryToken.ID || response.Items[0].GroupID != fixture.entry.ID {
		t.Fatalf("pending tokens were not scoped to entry group: %+v", response.Items)
	}
	for _, secret := range []string{entryToken.Token, otherToken.Token, securetoken.Hash(entryToken.Token)} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("pending token listing exposed bearer material")
		}
	}
	requestWithKey(t, fixture.handler, "/api/v1/enrollment-tokens/"+entryToken.ID+"/revoke", fixture.token, "revoke-entry-token", nil, http.StatusCreated)
	body = requestJSON(t, fixture.handler, http.MethodGet, path, fixture.token, nil, http.StatusOK)
	decodeResponse(t, body, &response)
	if len(response.Items) != 0 {
		t.Fatalf("revoked token remained pending: %+v", response.Items)
	}
	usedToken := issue("used-entry-node", fixture.entry.ID)
	requestJSON(t, fixture.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{
		EnrollmentToken: usedToken.Token, Hostname: "used-entry.example.test", Platform: "linux", Architecture: "amd64", AgentVersion: "1.0.0",
	}, http.StatusCreated)
	body = requestJSON(t, fixture.handler, http.MethodGet, path, fixture.token, nil, http.StatusOK)
	decodeResponse(t, body, &response)
	if len(response.Items) != 0 {
		t.Fatalf("used token remained pending: %+v", response.Items)
	}
	requestJSON(t, fixture.handler, http.MethodGet, "/api/v1/device-groups/missing/enrollment-tokens", fixture.token, nil, http.StatusNotFound)
}
