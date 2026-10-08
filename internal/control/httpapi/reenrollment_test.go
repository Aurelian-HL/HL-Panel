package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/securetoken"
)

func TestReenrollmentRequiresPrivateIdentityAndUsesLatestGroupDurably(t *testing.T) {
	f := newBusinessFixture(t)
	issue := func(group string) enrollment.IssueResult {
		t.Helper()
		var token enrollment.IssueResult
		decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token,
			map[string]any{"name": "reinstalled", "group_id": group, "expires_in_seconds": 900}, http.StatusCreated), &token)
		return token
	}
	first := issue(f.entry.ID)
	input := agentv1.EnrollmentRequest{EnrollmentToken: first.Token, Hostname: "node.example.test", DialHost: "192.0.2.10", Platform: "linux", Architecture: "amd64", AgentVersion: "v0.1.42"}
	var identity agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", input, http.StatusCreated), &identity)
	next := issue(f.exit.ID)
	input.EnrollmentToken = next.Token
	route := "/api/v1/agent/reenroll"
	requestJSON(t, f.handler, http.MethodPost, route, "", input, http.StatusUnauthorized)
	requestJSON(t, f.handler, http.MethodPost, route, f.token, input, http.StatusUnauthorized)
	body := requestJSON(t, f.handler, http.MethodPost, route, identity.NodeCredential, input, http.StatusOK)
	if !bytes.Contains(body, []byte(identity.NodeID)) || bytes.Contains(body, []byte(identity.NodeCredential)) || bytes.Contains(body, []byte(next.Token)) {
		t.Fatal("reenrollment returned the wrong identity or leaked a secret")
	}
	old, _ := f.store.ListGroupMembers(context.Background(), f.entry.ID)
	current, _ := f.store.ListGroupMembers(context.Background(), f.exit.ID)
	if len(old) != 1 || old[0].RetiredAt == nil || len(current) != 1 || current[0].NodeID != identity.NodeID || current[0].RetiredAt != nil {
		t.Fatal("latest group did not replace old membership")
	}
	auditCount := len(f.store.AuditEvents())
	requestJSON(t, f.handler, http.MethodPost, route, identity.NodeCredential, input, http.StatusOK)
	if len(f.store.AuditEvents()) != auditCount {
		t.Fatal("repeat duplicated audit")
	}
	input.EnrollmentToken = first.Token
	requestJSON(t, f.handler, http.MethodPost, route, identity.NodeCredential, input, http.StatusUnauthorized)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", input, http.StatusUnauthorized)
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	node, err := restored.NodeByCredentialHash(context.Background(), securetoken.Hash(identity.NodeCredential))
	if err != nil {
		t.Fatal("persistent credential changed")
	}
	service := enrollment.NewService(restored, time.Now, time.Hour)
	input.EnrollmentToken = next.Token
	replay := enrollment.EnrollInput{RawToken: input.EnrollmentToken, Hostname: input.Hostname, DialHost: input.DialHost, Platform: input.Platform, Architecture: input.Architecture, AgentVersion: input.AgentVersion}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Reenroll(context.Background(), node, replay)
			if err != nil || result.ID != identity.NodeID {
				t.Error("durable concurrent replay failed")
			}
		}()
	}
	wg.Wait()
	list, _ := restored.ListNodes(context.Background())
	if len(list) != 1 || len(restored.AuditEvents()) != auditCount {
		t.Fatal("durable replay duplicated node or audit")
	}
	replay.RawToken = first.Token
	if _, err := service.Reenroll(context.Background(), node, replay); !errors.Is(err, faults.ErrAlreadyUsed) {
		t.Fatal("superseded token was accepted")
	}
	for _, rejected := range []string{"expired", "revoked"} {
		fresh, err := service.IssueForGroup(context.Background(), "test-admin", rejected, f.entry.ID, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		clock := time.Now
		expected := faults.ErrConflict
		if rejected == "expired" {
			clock = func() time.Time { return time.Now().Add(time.Minute) }
			expected = faults.ErrExpired
		} else {
			if _, _, err := service.Revoke(context.Background(), "test-admin", fresh.ID, "revoke-reenroll"); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := restored.EncodeSnapshot()
		replay.RawToken = fresh.Token
		s := enrollment.NewService(restored, clock, time.Hour)
		if _, err := s.Reenroll(context.Background(), node, replay); !errors.Is(err, expected) {
			t.Fatalf("%s token: %v", rejected, err)
		}
		after, _ := restored.EncodeSnapshot()
		if !bytes.Equal(before, after) {
			t.Fatal("rejected token changed durable state")
		}
	}
}
