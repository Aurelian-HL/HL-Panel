package xrayconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestLeafContinuityPreservesLegacyAndVersionedHandlers(t *testing.T) {
	legacy, _ := json.Marshal(readVersioned(t, []byte(leafFixture)))
	versioned, err := VersionLeafOutbounds(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, previous := range [][]byte{legacy, versioned} {
		got, err := PreserveUnchangedLeafTags(previous, versioned)
		if err != nil || !bytes.Equal(got, previous) {
			t.Fatalf("stable handler replaced: %v", err)
		}
		// Re-preparing the same generation must be byte-for-byte stable.
		again, err := PreserveUnchangedLeafTags(got, versioned)
		if err != nil || !bytes.Equal(again, got) {
			t.Fatalf("unstable prepare: %v", err)
		}
	}
}

func TestLeafContinuityNeverHidesSettingsOrAccountingChanges(t *testing.T) {
	for name, fixture := range map[string]string{
		"credentials": strings.Replace(leafFixture, `"pass":"one"`, `"pass":"two"`, 1),
		"accounting":  strings.Replace(leafFixture, "--metadata", "--other-account", -1),
	} {
		t.Run(name, func(t *testing.T) {
			candidate, err := VersionLeafOutbounds([]byte(fixture))
			if err != nil {
				t.Fatal(err)
			}
			got, err := PreserveUnchangedLeafTags([]byte(leafFixture), candidate)
			if err != nil {
				t.Fatal(err)
			}
			actual, desired := readVersioned(t, got), readVersioned(t, candidate)
			if !reflect.DeepEqual(actual["outbounds"].([]any)[0], desired["outbounds"].([]any)[0]) {
				t.Fatal("changed handler reused")
			}
			if actual["routing"].(map[string]any)["rules"].([]any)[1].(map[string]any)["outboundTag"] != desired["outbounds"].([]any)[0].(map[string]any)["tag"] {
				t.Fatal("changed handler route lost")
			}
			if !bytes.Contains(got, []byte("9007199254740993")) {
				t.Fatal("numeric precision lost")
			}
		})
	}
}

func TestLeafContinuityRejectsAliasCollisions(t *testing.T) {
	versioned, _ := VersionLeafOutbounds([]byte(leafFixture))
	for _, mutate := range []func(map[string]any){
		func(d map[string]any) {
			outs := d["outbounds"].([]any)
			outs = append(outs, outs[0])
			d["outbounds"] = outs
		},
		func(d map[string]any) {
			outs := d["outbounds"].([]any)
			clone := map[string]any{}
			for k, v := range outs[0].(map[string]any) {
				clone[k] = v
			}
			clone["tag"] = "landing-v00000000000000000000000000000000--metadata"
			d["outbounds"] = append(outs, clone)
		},
	} {
		doc := readVersioned(t, versioned)
		mutate(doc)
		raw, _ := json.Marshal(doc)
		if _, err := PreserveUnchangedLeafTags([]byte(leafFixture), raw); !errors.Is(err, ErrOutboundVersioning) {
			t.Fatal("ambiguous alias accepted")
		}
	}
}

func TestLeafContinuityLeavesUnknownReferencesUntouched(t *testing.T) {
	versioned, _ := VersionLeafOutbounds([]byte(leafFixture))
	doc := readVersioned(t, versioned)
	doc["outbounds"].([]any)[0].(map[string]any)["proxySettings"] = map[string]any{"tag": doc["outbounds"].([]any)[1].(map[string]any)["tag"]}
	raw, _ := json.Marshal(doc)
	got, err := PreserveUnchangedLeafTags([]byte(leafFixture), raw)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("unsupported chain references rewritten")
	}
}

// Match the incident's scale: 440 listeners with 1,200 leaf outbounds. A
// single deletion must not schedule a content-version migration of the rest.
func TestLeafContinuityLargeLegacyDeletion(t *testing.T) {
	outs, routes := []any{}, []any{}
	for j := 0; j < 1200; j++ {
		tag := fmt.Sprintf("landing-%d--account-%d", j, j%440)
		outs = append(outs, map[string]any{"tag": tag, "protocol": "freedom", "settings": map[string]any{}})
		routes = append(routes, map[string]any{"inboundTag": []string{fmt.Sprintf("in-%d", j%440)}, "outboundTag": tag})
	}
	legacy, _ := json.Marshal(map[string]any{"outbounds": outs, "routing": map[string]any{"rules": routes}})
	outs = outs[1:]
	routes = routes[1:]
	deleted, _ := json.Marshal(map[string]any{"outbounds": outs, "routing": map[string]any{"rules": routes}})
	versioned, err := VersionLeafOutbounds(deleted)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PreserveUnchangedLeafTags(legacy, versioned)
	if err != nil || !bytes.Equal(got, deleted) {
		t.Fatalf("deletion migrated unchanged handlers: %v", err)
	}
}
