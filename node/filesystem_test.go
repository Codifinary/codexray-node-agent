// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

const mountinfoFixture = `22 1 259:1 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p1 rw
23 22 0:21 / /proc rw,nosuid shared:2 - proc proc rw
24 22 0:22 / /sys rw shared:3 - sysfs sysfs rw
25 22 0:5 / /dev rw shared:4 - devtmpfs devtmpfs rw
26 22 0:23 / /run rw shared:5 - tmpfs tmpfs rw
27 22 259:2 / /var/lib/kubelet rw,relatime shared:6 - xfs /dev/nvme1n1 rw
28 22 259:2 /pods/abc/volumes/x /var/lib/kubelet/pods/abc/volumes/x rw shared:6 - xfs /dev/nvme1n1 rw
29 22 259:2 /containerd /var/lib/containerd rw shared:6 - xfs /dev/nvme1n1 rw
30 22 0:50 / /var/lib/containerd/io.containerd.runtime.v2.task/k8s.io/abc/rootfs rw - overlay overlay rw
31 22 259:3 / /mnt/data\040disk ro,relatime shared:7 - ext4 /dev/nvme2n1 ro
32 22 259:1 /home /home rw,relatime shared:1 - ext4 /dev/nvme0n1p1 rw
33 22 0:60 / /mnt/nfs rw - nfs4 10.0.0.9:/export rw
`

func TestParseAndSelectFilesystems(t *testing.T) {
	mounts := parseMountInfo([]byte(mountinfoFixture))
	require.Len(t, mounts, 12)
	got := selectFilesystems(mounts, regexp.MustCompile(DefaultFSExcludeTypes), regexp.MustCompile(DefaultFSExcludeMountPoints), 64)

	var mps []string
	for _, m := range got {
		mps = append(mps, m.MountPoint)
	}
	// One entry per device: the bind mount /home of the root fs and the kubelet/containerd
	// bind mounts of nvme1n1 are not double counted; virtual and overlay mounts are excluded.
	require.Equal(t, []string{"/", "/mnt/data disk", "/mnt/nfs", "/var/lib/kubelet"}, mps)
	for _, m := range got {
		if m.MountPoint == "/mnt/data disk" {
			require.True(t, m.ReadOnly)
			require.Equal(t, "/dev/nvme2n1", m.Source)
		}
	}
}

func TestSelectFilesystemsCap(t *testing.T) {
	got := selectFilesystems(parseMountInfo([]byte(mountinfoFixture)), regexp.MustCompile(DefaultFSExcludeTypes), regexp.MustCompile(DefaultFSExcludeMountPoints), 2)
	require.Len(t, got, 2)
}

func TestLoadAverageAndFSCollectorOnRealHost(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadavg"), []byte("0.52 1.25 2.00 3/450 12345\n"), 0o600))
	la, err := loadAverage(dir)
	require.NoError(t, err)
	require.Equal(t, [3]float64{0.52, 1.25, 2.0}, la)

	// Smoke test against the test host's own /proc/1 (root filesystem of the test container).
	c := newFSCollector()
	ch := make(chan prometheus.Metric, 1000)
	c.collect(ch)
	close(ch)
	n := 0
	for range ch {
		n++
	}
	require.Equal(t, 0, n%6, "six series per filesystem")
}
