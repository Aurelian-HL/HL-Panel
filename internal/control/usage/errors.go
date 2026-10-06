package usage

import (
	"errors"

	"github.com/hongle/hl-panel/internal/control/faults"
)

var (
	ErrNegativeBytes       = errors.New("usage bytes must be non-negative")
	ErrByteOverflow        = errors.New("usage byte calculation overflow")
	ErrInvalidMultiplier   = errors.New("usage multiplier is outside the supported range")
	ErrSequenceGap         = errors.New("usage sequence has a gap")
	ErrSequenceOutOfOrder  = errors.New("usage sequence is out of order")
	ErrSequenceOverflow    = errors.New("usage sequence cannot be incremented")
	ErrIdempotencyConflict = faults.ErrIdempotencyConflict
)
