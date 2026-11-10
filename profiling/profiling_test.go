// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/codifinary/codexray-node-agent/diag"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Processes whose exit was never reported by the profiling session must not accumulate.
func TestTargetFinderReconcileRemovesExitedProcesses(t *testing.T) {
	tf := &TargetFinder{processes: map[uint32]*processInfo{}}
	for pid := uint32(1); pid <= 1000; pid++ {
		tf.add(containers.ProcessInfo{Pid: pid, ContainerId: "/k8s/ns/pod/c", StartedAt: time.Now()})
	}
	require.Len(t, tf.processes, 1000)
	require.Equal(t, 1000.0, testutil.ToFloat64(diag.ProfilingTargets))

	alive := func(pid uint32) bool { return pid%10 == 0 }
	tf.reconcile(alive)
	require.Len(t, tf.processes, 100)
	require.Equal(t, 100.0, testutil.ToFloat64(diag.ProfilingTargets))
}

func TestTargetFinderFindTargetRespectsMinAgeAndIsConcurrencySafe(t *testing.T) {
	tf := &TargetFinder{processes: map[uint32]*processInfo{}}
	now := time.Now()
	tf.add(containers.ProcessInfo{Pid: 1, ContainerId: "/k8s/ns/pod/c", StartedAt: now})
	tf.setNow(now.UnixNano())
	require.Nil(t, tf.FindTarget(1), "processes younger than one collect interval are not profiled yet")

	tf.setNow(now.Add(2 * CollectInterval).UnixNano())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); require.NotNil(t, tf.FindTarget(1)) }()
		go func() { defer wg.Done(); tf.reconcile(func(uint32) bool { return true }) }()
	}
	wg.Wait()
	require.Nil(t, tf.FindTarget(2))
}

func TestJvmPerfmapDumpInterval(t *testing.T) {
	interval := 5 * time.Minute
	prev := flagsJvmInterval(interval)
	defer flagsJvmInterval(prev)

	tf := &TargetFinder{processes: map[uint32]*processInfo{}}
	start := time.Now()
	tf.add(containers.ProcessInfo{Pid: 1, ContainerId: "/k8s/ns/pod/c", StartedAt: start.Add(-time.Hour)})
	pi := tf.processes[1]
	pi.initialized = true
	pi.jvmPerfmapDumpSupported = true

	dumps := 0
	for round := 0; round < 10; round++ { // 10 rounds of ~1m with jitter
		now := start.Add(time.Duration(round)*CollectInterval - time.Duration(round%2)*time.Second)
		tf.setNow(now.UnixNano())
		before := pi.lastPerfmapDump
		tf.FindTarget(1) // DumpPerfmap fails (no JVM); lastPerfmapDump still advances
		if pi.lastPerfmapDump != before {
			dumps++
		}
	}
	require.Equal(t, 2, dumps, "one dump every 5 rounds")
}

func flagsJvmInterval(d time.Duration) time.Duration {
	var prev time.Duration
	if flags.ProfilingJvmPerfmapInterval != nil {
		prev = *flags.ProfilingJvmPerfmapInterval
	}
	*flags.ProfilingJvmPerfmapInterval = d
	return prev
}
