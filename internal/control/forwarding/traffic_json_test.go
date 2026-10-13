package forwarding

import (
	"encoding/json"
	"testing"
)

func TestRuleJSONIncludesZeroTrafficProjection(t *testing.T) {
	raw, err := json.Marshal(Rule{ID: "rule-zero"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["traffic_used_bytes"]) != "0" {
		t.Fatalf("zero traffic must be explicit, got %s", raw)
	}
}
