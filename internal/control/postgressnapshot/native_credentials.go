package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

// RotateNativeSOCKS5Credentials uses the same transaction, config history and
// audit journal as ordinary edits; no direct snapshot SQL patch is permitted.
func (s *Store) RotateNativeSOCKS5Credentials(ctx context.Context, in vlessruntime.NativeCredentialsRotation) (vlessruntime.NativeCredentialsResult, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (vlessruntime.NativeCredentialsResult, error) {
		return state.RotateNativeSOCKS5Credentials(ctx, in)
	})
}
