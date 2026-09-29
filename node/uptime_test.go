// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNode_uptime(t *testing.T) {
	v, err := uptime("fixtures/proc")
	assert.Nil(t, err)
	assert.Equal(t, 2659150.03, v)
}

func TestNodeUptimeEdgeCases(t *testing.T) {
	dir := t.TempDir()
	_, err := uptime(dir)
	assert.Error(t, err)

	for _, bad := range []string{"", "123.45\n", "1 2 3\n", "abc 1.0\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "uptime"), []byte(bad), 0o644))
		_, err = uptime(dir)
		assert.Error(t, err, "%q", bad)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uptime"), []byte("350735.47 234388.90\n"), 0o644))
	v, err := uptime(dir)
	require.NoError(t, err)
	assert.Equal(t, 350735.47, v)
}
