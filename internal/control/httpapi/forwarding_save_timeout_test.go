package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
)

type blockedEndpointLookup struct{ *memoryrepo.Store }

func (s blockedEndpointLookup) ListEndpointPools(ctx context.Context) ([]endpoints.EndpointPool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCommittedRuleReturnsSuccessWhenSetupLookupTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	store := memoryrepo.New(auth.Administrator{ID: "admin", Username: "admin"})
	group, err := groups.NewService(store, time.Now).Create(ctx, "admin", "isolated", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "group")
	if err != nil {
		t.Fatal(err)
	}
	networks := groupconfig.NewService(store, time.Now)
	if _, _, err := networks.Update(ctx, "admin", group.ID, groupconfig.Request{ConnectHost: "127.0.0.1", PortStart: 20000, PortEnd: 20010, AllowDirect: true, TrafficMultiplier: 1}, "network"); err != nil {
		t.Fatal(err)
	}
	identities, err := vlessidentity.NewService(store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	forward := forwarding.NewService(store, time.Now, forwarding.WithRealityDefaults(forwarding.RealityDefaults{ServerName: "example.test", Destination: "example.test:443"}))
	api := &API{forwarding: forward, vlessSetup: vlesssetup.NewService(networks, endpoints.NewService(blockedEndpointLookup{store}, time.Now), identities), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	input := forwarding.Request{Name: "bounded save", EntryGroupID: group.ID, EgressMode: forwarding.EgressDirect, IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP, ListenPort: 20000, SelectionPolicy: forwarding.SelectionRoundRobin, VLESSFlow: "xtls-rprx-vision", VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "127.0.0.1", VLESSSOCKS5Port: 1080, VLESSSOCKS5Username: "isolated", VLESSSOCKS5Password: "disposable"}
	raw, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/forwarding-rules", bytes.NewReader(raw)).WithContext(ctx)
	request.Header.Set("Idempotency-Key", "bounded-save")
	response := httptest.NewRecorder()
	started := time.Now()
	api.saveForwardingRule(response, request, auth.Session{AdminID: "admin"})
	if response.Code != http.StatusCreated || time.Since(started) > 5*time.Second {
		t.Fatalf("committed rule reported failure or waited for request timeout: status=%d duration=%s", response.Code, time.Since(started))
	}
	var result struct {
		Rule forwarding.Rule `json:"rule"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Rule.ID == "" || result.Rule.Status != forwarding.StatusPendingActivation {
		t.Fatal("saved pending rule missing from response")
	}
	if _, err := forward.GetForAdministrator(ctx, "admin", result.Rule.ID); err != nil {
		t.Fatal("successful save was not committed")
	}
}
