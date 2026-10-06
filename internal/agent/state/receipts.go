package state

import (
	"errors"
	"fmt"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func validatePendingReceipts(receipts []agentv1.ApplyResultRequest) error {
	if len(receipts) > 2 {
		return errors.New("too many pending apply receipts")
	}
	var attemptID, hash string
	var generation agentv1.NodeConfigGeneration
	for index, receipt := range receipts {
		if receipt.Generation == 0 || receipt.Status != agentv1.ApplyStatusSucceeded ||
			(receipt.Phase != agentv1.ApplyPhaseCommit && receipt.Phase != agentv1.ApplyPhaseVerify) ||
			(receipt.EngineMode != "" && receipt.EngineMode != "gost" && receipt.EngineMode != "xray" && receipt.EngineMode != "mixed") ||
			!validReceiptAttemptID(receipt.AttemptID) || len(receipt.Message) > MaxApplyMessageBytes {
			return errors.New("invalid pending apply receipt")
		}
		if _, err := normalizeSHA256(receipt.ConfigSHA256); err != nil {
			return fmt.Errorf("invalid pending apply receipt hash: %w", err)
		}
		if index == 0 {
			attemptID, hash, generation = receipt.AttemptID, receipt.ConfigSHA256, receipt.Generation
		} else if receipt.AttemptID != attemptID || receipt.ConfigSHA256 != hash || receipt.Generation != generation ||
			receipt.Phase == receipts[0].Phase || receipt.EngineMode != receipts[0].EngineMode {
			return errors.New("pending apply receipts do not describe one attempt")
		}
	}
	return nil
}

func validReceiptAttemptID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (s *AtomicStateStore) PendingApplyReceipts() ([]agentv1.ApplyResultRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	return append([]agentv1.ApplyResultRequest(nil), current.PendingReceipts...), nil
}

func (s *AtomicStateStore) AcknowledgeApplyReceipt(receipt agentv1.ApplyResultRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if len(current.PendingReceipts) == 0 || current.PendingReceipts[0] != receipt {
		return errors.New("pending apply receipt changed before acknowledgement")
	}
	current.PendingReceipts = append([]agentv1.ApplyResultRequest(nil), current.PendingReceipts[1:]...)
	return s.saveUnlocked(current)
}

func (s *AtomicStateStore) ClearApplyReceipts() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if len(current.PendingReceipts) == 0 {
		return nil
	}
	current.PendingReceipts = nil
	return s.saveUnlocked(current)
}
