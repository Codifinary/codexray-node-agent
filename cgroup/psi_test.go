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

func TestCgroupPSI(t *testing.T) {
	cgRoot = "fixtures/cgroup"
	cg2Root = "fixtures/cgroup"

	cg, _ := NewFromProcessCgroupFile(path.Join("fixtures/proc/400/cgroup"))
	stat := cg.PSI()
	require.NotNil(t, stat)
	assert.Equal(t, float64(465907442)/1e6, stat.CPUSecondsSome)
	assert.Equal(t, float64(463529433)/1e6, stat.CPUSecondsFull)
	assert.Equal(t, float64(6937313991)/1e6, stat.MemorySecondsSome)
	assert.Equal(t, float64(6934649214)/1e6, stat.MemorySecondsFull)
	assert.Equal(t, float64(17657662684)/1e6, stat.IOSecondsSome)
	assert.Equal(t, float64(17636951020)/1e6, stat.IOSecondsFull)

	cg, _ = NewFromProcessCgroupFile(path.Join("fixtures/proc/100/cgroup"))
	assert.Nil(t, cg.PSI())
}

func TestCgroupPSIEdgeCases(t *testing.T) {
	root := t.TempDir()
	cgroupTestSetRoots(t, root, root)
	cg := &Cgroup{subsystems: map[string]string{"": "/app"}}

	// no pressure files (PSI disabled / cgroup removed) -> nil, tolerated
	assert.Nil(t, cg.PSI())

	// older kernels expose only the "some" line for cpu.pressure
	cgroupTestWriteFile(t, root, "app/cpu.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=1500000\n")
	cgroupTestWriteFile(t, root, "app/memory.pressure",
		"some avg10=0.00 avg60=0.00 avg300=0.00 total=2000000\n\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1000000\n")
	assert.Nil(t, cg.PSI(), "io.pressure missing")
	cgroupTestWriteFile(t, root, "app/io.pressure",
		"some total=3000000 avg10=0.00\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\nweird total=5\n")

	s := cg.PSI()
	require.NotNil(t, s)
	assert.Equal(t, PSIStats{
		CPUSecondsSome: 1.5, CPUSecondsFull: 0,
		MemorySecondsSome: 2, MemorySecondsFull: 1,
		IOSecondsSome: 3, IOSecondsFull: 0,
	}, *s)

	cgroupTestWriteFile(t, root, "app/io.pressure", "some avg10=0.00 total=-5\n")
	assert.Nil(t, cg.PSI(), "malformed total")
	_, err := cg.readPressure("io")
	assert.Error(t, err)
}
