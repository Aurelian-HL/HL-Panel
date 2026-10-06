package memoryrepo

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/faults"
)

// businessMutation contains public result data only. Callers scope keys by
// authenticated actor, operation and resource before entering these helpers.
type businessMutation struct {
	RequestSHA256 string
	ResourceID    string
	Result        json.RawMessage
}

func (s *Store) replayBusinessLocked(key, hash string, target any) (bool, error) {
	if key == "" || hash == "" {
		return false, fmt.Errorf("%w: idempotency key and request hash are required", faults.ErrValidation)
	}
	existing, found := s.businessIdempotency[key]
	if !found {
		return false, nil
	}
	if existing.RequestSHA256 != hash {
		return false, faults.ErrIdempotencyConflict
	}
	if json.Unmarshal(existing.Result, target) != nil {
		return false, errors.New("stored operation result is invalid")
	}
	return true, nil
}

func (s *Store) recordBusinessLocked(key, hash, id string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return errors.New("encode operation result failed")
	}
	s.businessIdempotency[key] = businessMutation{RequestSHA256: hash, ResourceID: id, Result: raw}
	return nil
}
