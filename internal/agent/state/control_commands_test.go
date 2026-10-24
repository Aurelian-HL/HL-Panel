package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestControlResultsSurviveReloadAndLaterCommands(t *testing.T) {
	for _, action := range []agentv1.ControlCommand{agentv1.ControlRestart, agentv1.ControlStop} {
		for _, status := range []string{"succeeded", "failed"} {
			t.Run(string(action)+"/"+status, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "state.json")
				s := NewAtomicStateStore(path, filepath.Join(root, "configs"))
				command := agentv1.ControlCommandEnvelope{ID: "first", Command: action}
				if cached, err := s.BeginControl(command); err != nil || cached != nil {
					t.Fatalf("first claim = %+v, %v", cached, err)
				}
				want := agentv1.ControlCommandResultRequest{ID: command.ID, Status: status, Message: "known result"}
				if err := s.CompleteControl(command, want); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 3; i++ {
					reloaded := NewAtomicStateStore(path, filepath.Join(root, "configs"))
					if got, err := reloaded.BeginControl(command); err != nil || got == nil || *got != want {
						t.Fatalf("replay after reload = %+v, %v", got, err)
					}
					later := agentv1.ControlCommandEnvelope{ID: fmt.Sprint("later-", i), Command: action}
					if got, err := reloaded.BeginControl(later); err != nil || got != nil {
						t.Fatalf("new command = %+v, %v", got, err)
					}
				}
				if _, err := s.Load(); err != nil {
					t.Fatalf("legacy state no longer readable: %v", err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("control commands changed the legacy state file")
				}
			})
		}
	}
}

func TestInterruptedControlCannotExecuteAgain(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	s := NewAtomicStateStore(path, filepath.Join(root, "configs"))
	command := agentv1.ControlCommandEnvelope{ID: "interrupted", Command: agentv1.ControlRestart}
	if got, err := s.BeginControl(command); got != nil || err != nil {
		t.Fatalf("first claim = %+v, %v", got, err)
	}
	reloaded := NewAtomicStateStore(path, filepath.Join(root, "configs"))
	for i := 0; i < 3; i++ {
		if got, err := reloaded.BeginControl(command); err != nil || got == nil || got.Status != "failed" || got.ID != command.ID {
			t.Fatalf("unknown execution was retried or reported successful = %+v, %v", got, err)
		}
	}
}

func TestControlClaimIsExclusiveWithinAgent(t *testing.T) {
	root := t.TempDir()
	s := NewAtomicStateStore(filepath.Join(root, "state.json"), filepath.Join(root, "configs"))
	command := agentv1.ControlCommandEnvelope{ID: "concurrent", Command: agentv1.ControlRestart}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.BeginControl(command)
			if err != nil {
				t.Error(err)
			} else if got == nil {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := claims.Load(); got != 1 {
		t.Fatalf("duplicate command claimed %d times", got)
	}
}

func TestControlIdentityAndFinalResultCannotBeOverwritten(t *testing.T) {
	root := t.TempDir()
	s := NewAtomicStateStore(filepath.Join(root, "state.json"), filepath.Join(root, "configs"))
	command := agentv1.ControlCommandEnvelope{ID: "identity", Command: agentv1.ControlRestart}
	result := agentv1.ControlCommandResultRequest{ID: command.ID, Status: "succeeded"}
	if err := s.CompleteControl(command, result); err == nil {
		t.Fatal("completed without a durable claim")
	}
	if _, err := s.BeginControl(command); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginControl(agentv1.ControlCommandEnvelope{ID: command.ID, Command: agentv1.ControlStop}); err == nil {
		t.Fatal("same ID accepted a different destructive action")
	}
	if err := s.CompleteControl(command, result); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteControl(command, result); err != nil {
		t.Fatal("identical completion is not idempotent", err)
	}
	result.Status = "failed"
	if err := s.CompleteControl(command, result); err == nil {
		t.Fatal("a final result could be rewritten")
	}
}

func TestControlJournalFailureRetainsUncertainClaim(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	s := NewAtomicStateStore(path, filepath.Join(root, "configs"))
	command := agentv1.ControlCommandEnvelope{ID: "storage-failure", Command: agentv1.ControlStop}
	if _, err := s.BeginControl(command); err != nil {
		t.Fatal(err)
	}
	journal := path + ".control-commands"
	saved := journal + ".test-saved"
	if err := os.Rename(journal, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteControl(command, agentv1.ControlCommandResultRequest{ID: command.ID, Status: "succeeded"}); err == nil {
		t.Fatal("unwritable completion was silently accepted")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, journal); err != nil {
		t.Fatal(err)
	}
	if got, err := s.BeginControl(command); err != nil || got == nil || got.Status != "failed" {
		t.Fatalf("failed completion allowed repeat execution = %+v, %v", got, err)
	}
}

func TestInvalidControlJournalsRejectNewDestructiveActions(t *testing.T) {
	for name, body := range map[string]string{
		"truncated": `{"version":1`,
		"version":   `{"version":2,"records":[]}`,
		"trailing":  `{"version":1,"records":[]} {}`,
		"unknown":   `{"version":1,"records":[],"unexpected":true}`,
		"duplicate": `{"version":1,"records":[{"id":"a","command":"stop"},{"id":"a","command":"stop"}]}`,
		"action":    `{"version":1,"records":[{"id":"a","command":"shell"}]}`,
		"result":    `{"version":1,"records":[{"id":"a","command":"stop","result":{"id":"b","status":"succeeded"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.json")
			if err := os.WriteFile(path+".control-commands", []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			s := NewAtomicStateStore(path, filepath.Join(root, "configs"))
			if _, err := s.BeginControl(agentv1.ControlCommandEnvelope{ID: "new", Command: agentv1.ControlRestart}); err == nil {
				t.Fatal("corrupt journal silently allowed destructive action")
			}
		})
	}
}

func TestFullControlJournalDoesNotForgetOldCommandIDs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	journal := controlJournal{Version: 1}
	for i := 0; i < maxControlRecords; i++ {
		journal.Records = append(journal.Records, controlRecord{ID: fmt.Sprint(i), Command: agentv1.ControlRestart})
	}
	body, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".control-commands", body, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewAtomicStateStore(path, filepath.Join(root, "configs"))
	if got, err := s.BeginControl(agentv1.ControlCommandEnvelope{ID: "0", Command: agentv1.ControlRestart}); err != nil || got == nil {
		t.Fatalf("old command was forgotten: %+v, %v", got, err)
	}
	if _, err := s.BeginControl(agentv1.ControlCommandEnvelope{ID: "new", Command: agentv1.ControlRestart}); err == nil {
		t.Fatal("full journal silently discarded previous command IDs")
	}
}
