// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatFS(t *testing.T) {
	s, err := StatFS(t.TempDir())
	require.NoError(t, err)
	assert.Greater(t, s.CapacityBytes, uint64(0))
	assert.LessOrEqual(t, s.UsedBytes, s.CapacityBytes)
	assert.LessOrEqual(t, s.ReservedBytes, s.CapacityBytes)

	_, err = StatFS(filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)
}
