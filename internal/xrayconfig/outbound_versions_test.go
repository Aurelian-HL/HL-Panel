package xrayconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const leafFixture = `{"inbounds":[{"tag":"client","settings":{"id":"unchanged"}}],"outbounds":[{"tag":"landing--metadata","protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":1080,"users":[{"user":"test","pass":"one"}]}]}},{"tag":"probe","protocol":"freedom","settings":{"redirect":"127.0.0.1:31998"}}],"routing":{"rules":[{"inboundTag":["client"],"user":["probe"],"outboundTag":"probe"},{"inboundTag":["client"],"outboundTag":"landing--metadata"}]},"policy":{"exactNumber":9007199254740993}}`

func readVersioned(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestOutboundVersionsPreserveIdentityRoutingAndNumbers(t *testing.T) {
	before := readVersioned(t, []byte(leafFixture))
	raw, err := VersionLeafOutbounds([]byte(leafFixture))
	if err != nil {
		t.Fatal(err)
	}
	after := readVersioned(t, raw)
	if !reflect.DeepEqual(before["inbounds"], after["inbounds"]) || !reflect.DeepEqual(before["policy"], after["policy"]) {
		t.Fatal("inbound identity or precise policy number changed")
	}
	if !bytes.Contains(raw, []byte("9007199254740993")) {
		t.Fatal("integer precision lost")
	}
	outs := after["outbounds"].([]any)
	routes := after["routing"].(map[string]any)["rules"].([]any)
	landing := outs[0].(map[string]any)["tag"].(string)
	if !strings.HasPrefix(landing, "landing-v") || !strings.HasSuffix(landing, "--metadata") || len(landing) != len("landing--metadata")+34 {
		t.Fatal("version or accounting metadata malformed")
	}
	if routes[0].(map[string]any)["outboundTag"] != outs[1].(map[string]any)["tag"] || routes[1].(map[string]any)["outboundTag"] != landing {
		t.Fatal("route order or destination changed")
	}
	for i, v := range before["outbounds"].([]any) {
		out := outs[i].(map[string]any)
		out["tag"] = v.(map[string]any)["tag"]
		if !reflect.DeepEqual(v, out) {
			t.Fatal("outbound payload changed")
		}
	}
}

func TestOutboundVersionsChangeOnlyAffectedEgress(t *testing.T) {
	a, err := VersionLeafOutbounds([]byte(leafFixture))
	if err != nil {
		t.Fatal(err)
	}
	b, err := VersionLeafOutbounds([]byte(strings.Replace(leafFixture, `"pass":"one"`, `"pass":"two"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	old, next := readVersioned(t, a), readVersioned(t, b)
	oldOut, nextOut := old["outbounds"].([]any), next["outbounds"].([]any)
	if oldOut[0].(map[string]any)["tag"] == nextOut[0].(map[string]any)["tag"] || !reflect.DeepEqual(oldOut[1], nextOut[1]) || !reflect.DeepEqual(old["inbounds"], next["inbounds"]) {
		t.Fatal("egress edit did not isolate content version from stable handlers")
	}
	// Canonical ordering and whitespace cannot introduce spurious generations.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(leafFixture), "", "  "); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(readVersioned(t, pretty.Bytes()))
	c, err := VersionLeafOutbounds(canonical)
	if err != nil || !bytes.Equal(a, c) {
		t.Fatal("formatting changed content versions")
	}
}

func TestOutboundVersionsRejectUnboundedConfigurations(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"stream settings": func(d map[string]any) {
			d["outbounds"].([]any)[0].(map[string]any)["streamSettings"] = map[string]any{}
		},
		"proxy chain": func(d map[string]any) {
			d["outbounds"].([]any)[0].(map[string]any)["proxySettings"] = map[string]any{"tag": "probe"}
		},
		"dialer proxy":     func(d map[string]any) { d["outbounds"].([]any)[0].(map[string]any)["dialerProxy"] = "probe" },
		"unknown protocol": func(d map[string]any) { d["outbounds"].([]any)[0].(map[string]any)["protocol"] = "vless" },
		"balancer": func(d map[string]any) {
			d["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool"}}
		},
		"balancer route": func(d map[string]any) {
			d["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)["balancerTag"] = "pool"
		},
		"unknown target": func(d map[string]any) {
			d["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)["outboundTag"] = "unknown"
		},
		"duplicate tag": func(d map[string]any) {
			outs := d["outbounds"].([]any)
			outs[1].(map[string]any)["tag"] = outs[0].(map[string]any)["tag"]
		},
		"empty tag":    func(d map[string]any) { d["outbounds"].([]any)[0].(map[string]any)["tag"] = "" },
		"empty routes": func(d map[string]any) { d["routing"].(map[string]any)["rules"] = []any{} },
	} {
		t.Run(name, func(t *testing.T) {
			doc := readVersioned(t, []byte(leafFixture))
			mutate(doc)
			raw, _ := json.Marshal(doc)
			if _, err := VersionLeafOutbounds(raw); !errors.Is(err, ErrOutboundVersioning) {
				t.Fatal("unsafe versioning accepted")
			}
		})
	}
	for _, raw := range []string{"null", "{}", leafFixture + " {}", leafFixture + " trailing"} {
		if _, err := VersionLeafOutbounds([]byte(raw)); !errors.Is(err, ErrOutboundVersioning) {
			t.Fatal("invalid document accepted")
		}
	}
}
