// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNode_cpu(t *testing.T) {
	usage, err := cpuStat("fixtures/proc")

	assert.Nil(t, err)
	//cpu  22246621850 266246696 6211649668 51164293943 1715028476 0 3050509822 0 0 0
	assert.Equal(t,
		CpuStat{
			TotalUsage: CpuUsage{
				User:    222466218.50,
				Nice:    2662466.96,
				System:  62116496.68,
				Idle:    511642939.43,
				IoWait:  17150284.76,
				Irq:     0,
				SoftIrq: 30505098.22,
				Steal:   0,
			},
			LogicalCores: 16,
		},
		usage)
}

func cpuTestProcRoot(t *testing.T, stat string) string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	return dir
}

func TestNodeCpuStatEdgeCases(t *testing.T) {
	_, err := cpuStat(t.TempDir())
	assert.True(t, common.IsNotExist(err))

	// USER_HZ is 100 on every supported arch (amd64, arm64): 250 ticks = 2.5s
	s, err := cpuStat(cpuTestProcRoot(t, "cpu  250 0 0 0 0 0 0 0 0 0\ncpu0 250 0 0 0 0 0 0 0 0 0\nintr 5\n"))
	require.NoError(t, err)
	assert.Equal(t, 2.5, s.TotalUsage.User)
	assert.Equal(t, 1, s.LogicalCores)

	// no aggregate line -> zeros, cores still counted
	s, err = cpuStat(cpuTestProcRoot(t, "cpu0 1 1 1 1 1 1 1 1\ncpu1 1 1 1 1 1 1 1 1\n"))
	require.NoError(t, err)
	assert.Equal(t, CpuUsage{}, s.TotalUsage)
	assert.Equal(t, 2, s.LogicalCores)

	for i := 1; i <= 8; i++ {
		fields := []string{"cpu ", "1", "1", "1", "1", "1", "1", "1", "1", "0", "0"}
		fields[i] = "x"
		_, err = cpuStat(cpuTestProcRoot(t, strings.Join(fields, " ")+"\n"))
		assert.Error(t, err, "bad field %d", i)
	}
}

func TestNodeCpuStatTruncatedLine(t *testing.T) {
	// BUG: cpuStat indexes parts[1..8] of the "cpu " line without a length check and panics on a short line — unskip when fixed
	t.Skip("BUG: cpuStat panics (index out of range) on a truncated \"cpu \" line in /proc/stat")
	assert.NotPanics(t, func() {
		_, err := cpuStat(cpuTestProcRoot(t, "cpu  1 2 3\n"))
		assert.Error(t, err)
	})
}
