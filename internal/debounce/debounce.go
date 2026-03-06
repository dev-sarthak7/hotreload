// Package debounce provides a debouncer that collapses rapid events
// into a single trigger after a quiet period.
package debounce

import (
	"context"
	"time"

	"github.com/dev-sarthak7/hotreload/internal/watcher"
)

// Debouncer collapses rapid change events.
type Debouncer struct {
	delay time.Duration
}

// New creates a Debouncer with the given delay in milliseconds.
func New(delayMs int) *Debouncer {
	return &Debouncer{delay: time.Duration(delayMs) * time.Millisecond}
}

// Wrap takes an input change channel and returns an output channel that only
// fires once per burst of changes, after the burst ends and the delay elapses.
func (d *Debouncer) Wrap(ctx context.Context, in <-chan watcher.ChangedFile) <-chan struct{} {
	out := make(chan struct{}, 1)

	go func() {
		defer close(out)

		var timer *time.Timer
		var timerC <-chan time.Time

		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return

			case _, ok := <-in:
				if !ok {
					// Input channel closed
					if timer != nil {
						timer.Stop()
					}
					return
				}
				// Reset (or start) the timer on every incoming event
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(d.delay)
				timerC = timer.C

			case <-timerC:
				// Quiet period elapsed — fire exactly one signal
				// Non-blocking: if the consumer hasn't picked up the last one yet,
				// we don't queue another (they'll get the latest build anyway).
				select {
				case out <- struct{}{}:
				default:
				}
				timerC = nil
				timer = nil
			}
		}
	}()

	return out
}
