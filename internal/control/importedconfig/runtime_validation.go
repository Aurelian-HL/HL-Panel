package importedconfig

import (
	"encoding/json"
)

// CompileLoopbackValidation preserves the imported native business configuration
// for an engine parser check. It never creates a rule or an identity. Its output
// is confidential and is NOT an activation bundle: real SOCKS destinations and
// Reality decoys remain present, so source archives must ONLY use xray run -test.
// Real handshake tests use synthetic, entirely loopback archives instead.
//
// Management listeners, API routing, metrics and log files are excluded. Business
// UUIDs, explicit empty flow, stream/sniffing settings, outbound destinations and
// the order of business routing rules are preserved without normal-rule defaults.
func CompileLoopbackValidation(raw []byte) ([]byte, Plan, error) {
	plan, err := Validate(raw)
	if err != nil {
		return nil, Plan{}, err
	}
	var archive Archive
	if err = decode(raw, &archive); err != nil {
		return nil, Plan{}, err
	}
	config, err := object(archive.Config)
	if err != nil {
		return nil, Plan{}, err
	}
	known := map[string]bool{"log": true, "api": true, "metrics": true, "inbounds": true, "outbounds": true, "routing": true, "policy": true, "stats": true}
	for _, k := range []string{"dns", "fakedns", "reverse", "transport", "observatory", "burstObservatory"} {
		known[k] = true
		if value, exists := config[k]; exists && !emptySection(value) {
			return nil, Plan{}, invalid("包含尚未验证的引擎扩展")
		}
	}
	for k := range config {
		if !known[k] {
			return nil, Plan{}, invalid("包含尚未验证的引擎扩展")
		}
	}
	inbounds, _ := list[map[string]json.RawMessage](config["inbounds"])
	businessTags, excludedTags := map[string]bool{}, map[string]bool{}
	selected := make([]map[string]json.RawMessage, 0, len(plan.Lines))
	allTags := map[string]bool{}
	for _, inbound := range inbounds {
		tag := text(inbound["tag"])
		if tag == "" || allTags[tag] {
			return nil, Plan{}, invalid("入站标记冲突")
		}
		allTags[tag] = true
		if text(inbound["protocol"]) != "vless" {
			excludedTags[tag] = true
			continue
		}
		businessTags[tag] = true
		inbound["listen"] = json.RawMessage(`"127.0.0.1"`)
		selected = append(selected, inbound)
	}
	outbounds, _ := list[map[string]json.RawMessage](config["outbounds"])
	outTags := map[string]string{}
	for _, outbound := range outbounds {
		for k := range outbound {
			if k != "tag" && k != "protocol" && k != "settings" {
				return nil, Plan{}, invalid("包含尚未验证的出口扩展")
			}
		}
		protocol := text(outbound["protocol"])
		if protocol == "blackhole" {
			if settings, exists := outbound["settings"]; exists && !emptySection(settings) {
				return nil, Plan{}, invalid("包含尚未验证的阻断出口")
			}
		} else if err := validateOutbound(outbound); err != nil {
			return nil, Plan{}, err
		}
		outTags[text(outbound["tag"])] = protocol
	}
	routing, _ := object(config["routing"])
	for k := range routing {
		if k != "rules" && k != "domainStrategy" && !(k == "balancers" && emptySection(routing[k])) {
			return nil, Plan{}, invalid("包含尚未验证的路由扩展")
		}
	}
	rules, _ := list[map[string]json.RawMessage](routing["rules"])
	selectedRules := make([]map[string]json.RawMessage, 0, len(rules))
	for _, rule := range rules {
		if tags, exists := rule["inboundTag"]; exists {
			values, err := list[string](tags)
			if err != nil || len(values) == 0 || len(rule) != 3 || text(rule["type"]) != "field" {
				return nil, Plan{}, invalid("入站路由暂不支持")
			}
			included, excluded := false, false
			for _, tag := range values {
				if businessTags[tag] {
					included = true
				} else if excludedTags[tag] {
					excluded = true
				} else {
					return nil, Plan{}, invalid("路由引用未知入站")
				}
			}
			// A mixed selector must not silently change its meaning.
			if included && excluded {
				return nil, Plan{}, invalid("业务路由与管理路由混用")
			}
			if excluded {
				continue
			}
			if outTags[text(rule["outboundTag"])] == "" {
				return nil, Plan{}, invalid("路由引用未知出口")
			}
		} else {
			// Preserve only the observed, understood global block rules. New
			// selectors must be reviewed rather than guessed or discarded.
			if text(rule["type"]) != "field" || len(rule) != 3 || outTags[text(rule["outboundTag"])] != "blackhole" ||
				(!equalJSON(rule["ip"], json.RawMessage(`["geoip:private"]`)) && !equalJSON(rule["protocol"], json.RawMessage(`["bittorrent"]`))) {
				return nil, Plan{}, invalid("全局路由暂不支持")
			}
		}
		selectedRules = append(selectedRules, rule)
	}
	routing["rules"], _ = json.Marshal(selectedRules)
	result := map[string]any{
		"log":      map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": selected, "outbounds": outbounds, "routing": routing,
	}
	for _, k := range []string{"policy", "stats"} {
		if v, exists := config[k]; exists {
			result[k] = v
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, Plan{}, invalid("引擎配置编码失败")
	}
	return encoded, plan, nil
}

func emptySection(raw json.RawMessage) bool {
	var value any
	if decode(raw, &value) != nil {
		return false
	}
	switch v := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(v) == 0
	case []any:
		return len(v) == 0
	default:
		return false
	}
}
