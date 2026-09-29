//go:build linux

package rlimit

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestRemoveMemlock exercises RemoveMemlock against the real kernel. On
// modern kernels (5.11+) with memcg-based accounting, this should be a
// no-op; on older kernels it requires CAP_SYS_RESOURCE to raise the limit
// to infinity, so we accept either a nil error or a permission error.
func TestRemoveMemlock(t *testing.T) {
	err := RemoveMemlock()
	if err != nil {
		t.Logf("RemoveMemlock returned an error (expected without CAP_SYS_RESOURCE on old kernels): %v", err)
	}

	// Calling it a second time should hit the sync.Once cached path and
	// behave the same way.
	err2 := RemoveMemlock()
	if (err == nil) != (err2 == nil) {
		t.Fatalf("RemoveMemlock behaved inconsistently across calls: first=%v second=%v", err, err2)
	}
}

func TestDetectMemcgAccounting(t *testing.T) {
	err := detectMemcgAccounting()
	// The function either succeeds (memcg accounting supported), returns
	// unsupportedMemcgAccounting, or fails for other environment reasons
	// (e.g. sandboxed test runners without BPF access). We just verify it
	// doesn't panic and returns some value we can reason about.
	t.Logf("detectMemcgAccounting: %v", err)
}

func TestRlimitRaw(t *testing.T) {
	var limit unix.Rlimit
	if err := unix.Prlimit(0, unix.RLIMIT_MEMLOCK, nil, &limit); err != nil {
		t.Skipf("cannot read RLIMIT_MEMLOCK in this environment: %v", err)
	}
	t.Logf("current memlock rlimit: cur=%d max=%d", limit.Cur, limit.Max)
}
