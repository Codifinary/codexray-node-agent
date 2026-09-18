// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"path/filepath"
	"testing"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadVariablesFromFile(t *testing.T) {
	dir := t.TempDir()
	p := cgroupTestWriteFile(t, dir, "stat", "a 1\nb notanumber\nc -1\nd 2 3\n\ne 18446744073709551615\n")
	vars, err := readVariablesFromFile(p)
	require.NoError(t, err)
	assert.Equal(t, map[string]uint64{"a": 1, "e": 18446744073709551615}, vars)

	_, err = readVariablesFromFile(filepath.Join(dir, "missing"))
	assert.True(t, common.IsNotExist(err))
}
