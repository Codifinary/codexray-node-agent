// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fileTestWrite(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestReadIntFromFile(t *testing.T) {
	v, err := ReadIntFromFile(fileTestWrite(t, "-1\n"))
	require.NoError(t, err)
	assert.Equal(t, int64(-1), v)

	v, err = ReadIntFromFile(fileTestWrite(t, "  100000 \n"))
	require.NoError(t, err)
	assert.Equal(t, int64(100000), v)

	_, err = ReadIntFromFile(fileTestWrite(t, "max\n"))
	assert.Error(t, err)
	_, err = ReadIntFromFile(fileTestWrite(t, ""))
	assert.Error(t, err)

	_, err = ReadIntFromFile(filepath.Join(t.TempDir(), "missing"))
	assert.True(t, IsNotExist(err))
}

func TestReadUintFromFile(t *testing.T) {
	v, err := ReadUintFromFile(fileTestWrite(t, "9223372036854771712\n"))
	require.NoError(t, err)
	assert.Equal(t, uint64(9223372036854771712), v)

	_, err = ReadUintFromFile(fileTestWrite(t, "-1\n"))
	assert.Error(t, err)
	_, err = ReadUintFromFile(fileTestWrite(t, "max\n"))
	assert.Error(t, err)

	_, err = ReadUintFromFile(filepath.Join(t.TempDir(), "missing"))
	assert.True(t, IsNotExist(err))
}
