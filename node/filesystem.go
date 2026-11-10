// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

var fsLabels = []string{"mount_point", "device", "fstype"}

var (
	fsSizeDesc      = prometheus.NewDesc("node_filesystem_size_bytes", "Filesystem size in bytes", fsLabels, nil)
	fsAvailDesc     = prometheus.NewDesc("node_filesystem_avail_bytes", "Filesystem space available to non-root users in bytes", fsLabels, nil)
	fsFreeDesc      = prometheus.NewDesc("node_filesystem_free_bytes", "Filesystem free space in bytes (including space reserved for root)", fsLabels, nil)
	fsFilesDesc     = prometheus.NewDesc("node_filesystem_files", "Filesystem total inodes", fsLabels, nil)
	fsFilesFreeDesc = prometheus.NewDesc("node_filesystem_files_free", "Filesystem free inodes", fsLabels, nil)
	fsReadonlyDesc  = prometheus.NewDesc("node_filesystem_readonly", "1 if the filesystem is mounted read-only", fsLabels, nil)

	loadDesc = prometheus.NewDesc("node_load_average", "System load average", []string{"window"}, nil)
)

// DefaultFSExcludeTypes are virtual, pseudo or per-container filesystems: they have no node
// capacity of their own or would be double counted (overlay roots of every container).
const DefaultFSExcludeTypes = `^(autofs|binfmt_misc|bpf|cgroup2?|configfs|debugfs|devpts|devtmpfs|efivarfs|fusectl|fuse\.lxcfs|hugetlbfs|mqueue|nsfs|overlay|proc|procfs|pstore|ramfs|rpc_pipefs|securityfs|selinuxfs|shm|squashfs|sysfs|tmpfs|tracefs|nfsd|erofs|iso9660)$`

// DefaultFSExcludeMountPoints are kernel/runtime mount points and per-pod volume mounts
// (pod volumes are reported per container by the container metrics and by kubelet volume stats).
const DefaultFSExcludeMountPoints = `^/(dev|proc|sys|run|var/lib/kubelet/pods|var/lib/docker/.+|var/lib/containerd/.+|run/containerd/.+|var/lib/containers/storage/.+|snap/.+)($|/)`

type mountInfo struct {
	majorMinor string
	root       string
	mountPoint string
	readOnly   bool
	fsType     string
	source     string
}

// parseMountInfo parses /proc/<pid>/mountinfo (see proc(5)).
func parseMountInfo(data []byte) []mountInfo {
	var res []mountInfo
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(fields) < sep+3 {
			continue
		}
		m := mountInfo{
			majorMinor: fields[2],
			root:       unescapeMount(fields[3]),
			mountPoint: unescapeMount(fields[4]),
			fsType:     fields[sep+1],
			source:     fields[sep+2],
		}
		for _, o := range strings.Split(fields[5], ",") {
			if o == "ro" {
				m.readOnly = true
			}
		}
		res = append(res, m)
	}
	return res
}

// unescapeMount decodes the octal escapes used by the kernel for spaces, tabs, newlines and backslashes.
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

// selectFilesystems keeps one mount per device (major:minor) so bind mounts and repeated
// mounts of the same filesystem are counted once. It prefers the mount of the filesystem
// root (root == "/"), then the shortest mount point. Results are sorted by mount point and
// capped at max.
func selectFilesystems(mounts []mountInfo, excludeTypes, excludeMounts *regexp.Regexp, max int) []mountInfo {
	byDev := map[string]mountInfo{}
	for _, m := range mounts {
		if excludeTypes != nil && excludeTypes.MatchString(m.fsType) {
			continue
		}
		if excludeMounts != nil && excludeMounts.MatchString(m.mountPoint) {
			continue
		}
		cur, ok := byDev[m.majorMinor]
		if !ok || better(m, cur) {
			byDev[m.majorMinor] = m
		}
	}
	res := make([]mountInfo, 0, len(byDev))
	for _, m := range byDev {
		res = append(res, m)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].mountPoint < res[j].mountPoint })
	if max > 0 && len(res) > max {
		res = res[:max]
	}
	return res
}

func better(a, b mountInfo) bool {
	if (a.root == "/") != (b.root == "/") {
		return a.root == "/"
	}
	if len(a.mountPoint) != len(b.mountPoint) {
		return len(a.mountPoint) < len(b.mountPoint)
	}
	return a.mountPoint < b.mountPoint
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
		st, ok := c.statfs(path.Join(procRoot, "1", "root", m.mountPoint))
		if !ok {
			continue
		}
		bsize := float64(st.Bsize)
		lv := []string{m.mountPoint, m.source, m.fsType}
		ch <- gauge(fsSizeDesc, float64(st.Blocks)*bsize, lv...)
		ch <- gauge(fsAvailDesc, float64(st.Bavail)*bsize, lv...)
		ch <- gauge(fsFreeDesc, float64(st.Bfree)*bsize, lv...)
		ch <- gauge(fsFilesDesc, float64(st.Files), lv...)
		ch <- gauge(fsFilesFreeDesc, float64(st.Ffree), lv...)
		ro := 0.0
		if m.readOnly || st.Flags&unix.ST_RDONLY != 0 {
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
