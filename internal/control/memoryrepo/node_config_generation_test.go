package memoryrepo

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func nodeGenerationFixture(t *testing.T) (*Store, generations.NodeConfigGeneration) {
	t.Helper()
	s := New(auth.Administrator{})
	s.nodes["generation-node"] = nodes.Node{ID: "generation-node"}
	s.membersByGroup["generation-group"] = map[string]groups.Member{"generation-node": {NodeID: "generation-node"}}
	s.revisionsByGroup["generation-group"] = []generations.GroupRevision{{GroupID: "generation-group", Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{"services":[],"fixture":1}`)}}
	config, created, err := s.compileNodeConfigLocked("generation-node", time.Now().UTC())
	if err != nil || !created {
		t.Fatal("initial configuration failed", err)
	}
	return s, config
}

func changeGenerationFixture(s *Store) {
	s.revisionsByGroup["generation-group"][0].Config = json.RawMessage(`{"services":[],"fixture":2}`)
}

func TestNodeConfigGenerationNeverReusesHotOrAppliedGeneration(t *testing.T) {
	for _, test := range []struct {
		name                    string
		applied, retained, want int64
	}{
		{"retained-after-counter-rollback", 558, 559, 560},
		{"applied-ahead-of-counter", 570, 559, 571},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, original := nodeGenerationFixture(t)
			node := s.nodes[original.NodeID]
			node.DesiredGeneration, node.AppliedGeneration = 558, test.applied
			s.nodes[node.ID] = node
			retained := original
			retained.Generation, retained.ID = test.retained, "retained-generation"
			s.nodeConfigsByNode[node.ID][test.retained] = retained
			changeGenerationFixture(s)
			config, created, err := s.compileNodeConfigLocked(node.ID, time.Now().UTC())
			if err != nil || !created || config.Generation != test.want {
				t.Fatalf("allocated generation=%d, want=%d, created=%t, error=%v", config.Generation, test.want, created, err)
			}
			if !reflect.DeepEqual(s.nodeConfigsByNode[node.ID][test.retained], retained) {
				t.Fatal("new compilation overwrote retained configuration")
			}
		})
	}
}

func TestNodeConfigGenerationArchiveFloorAndUnchangedFastPath(t *testing.T) {
	s, original := nodeGenerationFixture(t)
	reads := 0
	s.SetNodeConfigGenerationFloorReader(func(nodeID string) (int64, error) {
		reads++
		if nodeID != original.NodeID {
			t.Fatal("read another node's history")
		}
		return 559, nil
	})
	config, created, err := s.compileNodeConfigLocked(original.NodeID, time.Now().UTC())
	if err != nil || created || config.Generation != original.Generation || reads != 0 {
		t.Fatal("unchanged compilation accessed history or advanced generation", reads, err)
	}
	changeGenerationFixture(s)
	config, created, err = s.compileNodeConfigLocked(original.NodeID, time.Now().UTC())
	if err != nil || !created || config.Generation != 560 || reads != 1 {
		t.Fatal("archived generation was reused", config.Generation, reads, err)
	}
}

func TestNodeConfigGenerationFailurePreservesConfigurationAndNotification(t *testing.T) {
	for _, kind := range []string{"archive-read-failure", "archive-overflow", "applied-overflow", "invalid-archive-floor"} {
		t.Run(kind, func(t *testing.T) {
			s, original := nodeGenerationFixture(t)
			wantErr := errors.New("isolated history read failure")
			s.SetNodeConfigGenerationFloorReader(func(string) (int64, error) {
				switch kind {
				case "archive-read-failure":
					return 0, wantErr
				case "archive-overflow":
					return math.MaxInt64, nil
				case "invalid-archive-floor":
					return -1, nil
				default:
					return 0, nil
				}
			})
			if kind == "applied-overflow" {
				node := s.nodes[original.NodeID]
				node.AppliedGeneration = math.MaxInt64
				s.nodes[node.ID] = node
			}
			before := s.nodes[original.NodeID]
			changeGenerationFixture(s)
			wake := s.DesiredConfigChanges(original.NodeID)
			defer s.ReleaseDesiredConfigChanges(original.NodeID, wake)
			_, created, err := s.compileNodeConfigLocked(original.NodeID, time.Now().UTC())
			if err == nil || created || (kind == "archive-read-failure" && !errors.Is(err, wantErr)) {
				t.Fatal("unsafe generation allocation was not rejected", err)
			}
			if !reflect.DeepEqual(before, s.nodes[original.NodeID]) || len(s.nodeConfigsByNode[original.NodeID]) != 1 {
				t.Fatal("allocation failure changed node state")
			}
			got := s.nodeConfigsByNode[original.NodeID][original.Generation]
			if !reflect.DeepEqual(got, original) {
				t.Fatal("allocation failure changed desired configuration")
			}
			requireNoDesiredChange(t, wake)
		})
	}
}
