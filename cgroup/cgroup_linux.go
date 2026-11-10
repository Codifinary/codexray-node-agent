// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package cgroup

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

func Init() error {
	selfNs, err := netns.GetFromPath("/proc/self/ns/cgroup")
	if err != nil {
		return err
	}
	defer selfNs.Close()
	hostNs, err := netns.GetFromPath("/proc/1/ns/cgroup")
	if err != nil {
		return err
	}
	defer hostNs.Close()
	if !selfNs.Equal(hostNs) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := unix.Setns(int(hostNs), unix.CLONE_NEWCGROUP); err != nil {
			return err
		}
		cg, err := NewFromProcessCgroupFile("/proc/self/cgroup")
		if err != nil {
			return err
		}
		baseCgroupPath = cg.Id

		if err := unix.Setns(int(selfNs), unix.CLONE_NEWCGROUP); err != nil {
			return err
		}
	}
	if _, err := os.Stat(path.Join(cgRoot, "unified")); err == nil {
		if data, err := os.ReadFile("/proc/self/mounts"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, "cgroup/unified") {
					cg2Root = path.Join(cgRoot, "unified")
					break
				}
			}
		}
	}
	klog.Infoln("cgroup v2 root is", cg2Root)
	if err := checkCgroupRoot(cgRoot); err != nil {
		klog.Errorf("--cgroupfs-root %q does not look like a cgroup filesystem (%s): container CPU/memory/IO metrics will be missing", cgRoot, err)
	}
	return nil
}

// checkCgroupRoot reports whether root holds a cgroup v2 hierarchy (cgroup.controllers) or a
// v1 hierarchy (cpu or memory controller directories). A wrong --cgroupfs-root used to drop
// container resource metrics silently.
func checkCgroupRoot(root string) error {
	if _, err := os.Stat(root); err != nil {
		return err
	}
	for _, p := range []string{"cgroup.controllers", "unified/cgroup.controllers", "cpu", "memory", "cpu,cpuacct"} {
		if _, err := os.Stat(path.Join(root, p)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no cgroup.controllers and no cpu/memory controller directories")
}
