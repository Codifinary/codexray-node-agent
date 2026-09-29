// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"path"
	"testing"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCgroup_CpuStat(t *testing.T) {
	cgRoot = "fixtures/cgroup"
	cg2Root = "fixtures/cgroup"

	cg, _ := NewFromProcessCgroupFile(path.Join("fixtures/proc/100/cgroup"))
	s := cg.CpuStat()
	assert.Equal(t, 0., s.LimitCores)
	assert.Equal(t, 26778.913419246, s.UsageSeconds)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/200/cgroup"))
	s = cg.CpuStat()
	assert.Equal(t, 1.5, s.LimitCores)
	assert.Equal(t, 254005.032764376, s.ThrottledTimeSeconds)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/400/cgroup"))
	s = cg.CpuStat()
	assert.Equal(t, 0.1, s.LimitCores)
	assert.Equal(t, 0.363166, s.ThrottledTimeSeconds)
	assert.Equal(t, 3795.681254, s.UsageSeconds)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/500/cgroup"))
	s = cg.CpuStat()
	assert.Equal(t, 0., s.LimitCores)
	assert.Equal(t, 0., s.ThrottledTimeSeconds)
	assert.Equal(t, 5531.521992, s.UsageSeconds)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/1000/cgroup"))
	s, err := cg.cpuStatV1()
	assert.NoError(t, err)
	assert.Nil(t, s)

	cg2Root = "fixtures/cgroup/unified"
	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/550/cgroup"))
	s = cg.CpuStat()
	assert.Equal(t, 0., s.LimitCores)
	assert.Equal(t, 0., s.ThrottledTimeSeconds)
	assert.Equal(t, 151.439967, s.UsageSeconds)
}

func TestCgroupCpuStatV2EdgeCases(t *testing.T) {
	root := t.TempDir()
	cgroupTestSetRoots(t, root, root)
	cg := &Cgroup{subsystems: map[string]string{"": "/app"}}

	// process/cgroup gone -> nil, tolerated
	assert.Nil(t, cg.CpuStat())
	_, err := cg.cpuStatV2()
	assert.True(t, common.IsNotExist(err))

	cgroupTestWriteFile(t, root, "app/cpu.stat", "usage_usec 2500000\nuser_usec 1\nthrottled_usec 500000\nbogus notanumber\n")

	// no cpu.max (root cgroup) -> usage without limit
	s := cg.CpuStat()
	require.NotNil(t, s)
	assert.Equal(t, 2.5, s.UsageSeconds)
	assert.Equal(t, 0.5, s.ThrottledTimeSeconds)
	assert.Equal(t, 0., s.LimitCores)

	cases := []struct {
		cpuMax  string
		limit   float64
		wantErr bool
	}{
		{"max 100000\n", 0, false},
		{"250000 100000\n", 2.5, false},
		{"50000 100000", 0.5, false},
		{"50000 0\n", 0, false},
		{"50000\n", 0, true},
		{"1 2 3\n", 0, true},
		{"abc 100000\n", 0, true},
		{"50000 abc\n", 0, true},
		{"\n", 0, true},
	}
	for _, c := range cases {
		cgroupTestWriteFile(t, root, "app/cpu.max", c.cpuMax)
		st, err := cg.cpuStatV2()
		if c.wantErr {
			assert.Error(t, err, c.cpuMax)
			assert.Nil(t, cg.CpuStat(), c.cpuMax)
			continue
		}
		require.NoError(t, err, c.cpuMax)
		assert.Equal(t, c.limit, st.LimitCores, c.cpuMax)
	}

	// no v2 path at all
	st, err := (&Cgroup{subsystems: map[string]string{}}).cpuStatV2()
	assert.NoError(t, err)
	assert.Nil(t, st)
}

func TestCgroupCpuStatV1MissingFiles(t *testing.T) {
	root := t.TempDir()
	cgroupTestSetRoots(t, root, root)
	cg := &Cgroup{subsystems: map[string]string{"cpu": "/app", "cpuacct": "/app"}}

	steps := []struct{ rel, content string }{
		{"cpu/app/cpu.stat", "nr_periods 10\nthrottled_time 2000000000\n"},
		{"cpuacct/app/cpuacct.usage", "3000000000\n"},
		{"cpu/app/cpu.cfs_period_us", "100000\n"},
		{"cpu/app/cpu.cfs_quota_us", "-1\n"},
	}
	for _, s := range steps {
		// every file missing along the way (process exited mid-read) -> nil, no panic
		assert.Nil(t, cg.CpuStat(), "before "+s.rel)
		_, err := cg.cpuStatV1()
		assert.True(t, common.IsNotExist(err))
		cgroupTestWriteFile(t, root, s.rel, s.content)
	}
	s := cg.CpuStat()
	require.NotNil(t, s)
	assert.Equal(t, 3., s.UsageSeconds)
	assert.Equal(t, 2., s.ThrottledTimeSeconds)
	assert.Equal(t, 0., s.LimitCores, "quota -1 means unlimited")

	cgroupTestWriteFile(t, root, "cpu/app/cpu.cfs_quota_us", "garbage\n")
	assert.Nil(t, cg.CpuStat())
}
