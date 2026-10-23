// Package xrayconfig contains bounded native configuration transformations
// shared by the control-plane compilers and isolated engine tests.
package xrayconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var ErrOutboundVersioning = errors.New("xray outbound versioning requires validated leaf outbounds and explicit routes")

// VersionLeafOutbounds is applied once to a freshly compiled managed fragment.
// A content change gets a distinct tag so the agent can install the new handler
// before switching routes and retiring the old handler. Inbounds, credentials,
// route order, and the accounting metadata after -- remain unchanged.
//
// Only bounded leaf outbounds are supported. Chained handlers, balancers and
// unknown outbound extensions must not have their references silently rewritten.
func VersionLeafOutbounds(raw []byte) ([]byte, error) {
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&doc) != nil || doc == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrOutboundVersioning
	}
	outs, ok := doc["outbounds"].([]any)
	if !ok || len(outs) == 0 {
		return nil, ErrOutboundVersioning
	}
	tags := make(map[string]string, len(outs))
	for _, value := range outs {
		out, ok := value.(map[string]any)
		if !ok {
			return nil, ErrOutboundVersioning
		}
		for key := range out {
			if key != "tag" && key != "protocol" && key != "settings" {
				return nil, ErrOutboundVersioning
			}
		}
		protocol, _ := out["protocol"].(string)
		if protocol != "freedom" && protocol != "socks" && protocol != "blackhole" {
			return nil, ErrOutboundVersioning
		}
		tag, _ := out["tag"].(string)
		if tag == "" || tags[tag] != "" {
			return nil, ErrOutboundVersioning
		}
		// Marshal the parsed object for stable key order, keeping exact JSON
		// numbers. Never put runtime settings or credentials in diagnostics.
		canonical, err := json.Marshal(out)
		if err != nil {
			return nil, ErrOutboundVersioning
		}
		sum := sha256.Sum256(canonical)
		version := "-v" + hex.EncodeToString(sum[:16])
		if index := strings.Index(tag, "--"); index >= 0 {
			tags[tag] = tag[:index] + version + tag[index:]
		} else {
			tags[tag] = tag + version
		}
		out["tag"] = tags[tag]
	}
	routing, ok := doc["routing"].(map[string]any)
	if !ok {
		return nil, ErrOutboundVersioning
	}
	if balancers, exists := routing["balancers"]; exists {
		values, ok := balancers.([]any)
		if !ok || len(values) != 0 {
			return nil, ErrOutboundVersioning
		}
	}
	rules, ok := routing["rules"].([]any)
	if !ok || len(rules) == 0 {
		return nil, ErrOutboundVersioning
	}
	for _, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			return nil, ErrOutboundVersioning
		}
		tag, _ := rule["outboundTag"].(string)
		if _, exists := rule["balancerTag"]; exists || tags[tag] == "" {
			return nil, ErrOutboundVersioning
		}
		rule["outboundTag"] = tags[tag]
	}
	return json.Marshal(doc)
}
