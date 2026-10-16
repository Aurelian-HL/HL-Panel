package gatewaymembership

import (
	"bytes"
	"encoding/json"
	"github.com/hongle/hl-panel/internal/gateway/membership"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBatchHandlerRequiresEveryPoolCredentialAndReadsOnce(t *testing.T) {
	a, b := stateWithVerifiedEvidence(), stateWithVerifiedEvidence()
	b.Pool.ID = "pool-2"
	b.Rule.ID = "rule-2"
	b.Pool.RuleID = "rule-2"
	// Expired proof must remain expired in the batch response.
	proof := a.ProtocolHealth["node-1"]
	proof.LeaseExpiresAt = time.Now().Add(-time.Second)
	a.ProtocolHealth["node-1"] = proof
	repo := &batchStates{states: []State{a, b}}
	tokens := map[string]string{"pool-1": strings.Repeat("a", 32), "pool-2": strings.Repeat("b", 32)}
	h, err := NewHandler(NewService(repo), tokens)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	for _, bad := range []string{`{}`, `{"pool-1":"wrong"}`, `{"pool-2":"` + tokens["pool-1"] + `"}`, `{"unknown":"` + tokens["pool-1"] + `"}`, `{"pool-1":"` + tokens["pool-1"] + `"} {}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/gateway/membership", strings.NewReader(bad)))
		if w.Code == 200 || repo.calls != 0 {
			t.Fatalf("bad credential/body was accepted: %d", w.Code)
		}
	}
	raw, _ := json.Marshal(tokens)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/gateway/membership", bytes.NewReader(raw)))
	var snapshots map[string]membership.Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshots) != nil || len(snapshots) != 2 || repo.calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("batch failed: status=%d calls=%d", w.Code, repo.calls)
	}
	if snapshots["pool-1"].Endpoints[0].HealthLeaseExpiresAt.After(time.Now()) {
		t.Fatal("batch extended expired lease")
	}
}
