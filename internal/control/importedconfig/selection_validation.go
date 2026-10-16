package importedconfig

import "encoding/json"

// CompileSelectionValidation checks the native configuration for an explicit
// selection of source lines. The caller must provide every selected source ID;
// nil and empty selections are rejected to prevent an accidental enable-all.
// This is still a loopback parser/isolated-test configuration, not an activation
// contract. No listeners are started and no persistence is performed here.
//
// Native UUIDs, flow, sniffing, TCP, Reality and outbound settings are preserved.
// Global block rules and their original ordering remain intact. Inbound-specific
// selectors are intersected with the selected set, so excluded lines cannot gain
// a listener or an executable route through another selected line.
func CompileSelectionValidation(raw []byte, sourceIDs []int64) ([]byte, Plan, error) {
	if len(sourceIDs) == 0 || len(sourceIDs) > 500 {
		return nil, Plan{}, invalid("必须明确选择待验证线路")
	}
	selected := make(map[int64]bool, len(sourceIDs))
	for _, id := range sourceIDs {
		if id <= 0 || selected[id] {
			return nil, Plan{}, invalid("选择的线路无效或重复")
		}
		selected[id] = true
	}
	compiled, full, err := CompileLoopbackValidation(raw)
	if err != nil {
		return nil, Plan{}, err
	}
	plan := full
	plan.Lines = []Line{}
	plan.Unassigned = 0
	ports := make(map[int]bool, len(sourceIDs))
	for _, line := range full.Lines {
		if selected[line.SourceID] {
			ports[line.Port] = true
			plan.Lines = append(plan.Lines, line)
			if line.OwnerID == "" {
				plan.Unassigned++
			}
		}
	}
	if len(plan.Lines) != len(selected) {
		return nil, Plan{}, invalid("选择的线路不存在")
	}
	// SubscriptionCount remains the archive's subscription count. It is not a
	// count of subscriptions belonging exclusively to this selection.
	config, err := object(compiled)
	if err != nil {
		return nil, Plan{}, err
	}
	inbounds, _ := list[map[string]json.RawMessage](config["inbounds"])
	kept := []map[string]json.RawMessage{}
	tags := map[string]bool{}
	for _, inbound := range inbounds {
		if ports[int(number(inbound["port"]))] {
			kept = append(kept, inbound)
			tags[text(inbound["tag"])] = true
		}
	}
	config["inbounds"], _ = json.Marshal(kept)
	routing, _ := object(config["routing"])
	rules, _ := list[map[string]json.RawMessage](routing["rules"])
	keptRules := []map[string]json.RawMessage{}
	for _, rule := range rules {
		selector, specific := rule["inboundTag"]
		if specific {
			old, _ := list[string](selector)
			next := []string{}
			for _, tag := range old {
				if tags[tag] {
					next = append(next, tag)
				}
			}
			if len(next) == 0 {
				continue
			}
			rule["inboundTag"], _ = json.Marshal(next)
		}
		keptRules = append(keptRules, rule)
	}
	routing["rules"], _ = json.Marshal(keptRules)
	config["routing"], _ = json.Marshal(routing)
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, Plan{}, invalid("引擎配置编码失败")
	}
	return encoded, plan, nil
}
