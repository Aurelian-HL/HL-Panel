package vlessconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// These hashes come from the v0.1.115 compiler. Only content-versioned outbound
// tags and their route references may differ; handshake and listener settings
// must still satisfy the historical byte-for-byte regression contract.
func TestLegacyTCPConfigHashesUnchanged(t *testing.T) {
	cases := []struct {
		name, flow, mode, hash string
		probe                  bool
	}{
		{"vision_direct", "xtls-rprx-vision", "direct", "da069dc16cdfe931ac38020e2066d2131c049d2feacc944b1d34a4d763c777c7", false},
		{"vision_direct_probe", "xtls-rprx-vision", "direct", "9e015efef71240a0f76021d84f50a4133c18c3ffd95dbb78dd321549832e3da9", true},
		{"vision_socks5", "xtls-rprx-vision", "socks5", "0e7a82bd20d835b8521296c20bf14a955129af651d37cd4de459e01259d07601", false},
		{"vision_socks5_probe", "xtls-rprx-vision", "socks5", "b94118861443cdb8dacc8aeb6553f471882f18bc08543b1349dcbc46e553d2ce", true},
		{"empty_direct", "", "direct", "a4efc41544a420b6fe646ad71675da1880fe7640b18d9a6eb83bbcb8e05b379a", false},
		{"empty_direct_probe", "", "direct", "5743a96451fc495a915f7638b3e19f33a7b5af4e7fe4f2c863d332404765843d", true},
		{"empty_socks5", "", "socks5", "ca548d84fec223cf42e450dcd953b0c64f874465d2d9f9db7c0c67e755f7b0cb", false},
		{"empty_socks5_probe", "", "socks5", "c07ea691a1fd47dd7b18eae701539ff90432c81f77927e4437f7afff313ec34c", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := validRule()
			rule.VLESSFlow = tc.flow
			rule.VLESSFlowExplicit = true
			profile := validProfile()
			profile.Flow = tc.flow
			listener := Listener{Address: "0.0.0.0", Port: 24443}
			probe := ProbeIdentity{UUID: "615db532-728a-4a34-8353-873b34d12de2", EchoPort: 31998}
			upstream := SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080, Username: "landing-user", Password: "landing-password"}
			if tc.mode == "socks5" {
				rule.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
				rule.Targets = nil
			}
			var raw []byte
			var err error
			switch {
			case tc.mode == "socks5" && tc.probe:
				raw, err = CompileDirectWithProbeAndSOCKS5(rule, validIdentity(), profile, listener, probe, upstream)
			case tc.mode == "socks5":
				raw, err = CompileDirectWithSOCKS5(rule, validIdentity(), profile, listener, upstream)
			case tc.probe:
				raw, err = CompileDirectWithProbe(rule, validIdentity(), profile, listener, probe)
			default:
				raw, err = CompileDirect(rule, validIdentity(), profile, listener)
			}
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(withoutOutboundVersions(t, raw))
			if got := hex.EncodeToString(sum[:]); got != tc.hash {
				t.Fatalf("v0.1.115 TCP config hash changed: got %s, want %s", got, tc.hash)
			}
		})
	}
}

func withoutOutboundVersions(t *testing.T, raw []byte) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`-v[0-9a-f]{32}(--|$)`)
	tags := map[string]string{}
	for _, value := range doc["outbounds"].([]any) {
		out := value.(map[string]any)
		tag := out["tag"].(string)
		matches := pattern.FindAllStringIndex(tag, -1)
		if len(matches) != 1 {
			t.Fatal("expected exactly one outbound content version")
		}
		legacy := pattern.ReplaceAllString(tag, "$1")
		tags[tag] = legacy
		out["tag"] = legacy
	}
	for _, value := range doc["routing"].(map[string]any)["rules"].([]any) {
		route := value.(map[string]any)
		legacy, ok := tags[route["outboundTag"].(string)]
		if !ok {
			t.Fatal("route did not reference a content-versioned outbound")
		}
		route["outboundTag"] = legacy
	}
	result, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
