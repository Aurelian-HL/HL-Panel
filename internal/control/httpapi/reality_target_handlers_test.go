package httpapi_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/targetprobe"
)

func TestRealityTargetsRequireAdministratorAndReturnPairedCandidates(t *testing.T) {
	f := newBusinessFixture(t, forwarding.WithRealityTargets(forwarding.DefaultRealityTargets()))
	path := "/api/v1/forwarding-reality-targets"
	requestJSON(t, f.handler, http.MethodGet, path, "", nil, http.StatusUnauthorized)
	var response struct {
		Items []forwarding.RealityDefaults `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, path, "", nil, http.StatusOK), &response)
	if !reflect.DeepEqual(response.Items, forwarding.DefaultRealityTargets()) {
		t.Fatal("public candidates do not match the configured pool")
	}
}

func TestRealityProbeRequiresAdministratorAndRejectsPrivateTargets(t *testing.T) {
	f := newBusinessFixture(t)
	path := "/api/v1/forwarding-reality-target-probes"
	requestJSON(t, f.handler, http.MethodPost, path, "", targetprobe.RealityRequest{ServerName: "127.0.0.1"}, http.StatusUnauthorized)
	businessRequest(t, f, http.MethodPost, path, "", targetprobe.RealityRequest{ServerName: "https://example.com"}, http.StatusBadRequest)
	var response struct {
		Probe targetprobe.RealityResult `json:"probe"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, path, "", targetprobe.RealityRequest{ServerName: "127.0.0.1"}, http.StatusOK), &response)
	if response.Probe.Compatible || response.Probe.ErrorCode != "non_public_target" || response.Probe.Source != "control_plane" {
		t.Fatalf("unsafe probe response: %+v", response)
	}
}
