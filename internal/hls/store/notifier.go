package store

import (
	"context"
	"sync"
)

// Notifier uses sync.Cond to wake goroutines blocked on LL-HLS blocking
// playlist requests whenever a new part or segment is available.
type Notifier struct {
	mu       sync.Mutex
	cond     *sync.Cond
	lastMSN  int
	lastPart int // -1 means a full segment was completed
}

// NewNotifier creates a Notifier with initial state at MSN -1.
func NewNotifier() *Notifier {
	n := &Notifier{lastMSN: -1, lastPart: -1}
	n.cond = sync.NewCond(&n.mu)
	return n
}

// Broadcast records the latest MSN/part index and wakes all blocked waiters.
// partIdx should be -1 when a full segment is completed.
func (n *Notifier) Broadcast(msn, partIdx int) {
	n.mu.Lock()
	n.lastMSN = msn
	n.lastPart = partIdx
	n.cond.Broadcast()
	n.mu.Unlock()
}

// WaitFor blocks until the store contains at least segment targetMSN and
// (if targetPart >= 0) part targetPart of that segment.
// Returns ctx.Err() if the context is cancelled before the condition is met.
func (n *Notifier) WaitFor(ctx context.Context, targetMSN, targetPart int) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n.lastMSN > targetMSN {
			return nil // segment is already complete
		}
		if n.lastMSN == targetMSN {
			if targetPart < 0 && n.lastPart < 0 {
				return nil // full segment completed
			}
			// lastPart == -1 means the segment is fully complete, so all
			// parts of that segment are available.
			if targetPart >= 0 && (n.lastPart < 0 || n.lastPart >= targetPart) {
				return nil // desired part is available
			}
		}
		n.cond.Wait()
	}
}

// OnContextCancel starts a background goroutine that calls Broadcast when
// ctx is done, ensuring WaitFor can check ctx.Err() and unblock.
// The returned cancel function must be called to clean up the goroutine
// when it is no longer needed.
func (n *Notifier) OnContextCancel(ctx context.Context) (cancel func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			n.cond.Broadcast()
		case <-done:
		}
	}()
	return func() { close(done) }
}
