// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCgroup_IOStat(t *testing.T) {
	cgRoot = "fixtures/cgroup"
	cg2Root = "fixtures/cgroup"

	cg, _ := NewFromProcessCgroupFile(path.Join("fixtures/proc/200/cgroup"))
	stat := cg.IOStat()
	assert.Equal(t,
		map[string]IOStat{
			"8:0":  {ReadOps: 0, WriteOps: 281, ReadBytes: 0, WrittenBytes: 4603904},
			"8:16": {ReadOps: 0, WriteOps: 39, ReadBytes: 0, WrittenBytes: 655360},
			"8:32": {ReadOps: 23043666, WriteOps: 28906992, ReadBytes: 998632854016, WrittenBytes: 884175858688},
			"8:48": {ReadOps: 20689345, WriteOps: 27906791, ReadBytes: 875529547776, WrittenBytes: 753046432768},
			"9:1":  {ReadOps: 633949, WriteOps: 4, ReadBytes: 10238894080, WrittenBytes: 49152},
		},
		stat)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/400/cgroup"))
	stat = cg.IOStat()
	assert.Equal(t,
		map[string]IOStat{
			"252:0": {ReadOps: 22, WriteOps: 57111, ReadBytes: 11, WrittenBytes: 630538240},
			"253:0": {ReadOps: 44, WriteOps: 57056, ReadBytes: 33, WrittenBytes: 630538241},
		},
		stat)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/1000/cgroup"))
	stat, err := cg.ioStatV1()
	assert.NoError(t, err)
	assert.Nil(t, stat)
}

func TestCgroupIOStatEdgeCases(t *testing.T) {
	root := t.TempDir()
	cgroupTestSetRoots(t, root, root)

	v2 := &Cgroup{subsystems: map[string]string{"": "/app"}}
	assert.Nil(t, v2.IOStat(), "missing io.stat is tolerated")
	cgroupTestWriteFile(t, root, "app/io.stat",
		"8:0 rbytes=100 wbytes=200 rios=3 wios=4 dbytes=0 dios=0\n"+
			"8:16 rbytes=x wbytes=5 rios=1 wios=2\n"+ // bad value skipped, rest kept
			"8:32 rbytes=1\n"+ // too few fields
			"253:0 rbytes=1 wbytes=2 rios noeq=1\n"+
			"\n")
	assert.Equal(t, map[string]IOStat{
		"8:0":   {ReadOps: 3, WriteOps: 4, ReadBytes: 100, WrittenBytes: 200},
		"8:16":  {ReadOps: 1, WriteOps: 2, ReadBytes: 0, WrittenBytes: 5},
		"253:0": {ReadBytes: 1, WrittenBytes: 2},
	}, v2.IOStat())

	st, err := (&Cgroup{subsystems: map[string]string{}}).ioStatV2()
	assert.NoError(t, err)
	assert.Nil(t, st)

	v1 := &Cgroup{subsystems: map[string]string{"blkio": "/app"}}
	assert.Nil(t, v1.IOStat())
	cgroupTestWriteFile(t, root, "blkio/app/blkio.throttle.io_serviced",
		"8:0 Read 10\n8:0 Write 20\n8:0 Sync 30\n8:0 Total 30\n8:0 Read notanumber\nTotal 30\n")
	assert.Nil(t, v1.IOStat(), "missing io_service_bytes is tolerated")
	cgroupTestWriteFile(t, root, "blkio/app/blkio.throttle.io_service_bytes", "8:0 Read 4096\n8:0 Write 8192\n")
	assert.Equal(t, map[string]IOStat{
		"8:0": {ReadOps: 10, WriteOps: 20, ReadBytes: 4096, WrittenBytes: 8192},
	}, v1.IOStat())
}
