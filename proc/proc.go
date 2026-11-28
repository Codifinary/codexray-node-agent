// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codifinary/codexray-node-agent/cgroup"
)

var root = "/proc"

func Path(pid uint32, subpath ...string) string {
	return path.Join(append([]string{root, strconv.Itoa(int(pid))}, subpath...)...)
}

func HostPath(p string) string {
	return Path(1, "root", p)
}

func GetCmdline(pid uint32) []byte {
	cmdline, err := os.ReadFile(Path(pid, "cmdline"))
	if err != nil {
		return nil
	}
	return bytes.TrimSuffix(cmdline, []byte{0})
}

func GetNsPid(pid uint32) (uint32, error) {
	data, err := os.ReadFile(Path(pid, "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "NSpid:" {
			var f string
			switch len(fields) {
			case 2:
				f = fields[1]
			case 3:
				f = fields[2]
			default:
				return 0, errors.New("invalid NSpid value")
			}
			nsPid, err := strconv.ParseUint(f, 10, 32)
			if err != nil {
				return 0, fmt.Errorf("invalid NSpid value: %w", err)
			}
			return uint32(nsPid), nil
		}
	}
	return 0, errors.New("NSpid not found")
}

func ReadCgroup(pid uint32) (*cgroup.Cgroup, error) {
	return cgroup.NewFromProcessCgroupFile(Path(pid, "cgroup"))
}

func ListPids() ([]uint32, error) {
	root, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dirs, err := root.Readdirnames(0)
	if err != nil {
		return nil, err
	}
	res := make([]uint32, 0, len(dirs))
	for _, dir := range dirs {
		pid64, err := strconv.ParseUint(dir, 10, 32)
		if err != nil {
			continue
		}
		res = append(res, uint32(pid64))
	}
	return res, nil
}

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat times; it is 100 on every architecture the agent runs on.
const clockTicks = 100

var (
	bootTimeOnce sync.Once
	bootTime     time.Time
	bootTimeErr  error
)

// GetStartTime is when the process started, from /proc/<pid>/stat and the boot time in /proc/stat.
func GetStartTime(pid uint32) (time.Time, error) {
	data, err := os.ReadFile(Path(pid, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	// The command (field 2) is in parentheses and may hold spaces or parentheses itself.
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return time.Time{}, errors.New("invalid stat: no command")
	}
	fields := strings.Fields(string(data[end+1:]))
	const startTimeField = 22 - 3 // fields after the command start at field 3
	if len(fields) <= startTimeField {
		return time.Time{}, errors.New("invalid stat: no start time")
	}
	ticks, err := strconv.ParseUint(fields[startTimeField], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid start time: %w", err)
	}
	boot, err := getBootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * time.Second / clockTicks), nil
}

func getBootTime() (time.Time, error) {
	bootTimeOnce.Do(func() {
		data, err := os.ReadFile(path.Join(root, "stat"))
		if err != nil {
			bootTimeErr = err
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "btime "); ok {
				seconds, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if err != nil {
					bootTimeErr = fmt.Errorf("invalid btime: %w", err)
					return
				}
				bootTime = time.Unix(seconds, 0)
				return
			}
		}
		bootTimeErr = errors.New("btime not found")
	})
	return bootTime, bootTimeErr
}
