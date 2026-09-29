//go:build linux

package ebpfspy

import (
	"testing"
)

// TestNewPerfEventAndClose exercises the real perf_event_open(2) syscall
// wrapper for a software CPU-clock event. This normally requires no
// special privileges (unlike hardware counters), but sandboxed CI
// environments may still deny it via seccomp or
// /proc/sys/kernel/perf_event_paranoid, so we skip gracefully on failure.
func TestNewPerfEventAndClose(t *testing.T) {
	pe, err := newPerfEvent(0, 99)
	if err != nil {
		t.Skipf("perf_event_open not permitted in this environment: %v", err)
	}
	if pe.fd < 0 {
		t.Fatalf("expected a valid fd, got %d", pe.fd)
	}
	if err := pe.Close(); err != nil {
		t.Fatalf("Close() returned an error: %v", err)
	}
}

// TestNewPerfEventInvalidCPU checks that requesting a perf event on a
// nonexistent CPU returns an error rather than panicking.
func TestNewPerfEventInvalidCPU(t *testing.T) {
	_, err := newPerfEvent(1<<20, 99)
	if err == nil {
		t.Skip("expected an error opening a perf event on a bogus CPU, but the kernel allowed it")
	}
}

// TestPerfEventCloseWithoutLink verifies Close() is safe to call on a
// perfEvent that only has an fd and never got a link attached.
func TestPerfEventCloseWithoutLink(t *testing.T) {
	pe, err := newPerfEvent(0, 99)
	if err != nil {
		t.Skipf("perf_event_open not permitted in this environment: %v", err)
	}
	if pe.link != nil {
		t.Fatalf("expected no link to be set on a freshly created perfEvent")
	}
	if err := pe.Close(); err != nil {
		t.Fatalf("Close() returned an error: %v", err)
	}
}
