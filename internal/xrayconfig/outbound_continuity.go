package xrayconfig

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// PreserveUnchangedLeafTags keeps the installed name of a leaf handler when
// only its generated content-version suffix differs. Introducing versioned
// tags must not turn the first unrelated rule deletion into a replacement of
// every outbound on an existing node. A real settings or accounting change
// retains the new tag and follows the normal make-before-break transaction.
func PreserveUnchangedLeafTags(previous, candidate []byte) ([]byte, error) {
	var before, after map[string]json.RawMessage
	if json.Unmarshal(previous, &before) != nil || json.Unmarshal(candidate, &after) != nil {
		return nil, ErrOutboundVersioning
	}
	var old, next []map[string]json.RawMessage
	if json.Unmarshal(before["outbounds"], &old) != nil || json.Unmarshal(after["outbounds"], &next) != nil {
		return nil, ErrOutboundVersioning
	}
	// Xray permits unnamed default outbounds. They have no stable identity
	// for aliasing; leave such configurations intact and let the adapter's
	// exact byte/hash checks continue to attest the original document.
	for _, out := range next {
		var tag string
		if raw, exists := out["tag"]; !exists || bytes.Equal(raw, []byte(`""`)) {
			return candidate, nil
		} else if json.Unmarshal(raw, &tag) != nil {
			return nil, ErrOutboundVersioning
		}
	}
	installed := make(map[string]map[string]json.RawMessage, len(old))
	for _, out := range old {
		var tag string
		if _, exists := out["tag"]; !exists {
			continue
		}
		if json.Unmarshal(out["tag"], &tag) != nil {
			return nil, ErrOutboundVersioning
		}
		if tag == "" {
			continue
		}
		base := leafTagBase(tag)
		if _, exists := installed[base]; exists {
			// Ambiguous aliases cannot attest which installed handler to retain.
			return nil, ErrOutboundVersioning
		}
		installed[base] = out
	}
	aliases, tags := map[string]string{}, map[string]bool{}
	// Unknown extensions may reference leaf tags outside routing.rules. Do not
	// rewrite such documents without an explicit reference-rewriting contract.
	for _, out := range next {
		if !sameLeafPayload(out, out) {
			return candidate, nil
		}
	}
	for _, out := range next {
		var tag string
		if json.Unmarshal(out["tag"], &tag) != nil || tag == "" || tags[tag] {
			return nil, ErrOutboundVersioning
		}
		tags[tag] = true
		prior := installed[leafTagBase(tag)]
		if prior == nil || bytes.Equal(prior["tag"], out["tag"]) || !sameLeafPayload(prior, out) {
			continue
		}
		var oldTag string
		_ = json.Unmarshal(prior["tag"], &oldTag)
		aliases[tag] = oldTag
	}
	if len(aliases) == 0 {
		return candidate, nil
	}
	targets := map[string]bool{}
	for _, tag := range aliases {
		if tags[tag] || targets[tag] {
			return nil, ErrOutboundVersioning
		}
		targets[tag] = true
	}
	for _, out := range next {
		var tag string
		_ = json.Unmarshal(out["tag"], &tag)
		if alias := aliases[tag]; alias != "" {
			out["tag"], _ = json.Marshal(alias)
		}
	}
	var routing map[string]json.RawMessage
	var rules []map[string]json.RawMessage
	if json.Unmarshal(after["routing"], &routing) != nil || json.Unmarshal(routing["rules"], &rules) != nil {
		return nil, ErrOutboundVersioning
	}
	if raw, exists := routing["balancers"]; exists {
		var balancers []json.RawMessage
		if json.Unmarshal(raw, &balancers) != nil || len(balancers) != 0 {
			return candidate, nil
		}
	}
	for _, rule := range rules {
		var tag string
		_ = json.Unmarshal(rule["outboundTag"], &tag)
		if alias := aliases[tag]; alias != "" {
			rule["outboundTag"], _ = json.Marshal(alias)
		}
	}
	routing["rules"], _ = json.Marshal(rules)
	after["routing"], _ = json.Marshal(routing)
	after["outbounds"], _ = json.Marshal(next)
	return json.Marshal(after)
}

func leafTagBase(tag string) string {
	name, metadata, _ := strings.Cut(tag, "--")
	if len(name) >= 34 && name[len(name)-34:len(name)-32] == "-v" {
		if _, err := hex.DecodeString(name[len(name)-32:]); err == nil {
			name = name[:len(name)-34]
		}
	}
	if strings.Contains(tag, "--") {
		return name + "--" + metadata
	}
	return name
}

func sameLeafPayload(a, b map[string]json.RawMessage) bool {
	var protocol string
	if json.Unmarshal(a["protocol"], &protocol) != nil || (protocol != "freedom" && protocol != "socks" && protocol != "blackhole") {
		return false
	}
	payload := func(out map[string]json.RawMessage) ([]byte, bool) {
		copy := make(map[string]any, len(out))
		for key, raw := range out {
			if key == "tag" {
				continue
			}
			if key != "protocol" && key != "settings" {
				return nil, false
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value any
			if decoder.Decode(&value) != nil {
				return nil, false
			}
			copy[key] = value
		}
		raw, err := json.Marshal(copy)
		return raw, err == nil
	}
	left, ok := payload(a)
	right, otherOK := payload(b)
	return ok && otherOK && bytes.Equal(left, right)
}
