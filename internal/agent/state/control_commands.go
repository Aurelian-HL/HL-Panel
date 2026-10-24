package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const (
	maxControlRecords      = 4096
	maxControlJournalBytes = 4 << 20
)

type controlRecord struct {
	ID      string                               `json:"id"`
	Command agentv1.ControlCommand               `json:"command"`
	Result  *agentv1.ControlCommandResultRequest `json:"result,omitempty"`
}

type controlJournal struct {
	Version int             `json:"version"`
	Records []controlRecord `json:"records"`
}

// BeginControl durably claims a destructive command before its first side
// effect. A non-nil result is a replay, including an interrupted execution whose
// outcome cannot safely be inferred. The caller must serialize claim/execution/
// completion, but must not hold engine lifecycle locks during result delivery.
// Keep this file separate from the strictly decoded, rollback-compatible state.
func (s *AtomicStateStore) BeginControl(command agentv1.ControlCommandEnvelope) (*agentv1.ControlCommandResultRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validControlIdentity(command) {
		return nil, errors.New("invalid destructive control command")
	}
	journal, err := s.loadControlUnlocked()
	if err != nil {
		return nil, err
	}
	for _, record := range journal.Records {
		if record.ID != command.ID {
			continue
		}
		if record.Command != command.Command {
			return nil, errors.New("control command ID was reused for a different action")
		}
		if record.Result != nil {
			result := *record.Result
			return &result, nil
		}
		return &agentv1.ControlCommandResultRequest{
			ID: command.ID, Status: "failed",
			Message: "此前命令执行结果不确定，已阻止重复操作；请检查节点后重新下发新命令",
		}, nil
	}
	// Never evict an ID silently: that would make an old restart executable
	// again. This bound covers years of operator actions while corrupt/oversized
	// journals fail closed without stopping a live forwarding process.
	if len(journal.Records) >= maxControlRecords {
		return nil, errors.New("control command journal is full; maintenance required")
	}
	journal.Records = append(journal.Records, controlRecord{ID: command.ID, Command: command.Command})
	return nil, s.saveControlUnlocked(journal)
}

func (s *AtomicStateStore) CompleteControl(command agentv1.ControlCommandEnvelope, result agentv1.ControlCommandResultRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validControlIdentity(command) || !validControlResult(command.ID, result) {
		return errors.New("invalid destructive control result")
	}
	journal, err := s.loadControlUnlocked()
	if err != nil {
		return err
	}
	for i := range journal.Records {
		record := &journal.Records[i]
		if record.ID != command.ID {
			continue
		}
		if record.Command != command.Command {
			return errors.New("control command ID was reused for a different action")
		}
		if record.Result != nil {
			if *record.Result == result {
				return nil
			}
			return errors.New("control command result is already final")
		}
		record.Result = &result
		return s.saveControlUnlocked(journal)
	}
	return errors.New("control command was not durably claimed")
}

func validControlIdentity(command agentv1.ControlCommandEnvelope) bool {
	return len(command.ID) > 0 && len(command.ID) <= 128 &&
		(command.Command == agentv1.ControlStop || command.Command == agentv1.ControlRestart)
}

func validControlResult(id string, result agentv1.ControlCommandResultRequest) bool {
	return result.ID == id && (result.Status == "succeeded" || result.Status == "failed") &&
		len(result.Message) <= 512 && result.Logs == ""
}

func (s *AtomicStateStore) loadControlUnlocked() (controlJournal, error) {
	path := s.statePath + ".control-commands"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return controlJournal{Version: 1}, nil
	}
	if err != nil {
		return controlJournal{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxControlJournalBytes {
		return controlJournal{}, errors.New("invalid control command journal file")
	}
	f, err := os.Open(path)
	if err != nil {
		return controlJournal{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxControlJournalBytes+1))
	if err != nil || len(data) > maxControlJournalBytes {
		return controlJournal{}, errors.New("cannot read bounded control command journal")
	}
	var journal controlJournal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return controlJournal{}, fmt.Errorf("decode control command journal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return controlJournal{}, errors.New("trailing data in control command journal")
	}
	if journal.Version != 1 || len(journal.Records) > maxControlRecords {
		return controlJournal{}, errors.New("invalid control command journal version or size")
	}
	seen := make(map[string]bool, len(journal.Records))
	for _, record := range journal.Records {
		if !validControlIdentity(agentv1.ControlCommandEnvelope{ID: record.ID, Command: record.Command}) ||
			seen[record.ID] || (record.Result != nil && !validControlResult(record.ID, *record.Result)) {
			return controlJournal{}, errors.New("invalid control command journal record")
		}
		seen[record.ID] = true
	}
	return journal, nil
}

func (s *AtomicStateStore) saveControlUnlocked(journal controlJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if len(data) > maxControlJournalBytes {
		return errors.New("control command journal exceeds size limit")
	}
	return writeFileAtomic(s.statePath+".control-commands", data, 0o600)
}
