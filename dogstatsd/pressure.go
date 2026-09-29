package dogstatsd

import (
	"sync"
	"time"
)

// pressureState only reports saturation after utilization remains above the
// threshold for the configured duration. Any recovery below the threshold
// resets the observation window.
type pressureState struct {
	mu        sync.Mutex
	threshold float64
	duration  time.Duration
	since     time.Time
}

func newPressureState(threshold float64, duration time.Duration) *pressureState {
	return &pressureState{threshold: threshold, duration: duration}
}

func (p *pressureState) Observe(utilization float64, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if utilization < p.threshold {
		p.since = time.Time{}
		return false
	}
	if p.since.IsZero() {
		p.since = now
	}
	return now.Sub(p.since) >= p.duration
}
