package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestVerticalSliceLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	passwordHash, err := auth.HashPassword("test-administrator-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	store := memoryrepo.New(auth.Administrator{
		ID:           "adm_test",
		Username:     "admin",
		PasswordHash: passwordHash,
		CreatedAt:    now,
	})
	auditService := audit.NewService(store)
	handler := httpapi.New(
		auth.NewService(store, auditService, clock, time.Hour),
		enrollment.NewService(store, clock, time.Hour),
		nodes.NewService(store, clock, 90*time.Second),
		groups.NewService(store, clock),
		endpoints.NewService(store, clock),
		generations.NewService(store, clock),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	loginBody := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "admin",
		"password": "test-administrator-password",
	}, http.StatusOK)
	var login auth.LoginResult
	decodeResponse(t, loginBody, &login)
	if login.AccessToken == "" {
		t.Fatal("login did not return an access token")
	}

	tokenBody := requestJSON(t, handler, http.MethodPost, "/api/v1/enrollment-tokens", login.AccessToken, map[string]any{
		"name":               "edge-01",
		"expires_in_seconds": 900,
	}, http.StatusCreated)
	var issued enrollment.IssueResult
	decodeResponse(t, tokenBody, &issued)
	if issued.Token == "" {
		t.Fatal("enrollment token was not returned on creation")
	}

	enrollmentRequest := agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token,
		Hostname:        "edge-01.example.internal",
		Platform:        "linux",
		Architecture:    "amd64",
		AgentVersion:    "0.1.0",
		Capabilities:    []string{"xray", "gost"},
	}
	enrolledBody := requestJSON(t, handler, http.MethodPost, "/api/v1/agent/enroll", "", enrollmentRequest, http.StatusCreated)
	var enrolled agentv1.EnrollmentResponse
	decodeResponse(t, enrolledBody, &enrolled)
	if enrolled.NodeID == "" || enrolled.NodeCredential == "" {
		t.Fatal("enrollment response did not return the one-time node credential")
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/enroll", "", enrollmentRequest, http.StatusUnauthorized)

	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/heartbeat", enrolled.NodeCredential, agentv1.HeartbeatRequest{
		BootID:            "boot-a",
		Hostname:          enrollmentRequest.Hostname,
		Platform:          enrollmentRequest.Platform,
		Architecture:      enrollmentRequest.Architecture,
		AgentVersion:      enrollmentRequest.AgentVersion,
		Capabilities:      enrollmentRequest.Capabilities,
		EngineVersions:    map[string]string{"xray": "25.9.11"},
		AppliedGeneration: 0,
	}, http.StatusNoContent)

	groupA := createGroup(t, handler, login.AccessToken, "gz-entry", groups.KindEntry)
	groupB := createGroup(t, handler, login.AccessToken, "gz-egress", groups.KindExit)
	addMember(t, handler, login.AccessToken, groupA.ID, enrolled.NodeID)
	addMember(t, handler, login.AccessToken, groupB.ID, enrolled.NodeID)

	poolRequest := map[string]any{
		"name":             "public-vless",
		"group_id":         groupA.ID,
		"mode":             "SINGLE_SERVICE_ENDPOINT",
		"protocol":         "vless",
		"hostname":         "edge.example.test",
		"port":             443,
		"selection_policy": "weighted_least_connections",
		"idempotency_key":  "pool-create-1",
	}
	poolBody := requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools", login.AccessToken, poolRequest, http.StatusCreated)
	var poolResult struct {
		Pool     endpoints.EndpointPool `json:"pool"`
		Replayed bool                   `json:"replayed"`
	}
	decodeResponse(t, poolBody, &poolResult)
	if poolResult.Pool.ID == "" || poolResult.Pool.GroupID != groupA.ID || poolResult.Pool.MemberCount != 1 || poolResult.Pool.HealthyCandidateCount != 0 || poolResult.Replayed {
		t.Fatalf("unexpected endpoint pool creation result: %+v", poolResult)
	}

	replayBody := requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools", login.AccessToken, poolRequest, http.StatusOK)
	var replayPool struct {
		Pool     endpoints.EndpointPool `json:"pool"`
		Replayed bool                   `json:"replayed"`
	}
	decodeResponse(t, replayBody, &replayPool)
	if !replayPool.Replayed || replayPool.Pool.ID != poolResult.Pool.ID {
		t.Fatalf("endpoint pool idempotent replay changed resource: %+v", replayPool)
	}

	conflictingPoolRequest := map[string]any{}
	for key, value := range poolRequest {
		conflictingPoolRequest[key] = value
	}
	conflictingPoolRequest["hostname"] = "different.example.test"
	requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools", login.AccessToken, conflictingPoolRequest, http.StatusConflict)
	requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools", login.AccessToken, map[string]any{
		"name":             "another-pool",
		"group_id":         groupA.ID,
		"mode":             "SINGLE_SERVICE_ENDPOINT",
		"protocol":         "vless",
		"hostname":         "edge.example.test",
		"port":             443,
		"selection_policy": "weighted_round_robin",
		"idempotency_key":  "pool-create-2",
	}, http.StatusConflict)

	requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools/"+poolResult.Pool.ID+"/members", login.AccessToken, map[string]any{
		"node_id":         "node-not-in-group",
		"weight":          100,
		"priority":        0,
		"idempotency_key": "member-create-invalid",
	}, http.StatusConflict)
	memberBody := requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools/"+poolResult.Pool.ID+"/members", login.AccessToken, map[string]any{
		"node_id":         enrolled.NodeID,
		"weight":          100,
		"priority":        0,
		"idempotency_key": "member-create-1",
	}, http.StatusOK)
	var memberResult struct {
		Member   endpoints.EndpointPoolMember `json:"member"`
		Replayed bool                         `json:"replayed"`
	}
	decodeResponse(t, memberBody, &memberResult)
	if memberResult.Member.NodeID != enrolled.NodeID || memberResult.Member.GroupID != groupA.ID || memberResult.Replayed {
		t.Fatalf("unexpected endpoint member result: %+v", memberResult)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools/"+poolResult.Pool.ID+"/members", login.AccessToken, map[string]any{
		"node_id":         enrolled.NodeID,
		"weight":          200,
		"priority":        0,
		"idempotency_key": "member-override-rejected",
	}, http.StatusConflict)
	replayMemberBody := requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools/"+poolResult.Pool.ID+"/members", login.AccessToken, map[string]any{
		"node_id":         enrolled.NodeID,
		"weight":          100,
		"priority":        0,
		"idempotency_key": "member-create-1",
	}, http.StatusOK)
	var replayMember struct {
		Member   endpoints.EndpointPoolMember `json:"member"`
		Replayed bool                         `json:"replayed"`
	}
	decodeResponse(t, replayMemberBody, &replayMember)
	if !replayMember.Replayed || replayMember.Member.NodeID != enrolled.NodeID {
		t.Fatalf("endpoint member idempotent replay failed: %+v", replayMember)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/endpoint-pools/"+poolResult.Pool.ID+"/members", login.AccessToken, map[string]any{
		"node_id":         enrolled.NodeID,
		"weight":          200,
		"priority":        0,
		"idempotency_key": "member-create-1",
	}, http.StatusConflict)

	poolListBody := requestJSON(t, handler, http.MethodGet, "/api/v1/endpoint-pools", login.AccessToken, nil, http.StatusOK)
	var poolList struct {
		Items []endpoints.EndpointPool `json:"items"`
	}
	decodeResponse(t, poolListBody, &poolList)
	if len(poolList.Items) != 1 || poolList.Items[0].MemberCount != 1 || poolList.Items[0].HealthyCandidateCount != 0 || bytes.Contains(poolListBody, []byte(enrolled.NodeID)) {
		t.Fatalf("endpoint pool list leaked members or has wrong count: %s", poolListBody)
	}

	requestJSON(t, handler, http.MethodGet, "/api/v1/endpoint-pools/"+poolResult.Pool.ID+"/subscription", login.AccessToken, nil, http.StatusNotFound)

	revisionA := publishRevision(t, handler, login.AccessToken, groupA.ID, agentv1.EngineXray, json.RawMessage(`{
		"services": [{"id":"vless-main"}], "schema_version": 1
	}`), "group-a-1", http.StatusCreated)
	if revisionA.Generation.Revision != 1 || len(revisionA.Assignments) != 1 || revisionA.Assignments[0].Generation != 1 {
		t.Fatalf("unexpected first revision result: %+v", revisionA)
	}
	revisionB := publishRevision(t, handler, login.AccessToken, groupB.ID, agentv1.EngineGOST, json.RawMessage(`{"schema_version":1,"services":[{"id":"exit-main"}]}`), "group-b-1", http.StatusCreated)
	if revisionB.Generation.Revision != 1 || len(revisionB.Assignments) != 1 || revisionB.Assignments[0].Generation != 2 {
		t.Fatalf("unexpected second revision result: %+v", revisionB)
	}

	desiredBody := requestJSON(t, handler, http.MethodGet, "/api/v1/agent/desired", enrolled.NodeCredential, nil, http.StatusOK)
	var desired agentv1.DesiredNodeConfig
	decodeResponse(t, desiredBody, &desired)
	if desired.Generation != 2 || desired.Engine != agentv1.EngineNodeBundle {
		t.Fatalf("unexpected desired node config: %+v", desired)
	}
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(desired.Config, &bundle); err != nil {
		t.Fatalf("decode node bundle: %v", err)
	}
	if len(bundle.Fragments) != 2 || bundle.Fragments[0].GroupID > bundle.Fragments[1].GroupID {
		t.Fatalf("node bundle does not contain sorted fragments from both groups: %+v", bundle.Fragments)
	}
	if !((bundle.Fragments[0].GroupID == groupA.ID || bundle.Fragments[0].GroupID == groupB.ID) &&
		(bundle.Fragments[1].GroupID == groupA.ID || bundle.Fragments[1].GroupID == groupB.ID) &&
		bundle.Fragments[0].GroupID != bundle.Fragments[1].GroupID) {
		t.Fatalf("node bundle does not contain both group fragments: %+v", bundle.Fragments)
	}

	replay := publishRevision(t, handler, login.AccessToken, groupA.ID, agentv1.EngineXray, json.RawMessage(`{"schema_version":1,"services":[{"id":"vless-main"}]}`), "group-a-1", http.StatusOK)
	if !replay.Replayed || replay.Generation.Revision != 1 || replay.Assignments[0].Generation != 1 {
		t.Fatalf("idempotent replay changed the original revision: %+v", replay)
	}
	publishRevision(t, handler, login.AccessToken, groupA.ID, agentv1.EngineXray, json.RawMessage(`{"schema_version":1,"services":[]}`), "group-a-1", http.StatusConflict)

	// A heartbeat cannot self-assert successful application. Only a committed and
	// verified apply result can advance the control-plane applied generation.
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/heartbeat", enrolled.NodeCredential, agentv1.HeartbeatRequest{
		BootID:              "boot-a",
		Hostname:            enrollmentRequest.Hostname,
		Platform:            enrollmentRequest.Platform,
		Architecture:        enrollmentRequest.Architecture,
		AgentVersion:        enrollmentRequest.AgentVersion,
		Capabilities:        enrollmentRequest.Capabilities,
		EngineVersions:      map[string]string{"xray": "25.9.11"},
		AppliedGeneration:   desired.Generation,
		AppliedConfigSHA256: desired.ConfigSHA256,
		LastApplyStatus:     agentv1.ApplyStatusSucceeded,
	}, http.StatusNoContent)
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/desired", enrolled.NodeCredential, nil, http.StatusOK)

	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/apply-results", enrolled.NodeCredential, agentv1.ApplyResultRequest{
		Generation:   desired.Generation,
		Phase:        agentv1.ApplyPhaseVerify,
		Status:       agentv1.ApplyStatusSucceeded,
		ConfigSHA256: desired.ConfigSHA256,
	}, http.StatusConflict)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/apply-results", enrolled.NodeCredential, agentv1.ApplyResultRequest{
		Generation:   desired.Generation,
		Phase:        agentv1.ApplyPhaseCommit,
		Status:       agentv1.ApplyStatusSucceeded,
		ConfigSHA256: strings.Repeat("0", 64),
	}, http.StatusConflict)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/apply-results", enrolled.NodeCredential, agentv1.ApplyResultRequest{
		Generation:   desired.Generation,
		Phase:        agentv1.ApplyPhaseCommit,
		Status:       agentv1.ApplyStatusSucceeded,
		ConfigSHA256: desired.ConfigSHA256,
	}, http.StatusNoContent)
	requestJSON(t, handler, http.MethodPost, "/api/v1/agent/apply-results", enrolled.NodeCredential, agentv1.ApplyResultRequest{
		Generation:   desired.Generation,
		Phase:        agentv1.ApplyPhaseVerify,
		Status:       agentv1.ApplyStatusSucceeded,
		ConfigSHA256: desired.ConfigSHA256,
	}, http.StatusNoContent)
	requestJSON(t, handler, http.MethodGet, "/api/v1/agent/desired", enrolled.NodeCredential, nil, http.StatusNoContent)

	nodesBody := requestJSON(t, handler, http.MethodGet, "/api/v1/nodes", login.AccessToken, nil, http.StatusOK)
	if bytes.Contains(nodesBody, []byte(enrolled.NodeCredential)) || bytes.Contains(nodesBody, []byte("credential_hash")) {
		t.Fatal("node list leaked a node credential or credential hash")
	}
	var inventory struct {
		Items []nodes.View `json:"items"`
	}
	decodeResponse(t, nodesBody, &inventory)
	if len(inventory.Items) != 1 || inventory.Items[0].AppliedGeneration != 2 || inventory.Items[0].Status != "online" {
		t.Fatalf("unexpected node inventory: %+v", inventory.Items)
	}

	overviewBody := requestJSON(t, handler, http.MethodGet, "/api/v1/overview", login.AccessToken, nil, http.StatusOK)
	var overview nodes.Overview
	decodeResponse(t, overviewBody, &overview)
	if overview.NodeCount != 1 || overview.GroupCount != 2 || overview.OnlineNodeCount != 1 || overview.SyncingNodeCount != 0 || overview.FailedApplyCount != 0 {
		t.Fatalf("unexpected overview: %+v", overview)
	}

	auditJSON, err := json.Marshal(store.AuditEvents())
	if err != nil {
		t.Fatalf("marshal audit events: %v", err)
	}
	for _, secret := range []string{issued.Token, enrolled.NodeCredential, login.AccessToken, "test-administrator-password"} {
		if bytes.Contains(auditJSON, []byte(secret)) {
			t.Fatalf("audit events leaked secret %q", secret)
		}
	}

	memberPath := "/api/v1/device-groups/" + groupA.ID + "/members"
	requestJSON(t, handler, http.MethodGet, memberPath, "", nil, http.StatusUnauthorized)
	membersBody := requestJSON(t, handler, http.MethodGet, memberPath, login.AccessToken, nil, http.StatusOK)
	var membersResult struct {
		Items []groups.Member `json:"items"`
	}
	decodeResponse(t, membersBody, &membersResult)
	if len(membersResult.Items) != 1 || membersResult.Items[0].NodeID != enrolled.NodeID {
		t.Fatalf("unexpected group members: %+v", membersResult.Items)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v1/device-groups", login.AccessToken, map[string]any{
		"name": "invalid-owner", "kind": "ENTRY", "selection_policy": "weighted_round_robin", "user_group_id": "missing",
	}, http.StatusBadRequest)
	metadataBody := requestJSON(t, handler, http.MethodPost, "/api/v1/device-groups", login.AccessToken, map[string]any{
		"name": "probe-hidden", "kind": "ENTRY", "selection_policy": "weighted_round_robin", "hide_in_probe": true,
	}, http.StatusCreated)
	var metadataResult struct {
		Group groups.DeviceGroup `json:"group"`
	}
	decodeResponse(t, metadataBody, &metadataResult)
	if !metadataResult.Group.HideInProbe || metadataResult.Group.UserGroupID != "" {
		t.Fatalf("device-group metadata not returned: %+v", metadataResult.Group)
	}
	groupListBody := requestJSON(t, handler, http.MethodGet, "/api/v1/device-groups", login.AccessToken, nil, http.StatusOK)
	var groupList struct {
		Items []groups.DeviceGroup `json:"items"`
	}
	decodeResponse(t, groupListBody, &groupList)
	foundHidden := false
	for _, group := range groupList.Items {
		if group.ID == metadataResult.Group.ID {
			foundHidden = group.HideInProbe
		}
	}
	if !foundHidden {
		t.Fatal("device-group list lost probe visibility metadata")
	}
	retirePath := memberPath + "/" + enrolled.NodeID + "/retire"
	requestJSON(t, handler, http.MethodPost, retirePath, "", nil, http.StatusUnauthorized)
	retireBody := requestJSON(t, handler, http.MethodPost, retirePath, login.AccessToken, nil, http.StatusOK)
	var retired groups.RetireMemberResult
	decodeResponse(t, retireBody, &retired)
	if retired.Replayed || retired.Member.RetiredAt == nil {
		t.Fatalf("first retirement not applied: %+v", retired)
	}
	retireReplayBody := requestJSON(t, handler, http.MethodPost, retirePath, login.AccessToken, nil, http.StatusOK)
	decodeResponse(t, retireReplayBody, &retired)
	if !retired.Replayed || len(retired.Assignments) != 0 {
		t.Fatalf("retirement replay changed assignment: %+v", retired)
	}
	requestJSON(t, handler, http.MethodPost, memberPath+"/missing/retire", login.AccessToken, nil, http.StatusNotFound)

	deletePath := "/api/v1/endpoint-pools/" + poolResult.Pool.ID
	deletePool := func(bearer, key string, status int) []byte {
		t.Helper()
		request := httptest.NewRequest(http.MethodDelete, deletePath, nil)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("DELETE %s status = %d, want %d; body=%s", deletePath, response.Code, status, response.Body.String())
		}
		return response.Body.Bytes()
	}
	deletePool("", "delete-1", http.StatusUnauthorized)
	deletePool(login.AccessToken, "", http.StatusBadRequest)
	deleteBody := deletePool(login.AccessToken, "delete-1", http.StatusOK)
	var deleted struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, deleteBody, &deleted)
	if deleted.Replayed {
		t.Fatal("first endpoint delete reported replay")
	}
	decodeResponse(t, deletePool(login.AccessToken, "delete-1", http.StatusOK), &deleted)
	if !deleted.Replayed {
		t.Fatal("endpoint delete retry did not replay")
	}
	deletePool(login.AccessToken, "delete-2", http.StatusNotFound)
	if items, err := store.ListEndpointPools(context.Background()); err != nil || len(items) != 0 {
		t.Fatalf("deleted endpoint remains in inventory: %+v err=%v", items, err)
	}
}

func createGroup(t *testing.T, handler http.Handler, accessToken, name string, kind groups.Kind) groups.DeviceGroup {
	t.Helper()
	body := requestJSON(t, handler, http.MethodPost, "/api/v1/device-groups", accessToken, map[string]any{
		"name":             name,
		"kind":             kind,
		"selection_policy": "weighted_least_connections",
		"description":      "test group",
	}, http.StatusCreated)
	var result struct {
		Group groups.DeviceGroup `json:"group"`
	}
	decodeResponse(t, body, &result)
	return result.Group
}

func addMember(t *testing.T, handler http.Handler, accessToken, groupID, nodeID string) {
	t.Helper()
	requestJSON(t, handler, http.MethodPost, "/api/v1/device-groups/"+groupID+"/members", accessToken, map[string]any{
		"node_id":  nodeID,
		"weight":   100,
		"priority": 0,
	}, http.StatusOK)
}

func publishRevision(t *testing.T, handler http.Handler, accessToken, groupID string, engine agentv1.Engine, config json.RawMessage, idempotencyKey string, expectedStatus int) generations.CreateGroupRevisionResult {
	t.Helper()
	body := requestJSON(t, handler, http.MethodPost, "/api/v1/device-groups/"+groupID+"/generations", accessToken, map[string]any{
		"engine":          engine,
		"config":          config,
		"idempotency_key": idempotencyKey,
	}, expectedStatus)
	if expectedStatus >= 400 {
		return generations.CreateGroupRevisionResult{}
	}
	var result generations.CreateGroupRevisionResult
	decodeResponse(t, body, &result)
	return result
}

func requestJSON(t *testing.T, handler http.Handler, method, path, bearer string, body any, expectedStatus int) []byte {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.StatusCode, expectedStatus, responseBody)
	}
	return responseBody
}

func decodeResponse(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
}
