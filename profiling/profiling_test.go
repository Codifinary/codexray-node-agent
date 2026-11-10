// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"sync"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/containers"
	"github.com/codifinary/codexray-node-agent/diag"
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
