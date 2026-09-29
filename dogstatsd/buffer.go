// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"sync"

	"github.com/prometheus/prometheus/prompb"
)

// seriesBuffer keeps a snapshot reserved until Commit. That prevents incoming
// UDP traffic from overwriting samples while the remote writer is encoding and
// atomically placing that snapshot in the disk spool.
type seriesBuffer struct {
	mu       sync.Mutex
	max      int
	queued   []prompb.TimeSeries
	inflight []prompb.TimeSeries
}

func newSeriesBuffer(max int) *seriesBuffer {
	return &seriesBuffer{max: max, queued: make([]prompb.TimeSeries, 0, max)}
}

func (b *seriesBuffer) Enqueue(series prompb.TimeSeries) (dropped int, size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.inflight)+len(b.queued) >= b.max {
		if len(b.queued) == 0 {
			// Every slot is reserved by the payload currently being spooled.
			return 1, b.max
		}
		b.queued[0] = prompb.TimeSeries{}
		b.queued = b.queued[1:]
		dropped = 1
	}
	b.queued = append(b.queued, series)
	return dropped, len(b.inflight) + len(b.queued)
}

func (b *seriesBuffer) Snapshot(max int) []prompb.TimeSeries {
	b.mu.Lock()
	defer b.mu.Unlock()
	if max <= 0 {
		return nil
	}
	if len(b.inflight) == 0 {
		if len(b.queued) == 0 {
			return nil
		}
		if max > len(b.queued) {
			max = len(b.queued)
		}
		b.inflight = append(b.inflight[:0], b.queued[:max]...)
		for i := 0; i < max; i++ {
			b.queued[i] = prompb.TimeSeries{}
		}
		b.queued = b.queued[max:]
	}
	result := make([]prompb.TimeSeries, len(b.inflight))
	copy(result, b.inflight)
	return result
}

func (b *seriesBuffer) Commit(count int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if count > len(b.inflight) {
		count = len(b.inflight)
	}
	if count > 0 {
		for i := 0; i < count; i++ {
			b.inflight[i] = prompb.TimeSeries{}
		}
		b.inflight = b.inflight[count:]
	}
	if len(b.inflight) == 0 {
		b.inflight = nil
	}
	return len(b.inflight) + len(b.queued)
}
