// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCgroup_MemoryStat(t *testing.T) {
	cgRoot = "fixtures/cgroup"
	cg2Root = "fixtures/cgroup"

	cg, _ := NewFromProcessCgroupFile(path.Join("fixtures/proc/100/cgroup"))
	stat := cg.MemoryStat()
	assert.Equal(t, uint64(0), stat.Limit)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/200/cgroup"))
	stat = cg.MemoryStat()
	assert.Equal(t, uint64(14775123968), stat.RSS)
	assert.Equal(t, uint64(3206844416), stat.Cache)
	assert.Equal(t, uint64(21474836480), stat.Limit)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/400/cgroup"))
	stat = cg.MemoryStat()
	assert.Equal(t, uint64(44892160+0), stat.RSS)
	assert.Equal(t, uint64(1044480), stat.Cache)
	assert.Equal(t, uint64(0), stat.Limit)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/500/cgroup"))
	stat = cg.MemoryStat()
	assert.Equal(t, uint64(75247616+4038656), stat.RSS)
	assert.Equal(t, uint64(50835456), stat.Cache)
	assert.Equal(t, uint64(4294967296), stat.Limit)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/550/cgroup"))
	stat = cg.MemoryStat()
	assert.Equal(t, uint64(3637248+2703360), stat.RSS)
	assert.Equal(t, uint64(7299072), stat.Cache)
	assert.Equal(t, uint64(0), stat.Limit)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/1000/cgroup"))
	stat, err := cg.memoryStatV1()
	assert.NoError(t, err)
	assert.Nil(t, stat)

}

func TestCgroupMemoryStatEdgeCases(t *testing.T) {
	root := t.TempDir()
	cgroupTestSetRoots(t, root, root)

	v2 := &Cgroup{subsystems: map[string]string{"": "/app"}}
	assert.Nil(t, v2.MemoryStat(), "missing memory.stat is tolerated")

	cgroupTestWriteFile(t, root, "app/memory.stat", "anon 100\nfile 50\nfile_mapped 7\n")
	// memory.max absent or "max" -> no limit
	s := v2.MemoryStat()
	require.NotNil(t, s)
	assert.Equal(t, MemoryStat{RSS: 107, Cache: 50, Limit: 0}, *s)
	cgroupTestWriteFile(t, root, "app/memory.max", "max\n")
	assert.Equal(t, uint64(0), v2.MemoryStat().Limit)
	cgroupTestWriteFile(t, root, "app/memory.max", "1073741824\n")
	assert.Equal(t, uint64(1073741824), v2.MemoryStat().Limit)

	st, err := (&Cgroup{subsystems: map[string]string{}}).memoryStatV2()
	assert.NoError(t, err)
	assert.Nil(t, st)

	v1 := &Cgroup{subsystems: map[string]string{"memory": "/app"}}
	assert.Nil(t, v1.MemoryStat())
	cgroupTestWriteFile(t, root, "memory/app/memory.stat", "total_rss 10\ntotal_mapped_file 5\ntotal_cache 3\nrss 999\n")
	assert.Nil(t, v1.MemoryStat(), "missing limit file is tolerated")
	// the kernel reports "unlimited" as PAGE_COUNTER_MAX * PAGE_SIZE
	cgroupTestWriteFile(t, root, "memory/app/memory.limit_in_bytes", "9223372036854771712\n")
	s = v1.MemoryStat()
	require.NotNil(t, s)
	assert.Equal(t, MemoryStat{RSS: 15, Cache: 3, Limit: 0}, *s)
	cgroupTestWriteFile(t, root, "memory/app/memory.limit_in_bytes", "536870912\n")
	assert.Equal(t, uint64(536870912), v1.MemoryStat().Limit)
	cgroupTestWriteFile(t, root, "memory/app/memory.limit_in_bytes", "junk\n")
	assert.Nil(t, v1.MemoryStat())
}
