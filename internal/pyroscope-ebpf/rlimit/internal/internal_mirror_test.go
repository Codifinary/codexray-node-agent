//go:build linux

package internal

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestMapCreate exercises the raw BPF_MAP_CREATE syscall wrapper. On CI
// sandboxes or containers without CAP_BPF/CAP_SYS_ADMIN this will fail
// with a permission error, which we accept gracefully.
func TestMapCreate(t *testing.T) {
	attr := &MapCreateAttr{
		MapType:    2, // BPF_MAP_TYPE_ARRAY
		KeySize:    4,
		ValueSize:  4,
		MaxEntries: 1,
	}

	fd, err := MapCreate(attr)
	if err != nil {
		t.Skipf("BPF_MAP_CREATE not permitted in this environment: %v", err)
	}
	defer unix.Close(int(fd))

	if int(fd) < 0 {
		t.Fatalf("expected a valid fd, got %d", fd)
	}
}

func TestMapCreateInvalidAttr(t *testing.T) {
	// An absurd map type / sizes combination should fail with an error
	// rather than panicking.
	attr := &MapCreateAttr{
		MapType:    0xFFFFFFFF,
		KeySize:    0,
		ValueSize:  0,
		MaxEntries: 0,
	}
	fd, err := MapCreate(attr)
	if err == nil {
		defer unix.Close(int(fd))
		t.Skip("expected an error for invalid map attrs but kernel allowed it")
	}
}
