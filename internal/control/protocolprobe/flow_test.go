package protocolprobe

import (
	"encoding/json"
	"testing"
)

func TestClientConfigExplicitEmptyFlowAndUnsupportedFlow(t *testing.T) {
	spec := validSpec()
	spec.FlowExplicit = true
	data, err := clientConfig(spec, 23001)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds []struct {
			Settings struct {
				Vnext []struct{ Users []struct{ Flow string } }
			}
		}
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Outbounds[0].Settings.Vnext[0].Users[0].Flow != "" {
		t.Fatal("probe forced Vision")
	}
	spec.Flow = "unsupported"
	if _, err := clientConfig(spec, 23001); err == nil {
		t.Fatal("unsupported flow accepted")
	}
}

func TestProbeCacheSeparatesFlow(t *testing.T) {
	a := validSpec()
	b := a
	b.FlowExplicit = true
	if probeCacheKey(a, "xray", fakeFileInfo{}) == probeCacheKey(b, "xray", fakeFileInfo{}) {
		t.Fatal("flow change reused protocol health")
	}
}
