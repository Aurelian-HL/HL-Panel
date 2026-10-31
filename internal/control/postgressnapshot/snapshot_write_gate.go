package postgressnapshot

import (
	"context"
	"sync"
)

// Serial transactions hold this gate for their lifetime. Explicitly replayable
// private preparations hold it only while committing. The SQL row lock and MVCC
// revalidation still arbitrate other processes or stores.
type snapshotWriteGate struct {
	once   sync.Once
	permit chan struct{}
}

// Borrow a detached restore without waiting behind a serial transaction.
func (g *snapshotWriteGate) tryAcquire() (func(), bool) {
	g.once.Do(func() { g.permit = make(chan struct{}, 1) })
	select {
	case g.permit <- struct{}{}:
		return func() { <-g.permit }, true
	default:
		return nil, false
	}
}

func (g *snapshotWriteGate) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.once.Do(func() { g.permit = make(chan struct{}, 1) })
	select {
	case g.permit <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.permit
			return nil, err
		}
		return func() { <-g.permit }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
