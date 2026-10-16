package importedconfig

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// NativeProfile is confidential in-process migration material, never an HTTP
// DTO. Config retains the original transport, handshake and routing semantics.
type NativeProfile struct {
	Config      json.RawMessage `json:"-"`
	UUID        string          `json:"-"`
	PrivateKey  string          `json:"-"`
	Flow        string
	ServerNames []string
	ShortIDs    []string
	Destination string
	Port        int
}

// PrepareNative selects exactly one previously validated source listener. It
// retains the first outbound because Xray uses it when no route matches.
// Every output route is scoped to this listener during final compilation.
func PrepareNative(raw []byte, sourceID int64) (NativeProfile, error) {
	selected, _, err := CompileSelectionValidation(raw, []int64{sourceID})
	if err != nil {
		return NativeProfile{}, err
	}
	config, _ := object(selected)
	ins, _ := list[map[string]json.RawMessage](config["inbounds"])
	outs, _ := list[map[string]json.RawMessage](config["outbounds"])
	routing, _ := object(config["routing"])
	routes, _ := list[map[string]json.RawMessage](routing["rules"])
	required := map[string]bool{text(outs[0]["tag"]): true}
	for _, route := range routes {
		required[text(route["outboundTag"])] = true
	}
	kept := []map[string]json.RawMessage{}
	for _, out := range outs {
		if required[text(out["tag"])] {
			kept = append(kept, out)
		}
	}
	config["outbounds"], _ = json.Marshal(kept)
	config["inbounds"], _ = json.Marshal(ins)
	encoded, _ := json.Marshal(config)
	return InspectNative(encoded)
}

// InspectNative revalidates persisted material through the original bounded
// archive validator. This prevents snapshots from injecting unsupported engine
// extensions. The fabricated envelope contains only the selected native line.
func InspectNative(raw []byte) (NativeProfile, error) {
	config, err := object(raw)
	if err != nil {
		return NativeProfile{}, err
	}
	ins, err := list[map[string]json.RawMessage](config["inbounds"])
	if err != nil || len(ins) != 1 {
		return NativeProfile{}, invalid("原生运行材料入站无效")
	}
	in := ins[0]
	port := int(number(in["port"]))
	source := map[string]any{"id": 1, "port": port, "protocol": "vless", "settings": in["settings"], "streamSettings": in["streamSettings"]}
	if sniff, exists := in["sniffing"]; exists {
		source["sniffing"] = sniff
	}
	sourceRaw, _ := json.Marshal(source)
	envelope, _ := json.Marshal(Archive{Schema: "hl-panel.imported-config/v1", SourceID: "native-material", Host: "source.example.invalid", ObservedAt: time.Unix(1, 0).UTC(),
		Inbounds: []SourceInbound{{ID: 1, Port: port, Name: "native", Raw: sourceRaw}}, Config: raw})
	if _, _, err = CompileLoopbackValidation(envelope); err != nil {
		return NativeProfile{}, err
	}
	settings, _ := object(in["settings"])
	clients, _ := list[map[string]json.RawMessage](settings["clients"])
	stream, _ := object(in["streamSettings"])
	reality, _ := object(stream["realitySettings"])
	names, _ := list[string](reality["serverNames"])
	shorts, _ := list[string](reality["shortIds"])
	dest := text(reality["dest"])
	if dest == "" {
		dest = text(reality["target"])
	}
	return NativeProfile{Config: append(json.RawMessage(nil), raw...), UUID: text(clients[0]["id"]), PrivateKey: text(reality["privateKey"]),
		Flow: text(clients[0]["flow"]), ServerNames: names, ShortIDs: shorts, Destination: dest, Port: port}, nil
}

type NativeOptions struct {
	RuleID, CustomerID, EntryGroupID string
	UUID, PrivateKey                 string
	ListenAddress                    string
	ListenPort                       int
	ProbeUUID                        string
	EchoPort                         int
}

// CompileNativeFragment creates one independently scoped HL engine fragment.
// Tags are replaced for accounting, not customer credentials. Original route
// order survives; global blocks gain only this rule's inbound selector so they
// cannot affect another customer's fragment after the agent merges bundles.
// The probe identity, when requested, can reach only a fixed loopback echo.
func CompileNativeFragment(raw []byte, opts NativeOptions) ([]byte, error) {
	profile, err := InspectNative(raw)
	if err != nil {
		return nil, err
	}
	if !identifier(opts.RuleID) || !identifier(opts.CustomerID) || !identifier(opts.EntryGroupID) || opts.UUID != profile.UUID || opts.PrivateKey != profile.PrivateKey ||
		net.ParseIP(opts.ListenAddress) == nil || opts.ListenPort < 1 || opts.ListenPort > 65535 {
		return nil, invalid("原生运行材料关联无效")
	}
	if (opts.ProbeUUID == "") != (opts.EchoPort == 0) || opts.ProbeUUID != "" && (!validUUID(opts.ProbeUUID) || strings.EqualFold(opts.ProbeUUID, opts.UUID) || opts.EchoPort < 1 || opts.EchoPort > 65535) {
		return nil, invalid("原生运行材料探测无效")
	}
	config, _ := object(raw)
	ins, _ := list[map[string]json.RawMessage](config["inbounds"])
	outs, _ := list[map[string]json.RawMessage](config["outbounds"])
	routing, _ := object(config["routing"])
	routes, _ := list[map[string]json.RawMessage](routing["rules"])
	metadata, _ := json.Marshal(map[string]string{"rule_id": opts.RuleID, "customer_id": opts.CustomerID, "entry_group_id": opts.EntryGroupID, "exit_group_id": "", "protocol": "tcp"})
	inTag := agentv1.NativeRealityStatsTagPrefix + opts.RuleID + "--" + base64.RawURLEncoding.EncodeToString(metadata)
	tags := map[string]string{}
	for _, out := range outs {
		old := text(out["tag"])
		sum := sha256.Sum256([]byte(opts.RuleID + ":" + old))
		tags[old] = "kl-" + opts.RuleID + "-" + hex.EncodeToString(sum[:8])
		out["tag"], _ = json.Marshal(tags[old])
	}
	fallback := text(outs[0]["tag"])
	for _, route := range routes {
		out, exists := tags[text(route["outboundTag"])]
		if !exists {
			return nil, invalid("原生运行材料路由无效")
		}
		route["outboundTag"], _ = json.Marshal(out)
		route["inboundTag"], _ = json.Marshal([]string{inTag})
	}
	routes = append(routes, map[string]json.RawMessage{"type": json.RawMessage(`"field"`), "inboundTag": mustJSON([]string{inTag}), "outboundTag": mustJSON(fallback)})
	in := ins[0]
	in["tag"], _ = json.Marshal(inTag)
	in["listen"], _ = json.Marshal(opts.ListenAddress)
	in["port"], _ = json.Marshal(opts.ListenPort)
	if opts.ProbeUUID != "" {
		settings, _ := object(in["settings"])
		clients, _ := list[map[string]json.RawMessage](settings["clients"])
		probeEmail := "hl-probe-" + opts.RuleID
		clients = append(clients, map[string]json.RawMessage{"id": mustJSON(opts.ProbeUUID), "flow": mustJSON(profile.Flow), "email": mustJSON(probeEmail)})
		settings["clients"], _ = json.Marshal(clients)
		in["settings"], _ = json.Marshal(settings)
		probeTag := "probe-echo-" + opts.RuleID
		probeOut := map[string]any{"tag": probeTag, "protocol": "freedom", "settings": map[string]any{
			"redirect":   net.JoinHostPort("127.0.0.1", fmt.Sprint(opts.EchoPort)),
			"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "port": opts.EchoPort, "ip": []string{"127.0.0.1/32", "::1/128"}}}}}
		var encodedOut map[string]json.RawMessage
		_ = json.Unmarshal(mustJSON(probeOut), &encodedOut)
		outs = append(outs, encodedOut)
		probeRoute := map[string]json.RawMessage{"type": mustJSON("field"), "inboundTag": mustJSON([]string{inTag}), "user": mustJSON([]string{probeEmail}), "outboundTag": mustJSON(probeTag)}
		routes = append([]map[string]json.RawMessage{probeRoute}, routes...)
	}
	config["inbounds"], _ = json.Marshal(ins)
	config["outbounds"], _ = json.Marshal(outs)
	routing["rules"], _ = json.Marshal(routes)
	config["routing"], _ = json.Marshal(routing)
	// Logging is owned by the agent's merged configuration.
	delete(config, "log")
	return json.Marshal(config)
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
