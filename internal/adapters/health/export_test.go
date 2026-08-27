package health

import "time"

// WithClock freezes the waiter's notion of now, so a test asserting on a
// deadline does not have to wait for one.
func (w *Waiter) WithClock(clock func() time.Time) *Waiter { return w.withClock(clock) }
