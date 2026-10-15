package state

import (
	"errors"
	"os"
)

// Keep operator intent outside state.json: older agents strictly decode that
// journal, and must still be able to read it after a binary rollback.
func (s *AtomicStateStore) EngineStopped() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.statePath + ".engine-intent"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16 {
		return false, errors.New("invalid engine intent file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	switch string(data) {
	case "stopped\n":
		return true, nil
	case "running\n":
		return false, nil
	default:
		return false, errors.New("invalid engine intent")
	}
}

func (s *AtomicStateStore) SetEngineStopped(stopped bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent := "running\n"
	if stopped {
		intent = "stopped\n"
	}
	return writeFileAtomic(s.statePath+".engine-intent", []byte(intent), 0o600)
}
