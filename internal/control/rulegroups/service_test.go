package rulegroups

import (
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestNormalizeBatchRequestRequiresExactCASSet(t *testing.T) {
	valid := BatchRequest{Operation: BatchPause, RuleIDs: []string{"rule-1", "rule-2"}, ExpectedRevisions: map[string]int64{"rule-1": 1, "rule-2": 2}}
	if _, err := NormalizeBatchRequest(valid); err != nil {
		t.Fatal(err)
	}
	cases := []BatchRequest{
		{Operation: BatchPause, RuleIDs: nil, ExpectedRevisions: map[string]int64{}},
		{Operation: BatchPause, RuleIDs: []string{"rule-1", "rule-1"}, ExpectedRevisions: map[string]int64{"rule-1": 1}},
		{Operation: BatchResume, RuleIDs: []string{"rule-1"}, ExpectedRevisions: map[string]int64{}},
		{Operation: BatchPause, RuleGroupID: "group-1", RuleIDs: []string{"rule-1"}, ExpectedRevisions: map[string]int64{"rule-1": 1}},
	}
	for _, input := range cases {
		if _, err := NormalizeBatchRequest(input); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("expected validation failure for %+v, got %v", input, err)
		}
	}
}

func TestNormalizeBatchDeleteRequiresCASRevisions(t *testing.T) {
	input := BatchRequest{Operation: BatchDelete, RuleIDs: []string{"rule-1"}, ExpectedRevisions: map[string]int64{"rule-1": 3}}
	if normalized, err := NormalizeBatchRequest(input); err != nil || normalized.Operation != BatchDelete {
		t.Fatalf("delete normalization failed: %+v, %v", normalized, err)
	}
	input.RuleGroupID = "group-1"
	if _, err := NormalizeBatchRequest(input); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("delete accepted a rule_group_id")
	}
}
