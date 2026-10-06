package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestEnrollmentRecoveryIsBoundToPrivateAttemptAndSurvivesSnapshot(t *testing.T) {
	f := newBusinessFixture(t)
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token,
		map[string]any{"name": "recoverable", "group_id": f.entry.ID, "expires_in_seconds": 900}, http.StatusCreated), &issued)
	input := agentv1.EnrollmentRequest{EnrollmentToken: issued.Token, EnrollmentSecret: strings.Repeat("a", 64),
		Hostname: "recovery.example.test", Platform: "linux", Architecture: "amd64", AgentVersion: "test"}
	var first agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", input, http.StatusCreated), &first)
	audits := len(f.store.AuditEvents())
	var replay agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", input, http.StatusCreated), &replay)
	if replay != first || len(f.store.AuditEvents()) != audits {
		t.Fatal("recovery changed identity or duplicated audit")
	}
	for _, secret := range []string{"", strings.Repeat("b", 64)} {
		other := input
		other.EnrollmentSecret = secret
		requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", other, http.StatusUnauthorized)
	}
	other := input
	other.Hostname = "other.example.test"
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", other, http.StatusUnauthorized)
	other = input
	other.EnrollmentSecret = "too-short"
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", other, http.StatusBadRequest)
	for _, route := range []string{"/api/v1/nodes", "/api/v1/device-groups/" + f.entry.ID + "/members"} {
		body := requestJSON(t, f.handler, http.MethodGet, route, f.token, nil, http.StatusOK)
		if bytes.Contains(body, []byte(first.NodeCredential)) || bytes.Contains(body, []byte(input.EnrollmentSecret)) || bytes.Contains(body, []byte(issued.Token)) {
			t.Fatal("list API exposed enrollment recovery material")
		}
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(input.EnrollmentSecret)) || bytes.Contains(raw, []byte(first.NodeCredential)) {
		t.Fatal("snapshot stored plaintext recovery material")
	}
	restored, err := memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	service := enrollment.NewService(restored, func() time.Time { return now }, time.Hour)
	request := enrollment.EnrollInput{RawToken: input.EnrollmentToken, EnrollmentSecret: input.EnrollmentSecret,
		Hostname: input.Hostname, Platform: input.Platform, Architecture: input.Architecture, AgentVersion: input.AgentVersion}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Enroll(context.Background(), request)
			if err != nil || result.NodeID != first.NodeID || result.NodeCredential != first.NodeCredential {
				t.Error("concurrent durable recovery failed")
			}
		}()
	}
	wg.Wait()
	items, err := restored.ListNodes(context.Background())
	if err != nil || len(items) != 1 || len(restored.AuditEvents()) != audits {
		t.Fatal("recovery duplicated persisted state")
	}
	now = now.Add(25 * time.Hour)
	if _, err := service.Enroll(context.Background(), request); !errors.Is(err, faults.ErrAlreadyUsed) {
		t.Fatal("recovery exceeded bounded window")
	}
}
