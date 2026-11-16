// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

// Same names and labels as node-exporter, which the backend already queries.
var fsLabels = []string{"mountpoint", "device", "fstype"}

var (
	fsSizeDesc      = prometheus.NewDesc("node_filesystem_size_bytes", "Filesystem size in bytes", fsLabels, nil)
	fsAvailDesc     = prometheus.NewDesc("node_filesystem_avail_bytes", "Filesystem space available to non-root users in bytes", fsLabels, nil)
	fsFreeDesc      = prometheus.NewDesc("node_filesystem_free_bytes", "Filesystem free space in bytes (including space reserved for root)", fsLabels, nil)
	fsFilesDesc     = prometheus.NewDesc("node_filesystem_files", "Filesystem total inodes", fsLabels, nil)
	fsFilesFreeDesc = prometheus.NewDesc("node_filesystem_files_free", "Filesystem free inodes", fsLabels, nil)
	fsReadonlyDesc  = prometheus.NewDesc("node_filesystem_readonly", "1 if the filesystem is mounted read-only", fsLabels, nil)

	load1Desc  = prometheus.NewDesc("node_load1", "1m load average", nil, nil)
	load5Desc  = prometheus.NewDesc("node_load5", "5m load average", nil, nil)
	load15Desc = prometheus.NewDesc("node_load15", "15m load average", nil, nil)
)

// DefaultFSExcludeTypes are virtual, pseudo or per-container filesystems: they have no node
// capacity of their own or would be double counted (overlay roots of every container).
const DefaultFSExcludeTypes = `^(autofs|binfmt_misc|bpf|cgroup2?|configfs|debugfs|devpts|devtmpfs|efivarfs|fusectl|fuse\.lxcfs|hugetlbfs|mqueue|nsfs|overlay|proc|procfs|pstore|ramfs|rpc_pipefs|securityfs|selinuxfs|shm|squashfs|sysfs|tmpfs|tracefs|nfsd|erofs|iso9660)$`

// DefaultFSExcludeMountPoints are kernel/runtime mount points and per-pod volume mounts
// (pod volumes are reported per container by the container metrics and by kubelet volume stats).
const DefaultFSExcludeMountPoints = `^/(dev|proc|sys|run|var/lib/kubelet/pods|var/lib/docker/.+|var/lib/containerd/.+|run/containerd/.+|var/lib/containers/storage/.+|snap/.+)($|/)`

// mountInfo is a parsed mountinfo line; parsing is shared with container mount attribution.
type mountInfo = proc.MountEntry

func parseMountInfo(data []byte) []mountInfo { return proc.ParseMountInfo(data) }

// selectFilesystems keeps one mount per device (major:minor) so bind mounts and repeated
// mounts of the same filesystem are counted once. It prefers the mount of the filesystem
// root (root == "/"), then the shortest mount point. Results are sorted by mount point and
// capped at max.
func selectFilesystems(mounts []mountInfo, excludeTypes, excludeMounts *regexp.Regexp, max int) []mountInfo {
	byDev := map[string]mountInfo{}
	for _, m := range mounts {
		if excludeTypes != nil && excludeTypes.MatchString(m.FsType) {
			continue
		}
		if excludeMounts != nil && excludeMounts.MatchString(m.MountPoint) {
			continue
		}
		cur, ok := byDev[m.MajorMinor]
		if !ok || better(m, cur) {
			byDev[m.MajorMinor] = m
		}
	}
	res := make([]mountInfo, 0, len(byDev))
	for _, m := range byDev {
		res = append(res, m)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].MountPoint < res[j].MountPoint })
	if max > 0 && len(res) > max {
		res = res[:max]
	}
	return res
}

func better(a, b mountInfo) bool {
	if (a.Root == "/") != (b.Root == "/") {
		return a.Root == "/"
	}
	if len(a.MountPoint) != len(b.MountPoint) {
		return len(a.MountPoint) < len(b.MountPoint)
	}
	return a.MountPoint < b.MountPoint
}

type fsCollector struct {
	excludeTypes, excludeMounts *regexp.Regexp
	max                         int
	statTimeout                 time.Duration

	mu    sync.Mutex
	stuck map[string]time.Time // mount point -> until; statfs hung (e.g. unreachable NFS)
}

func newFSCollector() *fsCollector {
	c := &fsCollector{max: 64, statTimeout: 2 * time.Second, stuck: map[string]time.Time{}}
	types, mounts := DefaultFSExcludeTypes, DefaultFSExcludeMountPoints
	if flags.NodeFSExcludeTypes != nil && *flags.NodeFSExcludeTypes != "" {
		types = *flags.NodeFSExcludeTypes
	}
	if flags.NodeFSExcludeMountPoints != nil && *flags.NodeFSExcludeMountPoints != "" {
		mounts = *flags.NodeFSExcludeMountPoints
	}
	if flags.NodeFSMaxMounts != nil && *flags.NodeFSMaxMounts > 0 {
		c.max = *flags.NodeFSMaxMounts
	}
	var err error
	if c.excludeTypes, err = regexp.Compile(types); err != nil {
		klog.Errorf("invalid filesystem type exclusion %q: %s; using default", types, err)
		c.excludeTypes = regexp.MustCompile(DefaultFSExcludeTypes)
	}
	if c.excludeMounts, err = regexp.Compile(mounts); err != nil {
		klog.Errorf("invalid mount point exclusion %q: %s; using default", mounts, err)
		c.excludeMounts = regexp.MustCompile(DefaultFSExcludeMountPoints)
	}
	return c
}

// statfs runs unix.Statfs with a timeout. A mount whose statfs hangs is skipped for a few
// minutes instead of spawning a new blocked goroutine on every scrape.
func (c *fsCollector) statfs(p string) (*unix.Statfs_t, bool) {
	c.mu.Lock()
	if until, ok := c.stuck[p]; ok && time.Now().Before(until) {
		c.mu.Unlock()
		return nil, false
	}
	c.mu.Unlock()
	res := make(chan *unix.Statfs_t, 1)
	go func() {
		var st unix.Statfs_t
		if err := unix.Statfs(p, &st); err != nil {
			res <- nil
			return
		}
		res <- &st
	}()
	select {
	case st := <-res:
		return st, st != nil
	case <-time.After(c.statTimeout):
		klog.Warningf("statfs %s timed out, skipping it for 5m", p)
		c.mu.Lock()
		c.stuck[p] = time.Now().Add(5 * time.Minute)
		c.mu.Unlock()
		return nil, false
	}
}

func (c *fsCollector) collect(ch chan<- prometheus.Metric) {
	data, err := os.ReadFile(path.Join(procRoot, "1", "mountinfo"))
	if err != nil {
		klog.Warningln("failed to read host mountinfo:", err)
		return
	}
	for _, m := range selectFilesystems(parseMountInfo(data), c.excludeTypes, c.excludeMounts, c.max) {
		st, ok := c.statfs(path.Join(procRoot, "1", "root", m.MountPoint))
		if !ok {
			continue
		}
		bsize := float64(st.Bsize)
		lv := []string{m.MountPoint, m.Source, m.FsType}
		ch <- gauge(fsSizeDesc, float64(st.Blocks)*bsize, lv...)
		ch <- gauge(fsAvailDesc, float64(st.Bavail)*bsize, lv...)
		ch <- gauge(fsFreeDesc, float64(st.Bfree)*bsize, lv...)
		ch <- gauge(fsFilesDesc, float64(st.Files), lv...)
		ch <- gauge(fsFilesFreeDesc, float64(st.Ffree), lv...)
		ro := 0.0
		if m.ReadOnly || st.Flags&unix.ST_RDONLY != 0 {
			ro = 1
		}
		ch <- gauge(fsReadonlyDesc, ro, lv...)
	}
}

func loadAverage(procRoot string) ([3]float64, error) {
	var res [3]float64
	data, err := os.ReadFile(path.Join(procRoot, "loadavg"))
	if err != nil {
		return res, err
	}
	f := strings.Fields(string(data))
	for i := 0; i < 3 && i < len(f); i++ {
		res[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return res, nil
}
