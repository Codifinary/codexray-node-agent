// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetNodeDisks(t *testing.T) {
	procRoot = "fixtures"
	d, err := GetDisks()
	assert.Nil(t, err)
	assert.Equal(t,
		DevStat{
			Name:             "vda",
			MajorMinor:       "254:0",
			ReadOps:          10.,
			WriteOps:         50.,
			BytesRead:        30. * 512,
			BytesWritten:     70. * 512,
			ReadTimeSeconds:  40. / 1000,
			WriteTimeSeconds: 80. / 1000,
			IoTimeSeconds:    100. / 1000,
		},
		*d.GetParentBlockDevice("254:0"),
	)
	assert.Equal(t,
		DevStat{
			Name:             "nvme0n1",
			MajorMinor:       "259:0",
			ReadOps:          11146,
			WriteOps:         2.3639172e+07,
			BytesRead:        3.60193536e+08,
			BytesWritten:     3.80286784512e+11,
			ReadTimeSeconds:  1.614,
			WriteTimeSeconds: 5380.297,
			IoTimeSeconds:    26059.968},
		*d.GetParentBlockDevice("259:4"),
	)
	names := func(devices []DevStat) []string {
		var res []string
		for _, d := range devices {
			res = append(res, d.Name)
		}
		sort.Strings(res)
		return res
	}

	assert.Equal(t,
		[]string{"dm-0", "md1", "mmcblk1", "mmcblk2", "nvme0n1", "nvme1n1", "rbd0", "rbd1", "sda", "sdb", "vda", "xvda"},
		names(d.BlockDevices()),
	)
}

func TestGetDisksEdgeCases(t *testing.T) {
	dir := t.TempDir()
	prev := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = prev })

	_, err := GetDisks()
	assert.Error(t, err, "missing diskstats")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "diskstats"), []byte(
		"   8 0 sda 1 0 2 3 4 0 5 6 0 7 0\n"+ // 14 fields (pre-4.18 kernels)
			"   8 1 sda1 1 0 2 3 4 0 5 6 0 7 0\n"+
			"   8 16 sdb 1 0 2 3\n"+ // truncated -> skipped
			"   8 32 sdc x 0 2 3 4 0 5 6 0 7 0\n"+ // bad number -> skipped
			" 253 0 dm-0 1 0 1 1 1 0 1 1 0 1 0\n"+
			" 259 1 nvme0n1p1 1 0 1 1 1 0 1 1 0 1 0\n"+
			"\n"), 0o644))
	d, err := GetDisks()
	require.NoError(t, err)

	var names []string
	for _, dev := range d.BlockDevices() {
		names = append(names, dev.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"dm-0", "sda"}, names)

	sda := d.GetParentBlockDevice("8:1")
	require.NotNil(t, sda)
	assert.Equal(t, "sda", sda.Name)
	assert.Equal(t, 2.*512, sda.BytesRead)
	assert.Equal(t, 0.007, sda.IoTimeSeconds)

	assert.Nil(t, d.GetParentBlockDevice("9:9"), "unknown device")
	assert.Nil(t, d.GetParentBlockDevice("8:16"), "skipped line")
	assert.Nil(t, d.GetParentBlockDevice("259:1"), "partition whose parent is absent")
	assert.Equal(t, "dm-0", d.GetParentBlockDevice("253:0").Name)
}

func TestGetParentBlockDeviceNonBlock(t *testing.T) {
	d := &Disks{byMajorMinor: map[string]DevStat{"7:0": {Name: "loop0", MajorMinor: "7:0"}}}
	assert.Nil(t, d.GetParentBlockDevice("7:0"))
	assert.Empty(t, d.BlockDevices())
}

func TestParseFloats(t *testing.T) {
	v, err := parseFloats([]string{"1", "2.5", "0"})
	require.NoError(t, err)
	assert.Equal(t, []float64{1, 2.5, 0}, v)
	_, err = parseFloats([]string{"1", "x"})
	assert.Error(t, err)
	v, err = parseFloats(nil)
	require.NoError(t, err)
	assert.Empty(t, v)
}
