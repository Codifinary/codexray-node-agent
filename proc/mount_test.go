// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetMountInfoEdgeCases(t *testing.T) {
	dir := procTestRoot(t)
	assert.Nil(t, GetMountInfo(1), "exited process")

	procTestWrite(t, dir, "2/mountinfo",
		"36 35 98:0 /mnt1 /mnt/parent rw,noatime master:1 - ext3 /dev/root rw,errors=continue\n"+
			"37 35 0:44 / /data rw,relatime shared:2 - zfs tank/data rw,xattr\n"+ // zfs keeps anonymous dev
			"38 35 0:45 / /tmp rw - tmpfs tmpfs rw\n"+ // other anonymous devs skipped
			"39 35 0:46 / /nofstype rw\n"+ // no separator -> fs type unknown -> skipped
			"40 35 0:47 / /sep-at-end rw -\n"+
			"short line\n"+
			"\n")
	assert.Equal(t, map[string]MountInfo{
		"36": {MajorMinor: "98:0", MountPoint: "/mnt/parent"},
		"37": {MajorMinor: "0:44", MountPoint: "/data"},
	}, GetMountInfo(2))
}

func TestGetFsTypeFromMountInfo(t *testing.T) {
	assert.Equal(t, "ext4", getFsTypeFromMountInfo([]string{"1", "2", "8:1", "/", "/", "rw", "-", "ext4", "/dev/sda1", "rw"}))
	assert.Equal(t, "xfs", getFsTypeFromMountInfo([]string{"1", "2", "8:1", "/", "/", "rw", "shared:1", "master:2", "-", "xfs", "/dev/sda1", "rw"}))
	assert.Equal(t, "", getFsTypeFromMountInfo([]string{"1", "2", "8:1", "/", "/", "rw"}))
	assert.Equal(t, "", getFsTypeFromMountInfo([]string{"1", "2", "8:1", "/", "/", "rw", "-"}))
	assert.Equal(t, "", getFsTypeFromMountInfo([]string{"1", "2", "8:1", "/", "/", "rw", "shared:1"}))
	assert.Equal(t, "", getFsTypeFromMountInfo(nil))
}
