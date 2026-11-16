// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

type MountInfo struct {
	MajorMinor string
	MountPoint string
}

// MountEntry is one line of /proc/<pid>/mountinfo (see proc(5)), with octal escapes in paths
// decoded. It is the single mountinfo parser for container mount attribution and node
// filesystem metrics.
type MountEntry struct {
	ID         string
	MajorMinor string
	Root       string
	MountPoint string
	ReadOnly   bool
	FsType     string
	Source     string
}

// ParseMountInfo parses the contents of /proc/<pid>/mountinfo. Malformed lines are skipped.
func ParseMountInfo(data []byte) []MountEntry {
	var res []MountEntry
	for _, line := range strings.Split(string(data), "\n") {
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		sep := slices.Index(fields[6:], "-")
		if sep == -1 {
			continue
		}
		sep += 6
		if len(fields) < sep+3 {
			continue
		}
		e := MountEntry{
			ID:         fields[0],
			MajorMinor: fields[2],
			Root:       unescapeMount(fields[3]),
			MountPoint: unescapeMount(fields[4]),
			FsType:     fields[sep+1],
			Source:     fields[sep+2],
		}
		for _, o := range strings.Split(fields[5], ",") {
			if o == "ro" {
				e.ReadOnly = true
			}
		}
		res = append(res, e)
	}
	return res
}

// unescapeMount decodes the octal escapes the kernel uses for spaces, tabs, newlines and backslashes.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func GetMountInfo(pid uint32) map[string]MountInfo {
	data, err := os.ReadFile(Path(pid, "mountinfo"))
	if err != nil {
		return nil
	}
	res := map[string]MountInfo{}
	for _, e := range ParseMountInfo(data) {
		// Virtual filesystems (major 0) have no block device, except ZFS datasets.
		if strings.HasPrefix(e.MajorMinor, "0:") && e.FsType != "zfs" {
			continue
		}
		res[e.ID] = MountInfo{MajorMinor: e.MajorMinor, MountPoint: e.MountPoint}
	}
	return res
}
