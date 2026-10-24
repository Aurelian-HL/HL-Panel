package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestDesiredReceiptPreflightRequiresRoomForBothTerminalReceipts(t *testing.T) {
	store := newTestStore(t.TempDir())
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	for gen := 1; gen <= 16; gen++ {
		desired.Generation = agentv1.NodeConfigGeneration(gen)
		if _, err := store.Begin(desired, time.Now()); err != nil {
			t.Fatal(err)
		}
		commit := agentv1.ApplyResultRequest{Generation: desired.Generation, AttemptID: fmt.Sprintf("%032x", gen),
			Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, ConfigSHA256: desired.ConfigSHA256}
		verify := commit
		verify.Phase = agentv1.ApplyPhaseVerify
		if _, err := store.CompleteWithReceipts(desired, []agentv1.ApplyResultRequest{commit, verify}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	receipts, err := store.PendingApplyReceipts()
	if err != nil || len(receipts) != 32 {
		t.Fatalf("setup queue = %d, %v", len(receipts), err)
	}
	next := desired
	next.Generation++
	for _, remaining := range []int{32, 31, 30} {
		before, err := os.ReadFile(store.statePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CheckDesired(desired); err != nil {
			t.Fatalf("valid applied replay rejected with %d receipts: %v", remaining, err)
		}
		var want error
		if remaining > 30 {
			want = ErrApplyReceiptQueueFull
		}
		if _, err := store.CheckDesired(next); !errors.Is(err, want) {
			t.Fatalf("preflight with %d receipts = %v, want %v", remaining, err, want)
		}
		after, err := os.ReadFile(store.statePath)
		if err != nil || string(before) != string(after) {
			t.Fatalf("preflight wrote durable state: %v", err)
		}
		if remaining > 30 {
			if err := store.AcknowledgeApplyReceipt(receipts[32-remaining]); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestVerifiedReceiptsPersistAndAcknowledgeInOrder(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	store := NewAtomicStateStore(path, filepath.Join(directory, "configurations"))
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(desired, time.Now()); err != nil {
		t.Fatal(err)
	}
	commit := agentv1.ApplyResultRequest{Generation: desired.Generation, AttemptID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded,
		ConfigSHA256: desired.ConfigSHA256, EngineMode: "xray"}
	verify := commit
	verify.Phase = agentv1.ApplyPhaseVerify
	if _, err := store.CompleteWithReceipts(desired, []agentv1.ApplyResultRequest{commit, verify}, time.Now()); err != nil {
		t.Fatal(err)
	}
	reopened := NewAtomicStateStore(path, filepath.Join(directory, "configurations"))
	receipts, err := reopened.PendingApplyReceipts()
	if err != nil || len(receipts) != 2 || receipts[0] != commit || receipts[1] != verify {
		t.Fatalf("reopened receipts = %#v; error = %v", receipts, err)
	}
	if err := reopened.AcknowledgeApplyReceipt(verify); err == nil {
		t.Fatal("out-of-order receipt acknowledgement accepted")
	}
	if err := reopened.AcknowledgeApplyReceipt(commit); err != nil {
		t.Fatal(err)
	}
	if err := reopened.AcknowledgeApplyReceipt(commit); err == nil {
		t.Fatal("duplicate receipt acknowledgement accepted")
	}
	if err := reopened.AcknowledgeApplyReceipt(verify); err != nil {
		t.Fatal(err)
	}
	receipts, err = reopened.PendingApplyReceipts()
	if err != nil || len(receipts) != 0 {
		t.Fatalf("remaining receipts = %#v; error = %v", receipts, err)
	}
}

func TestStateRejectsMixedAttemptReceipts(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	store := NewAtomicStateStore(path, filepath.Join(directory, "configurations"))
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(desired, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(desired, time.Now()); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	commit := agentv1.ApplyResultRequest{Generation: desired.Generation, AttemptID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded,
		ConfigSHA256: desired.ConfigSHA256}
	verify := commit
	verify.Phase = agentv1.ApplyPhaseVerify
	verify.AttemptID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	current.PendingReceipts = []agentv1.ApplyResultRequest{commit, verify}
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("mixed attempt receipt state was accepted")
	}
}

func TestBatchAcknowledgementRequiresExactDurablePrefix(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	store := NewAtomicStateStore(path, filepath.Join(directory, "configurations"))
	desired := testDesired(1, `{"schema_version":1,"fragments":[]}`)
	if _, err := store.Begin(desired, time.Now()); err != nil {
		t.Fatal(err)
	}
	commit := agentv1.ApplyResultRequest{Generation: 1, AttemptID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ConfigSHA256: desired.ConfigSHA256, Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded}
	verify := commit
	verify.Phase = agentv1.ApplyPhaseVerify
	pair := []agentv1.ApplyResultRequest{commit, verify}
	if _, err := store.CompleteWithReceipts(desired, pair, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]agentv1.ApplyResultRequest{nil, {verify, commit}, {commit, commit}, {commit, verify, verify}} {
		if err := store.AcknowledgeApplyReceipts(invalid); err == nil {
			t.Fatal("invalid prefix accepted")
		}
		pending, err := store.PendingApplyReceipts()
		if err != nil || len(pending) != 2 {
			t.Fatal("rejected acknowledgement changed queue", err)
		}
	}
	if err := store.AcknowledgeApplyReceipts(pair); err != nil {
		t.Fatal(err)
	}
	reopened := NewAtomicStateStore(path, filepath.Join(directory, "configurations"))
	if pending, err := reopened.PendingApplyReceipts(); err != nil || len(pending) != 0 {
		t.Fatal("batch removal was not durable", err)
	}
}
