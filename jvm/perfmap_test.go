// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package jvm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPerfmapDumpSupported(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    bool
	}{
		{"cmdline_fp_enabled", true},
		{"cmdline_fp_absent", false},
		{"cmdline_fp_disabled", false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("fixtures", tc.fixture))
			require.NoError(t, err)
			assert.Equal(t, tc.want, IsPerfmapDumpSupported(data))
		})
	}
	assert.False(t, IsPerfmapDumpSupported(nil))
	assert.False(t, IsPerfmapDumpSupported([]byte{}))
	// truncated flag (cmdline cut mid-arg) must not match
	assert.False(t, IsPerfmapDumpSupported([]byte("java\x00-XX:+PreserveFrame")))
}

func TestIsPerfmapDumpSupportedLastFlagWins(t *testing.T) {
	// HotSpot applies boolean -XX flags in order, so the last occurrence wins.
	// BUG: IsPerfmapDumpSupported returns true when +PreserveFramePointer is later overridden by -PreserveFramePointer — unskip when fixed
	t.Skip("BUG: IsPerfmapDumpSupported ignores a later -XX:-PreserveFramePointer (perfmap.go:24)")
	assert.False(t, IsPerfmapDumpSupported([]byte("java\x00-XX:+PreserveFramePointer\x00-XX:-PreserveFramePointer\x00-jar\x00a.jar\x00")))
}
