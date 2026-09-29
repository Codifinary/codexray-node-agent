package dogstatsd

import (
	"testing"
	"time"
)

func TestPressureStateRequiresSustainedUtilizationAndRecovers(t *testing.T) {
	state := newPressureState(0.8, time.Minute)
	now := time.Unix(100, 0)
	if state.Observe(0.9, now) {
		t.Fatal("brief pressure was reported as sustained saturation")
	}
	if state.Observe(0.9, now.Add(59*time.Second)) {
		t.Fatal("pressure became saturated before the configured duration")
	}
	if !state.Observe(0.9, now.Add(time.Minute)) {
		t.Fatal("sustained pressure was not reported as saturation")
	}
	if state.Observe(0.5, now.Add(61*time.Second)) {
		t.Fatal("saturation did not clear after utilization recovered")
	}
	if state.Observe(0.9, now.Add(62*time.Second)) {
		t.Fatal("re-entering pressure reused the previous saturation window")
	}
}
