// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotExist(t *testing.T) {
	_, err := os.ReadFile(filepath.Join(t.TempDir(), "missing"))
	assert.True(t, IsNotExist(err))

	// a process that exited mid-read surfaces as ESRCH
	assert.True(t, IsNotExist(&os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.ESRCH}))
	assert.True(t, IsNotExist(errors.New("open /proc/123/fd: no such file or directory")))

	assert.False(t, IsNotExist(errors.New("permission denied")))
	assert.False(t, IsNotExist(&os.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}))
}
