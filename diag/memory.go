// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package diag

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
)

// selfCgroupDir is where the agent's own cgroup is visible. With cgroup namespaces (the
// default for containers on cgroup v2) the container sees its own cgroup at /sys/fs/cgroup.
var selfCgroupDir = "/sys/fs/cgroup"

// CgroupMemory returns the agent container's memory usage and limit as charged by the
// kernel. This is what the OOM killer acts on: it includes the process RSS plus kernel
// memory charged to the cgroup (page cache, tmpfs/emptyDir medium=Memory files, socket
// buffers, BPF maps on kernels that account them), which process RSS does not show.
// limit is 0 when unlimited or unknown.
func CgroupMemory() (usage, limit uint64, ok bool) {
	// cgroup v2
	if u, err := readUint(filepath.Join(selfCgroupDir, "memory.current")); err == nil {
		l, _ := readUint(filepath.Join(selfCgroupDir, "memory.max")) // "max" -> error -> 0
		return u, l, true
	}
	// cgroup v1
	if u, err := readUint(filepath.Join(selfCgroupDir, "memory", "memory.usage_in_bytes")); err == nil {
		l, _ := readUint(filepath.Join(selfCgroupDir, "memory", "memory.limit_in_bytes"))
		if l >= 1<<62 { // "unlimited" on v1 is a huge page-aligned number
			l = 0
		}
		return u, l, true
	}
	return 0, 0, false
}

// CgroupMemoryStat returns selected fields of memory.stat (bytes), used to attribute
// cgroup memory: anon, file, kernel, sock, shmem, ...
func CgroupMemoryStat() map[string]uint64 {
	for _, p := range []string{filepath.Join(selfCgroupDir, "memory.stat"), filepath.Join(selfCgroupDir, "memory", "memory.stat")} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		res := map[string]uint64{}
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			k, v, found := strings.Cut(sc.Text(), " ")
			if !found {
				continue
			}
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				res[k] = n
			}
		}
		return res
	}
	return nil
}

func readUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// cgroupStatKeys are the memory.stat fields exported as node_agent_cgroup_memory_stat_bytes.
// A fixed set keeps the label bounded.
var cgroupStatKeys = []string{
	"anon", "file", "kernel", "kernel_stack", "pagetables", "sock", "shmem", "file_mapped", "slab",
	// v1 names
	"rss", "cache", "mapped_file",
}

type cgroupCollector struct {
	usage, limit, stat *prometheus.Desc
}

// NewCgroupMemoryCollector exports the agent's own cgroup memory accounting.
func NewCgroupMemoryCollector() prometheus.Collector {
	return &cgroupCollector{
		usage: prometheus.NewDesc(ns+"_cgroup_memory_usage_bytes", "Memory charged to the agent's cgroup (what the OOM killer compares to the limit)", nil, nil),
		limit: prometheus.NewDesc(ns+"_cgroup_memory_limit_bytes", "Memory limit of the agent's cgroup (absent if unlimited)", nil, nil),
		stat:  prometheus.NewDesc(ns+"_cgroup_memory_stat_bytes", "Selected memory.stat fields of the agent's cgroup", []string{"type"}, nil),
	}
}

func (c *cgroupCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.usage
	ch <- c.limit
	ch <- c.stat
}

func (c *cgroupCollector) Collect(ch chan<- prometheus.Metric) {
	usage, limit, ok := CgroupMemory()
	if !ok {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.usage, prometheus.GaugeValue, float64(usage))
	if limit > 0 {
		ch <- prometheus.MustNewConstMetric(c.limit, prometheus.GaugeValue, float64(limit))
	}
	stat := CgroupMemoryStat()
	for _, k := range cgroupStatKeys {
		if v, ok := stat[k]; ok {
			ch <- prometheus.MustNewConstMetric(c.stat, prometheus.GaugeValue, float64(v), k)
		}
	}
}

// ApplyGoMemoryLimit sets the Go runtime soft memory limit to ratio * cgroup limit, unless
// GOMEMLIMIT is set explicitly (the runtime already honors it) or ratio <= 0. A soft limit
// makes the GC run harder and the scavenger return freed pages to the OS as the heap
// approaches it, which closes most of the gap between a small live heap and a large RSS.
// It is not a hard cap and cannot by itself prevent an OOM.
func ApplyGoMemoryLimit(ratio float64) {
	if ratio <= 0 {
		return
	}
	if v, ok := os.LookupEnv("GOMEMLIMIT"); ok {
		klog.Infof("GOMEMLIMIT=%s set explicitly, ignoring go-memory-limit-ratio", v)
		return
	}
	if ratio > 1 {
		klog.Warningf("go-memory-limit-ratio %.2f > 1, ignoring", ratio)
		return
	}
	_, limit, ok := CgroupMemory()
	if !ok || limit == 0 {
		klog.Infoln("no cgroup memory limit found, not setting a Go memory limit")
		return
	}
	l := int64(float64(limit) * ratio)
	debug.SetMemoryLimit(l)
	klog.Infof("Go soft memory limit set to %d bytes (%.0f%% of the %d byte cgroup limit)", l, ratio*100, limit)
}
