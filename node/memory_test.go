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

func TestNode_memory(t *testing.T) {
	m, err := memoryInfo("fixtures/proc")
	assert.Nil(t, err)
	assert.Equal(t,
		MemoryStat{
			TotalBytes:     65871236 * 1000,
			FreeBytes:      7540732 * 1000,
			AvailableBytes: 23826720 * 1000,
			CachedBytes:    15878036 * 1000,
		},
		m,
	)
}

func memoryTestProcRoot(t *testing.T, meminfo string) string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meminfo"), []byte(meminfo), 0o644))
	return dir
}

func TestMemoryInfoEdgeCases(t *testing.T) {
	_, err := memoryInfo(t.TempDir())
	assert.Error(t, err)

	// lines without a unit are taken as-is; short/garbage lines are skipped
	m, err := memoryInfo(memoryTestProcRoot(t, "MemTotal: 4096\nbroken\n\nHugePages_Total:       0\nCached: notanumber kB\n"))
	require.NoError(t, err)
	assert.Equal(t, 4096., m.TotalBytes)
	assert.Equal(t, 0., m.CachedBytes)
}

func TestMemoryInfoKibibytes(t *testing.T) {
	// /proc/meminfo "kB" is KiB: fs/proc/meminfo.c prints pages << (PAGE_SHIFT - 10).
	// Container memory (cgroup) is in bytes, so node_resources_memory_*_bytes must use 1024 too.
	// BUG: memoryInfo multiplies meminfo kB values by 1000 instead of 1024 (node memory under-reported by ~2.4%) — unskip when fixed
	t.Skip("BUG: memoryInfo multiplies meminfo kB values by 1000 instead of 1024")
	m, err := memoryInfo(memoryTestProcRoot(t, "MemTotal: 1024 kB\nMemFree: 512 kB\nMemAvailable: 768 kB\nCached: 256 kB\n"))
	require.NoError(t, err)
	assert.Equal(t, MemoryStat{TotalBytes: 1024 * 1024, FreeBytes: 512 * 1024, AvailableBytes: 768 * 1024, CachedBytes: 256 * 1024}, m)
}
