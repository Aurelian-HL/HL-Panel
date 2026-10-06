package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

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
