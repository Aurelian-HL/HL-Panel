package postgressnapshot

import (
	"context"
	"sync"
)

// One instance prepares one snapshot writer at a time. Preparing many copies
// concurrently makes all but the first stale, forcing another full decode
// under the database lock. Wait before borrowing a connection so queued writes
// also leave room for authentication, subscriptions and heartbeats. The SQL
// row lock and MVCC revalidation still arbitrate other processes or stores.
type snapshotWriteGate struct {
	once   sync.Once
	permit chan struct{}
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
