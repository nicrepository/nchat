package worker

import "time"

// Test seam for the poll interval.
//
// previewPollInterval is ten seconds in production, which is right for a
// background job and far too long for a test to wait on twice. Overriding it
// here — rather than turning it into a constructor parameter nobody varies in
// production — keeps the wiring honest while letting a test prove that the loop
// really does keep polling, and really does stop when it is cancelled.
func (w *Preview) SetIntervalForTest(interval time.Duration) {
	w.interval = interval
}
