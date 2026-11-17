// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckCgroupRoot(t *testing.T) {
	assert.Error(t, checkCgroupRoot("/nonexistent"))
	empty := t.TempDir()
	assert.Error(t, checkCgroupRoot(empty))

	v2 := t.TempDir()
	assert.NoError(t, os.WriteFile(path.Join(v2, "cgroup.controllers"), []byte("cpu memory"), 0o644))
	assert.NoError(t, checkCgroupRoot(v2))

	v1 := t.TempDir()
	assert.NoError(t, os.Mkdir(path.Join(v1, "memory"), 0o755))
	assert.NoError(t, checkCgroupRoot(v1))
}
