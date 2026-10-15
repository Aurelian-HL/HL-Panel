package engine

import "time"

// Bound only configuration validation, not the lifetime of a healthy engine.
const engineValidationTimeout = 15 * time.Second
