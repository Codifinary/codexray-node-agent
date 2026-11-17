// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cilium/ebpf/link"
	"github.com/codifinary/codexray-node-agent/diag"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// fakeLink satisfies link.Link by embedding the interface; only Close is exercised.
type fakeLink struct {
	link.Link
	closes *atomic.Int64
}

func (l fakeLink) Close() error {
	l.closes.Add(1)
	return nil
}

func newTestProcess() *Process {
	p := &Process{Pid: 1}
	p.ctx, p.cancelFunc = context.WithCancel(context.Background())
	diag.ProcessesActive.Inc()
	return p
}

func links(n int, closes *atomic.Int64) []link.Link {
	res := make([]link.Link, n)
	for i := range res {
		res[i] = fakeLink{closes: closes}
	}
	return res
}

// Every link handed to a process is closed exactly once, whether it was attached before
// Close (detached by Close) or after Close (detached immediately by addUprobes). This is the
// race between the instrumentation goroutine and process exit that used to leak links.
func TestProcessUprobesClosedExactlyOnceUnderRace(t *testing.T) {
	baseUprobes := testutil.ToFloat64(diag.UprobesActive)
	baseProcesses := testutil.ToFloat64(diag.ProcessesActive)

	const rounds = 200
	for i := 0; i < rounds; i++ {
		var closes atomic.Int64
		p := newTestProcess()
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); p.addUprobes(links(3, &closes)) }() // python/nodejs goroutine
		go func() { defer wg.Done(); p.addUprobes(links(5, &closes)) }() // TLS from the event loop
		go func() { defer wg.Done(); p.Close() }()                       // process exit
		wg.Wait()
		p.Close() // idempotent
		require.EqualValues(t, 8, closes.Load(), "round %d", i)
		require.Equal(t, 0, p.uprobeCount())
	}
	require.Equal(t, baseUprobes, testutil.ToFloat64(diag.UprobesActive), "active uprobe gauge must return to baseline")
	require.Equal(t, baseProcesses, testutil.ToFloat64(diag.ProcessesActive), "active process gauge must return to baseline")
}

func TestProcessAddUprobesAfterCloseDetachesImmediately(t *testing.T) {
	var closes atomic.Int64
	p := newTestProcess()
	p.Close()
	before := testutil.ToFloat64(diag.UprobesLeakedOnClose)
	p.addUprobes(links(4, &closes))
	require.EqualValues(t, 4, closes.Load())
	require.Equal(t, 0, p.uprobeCount())
	require.Equal(t, before+4, testutil.ToFloat64(diag.UprobesLeakedOnClose))
}
