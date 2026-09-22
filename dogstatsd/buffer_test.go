package dogstatsd

import (
	"testing"

	"github.com/prometheus/prometheus/prompb"
)

func TestSeriesBufferSnapshotCommitAndOverflow(t *testing.T) {
	buffer := newSeriesBuffer(2)
	series := func(value float64) prompb.TimeSeries {
		return prompb.TimeSeries{Samples: []prompb.Sample{{Value: value}}}
	}
	buffer.Enqueue(series(1))
	buffer.Enqueue(series(2))
	if dropped, size := buffer.Enqueue(series(3)); dropped != 1 || size != 2 {
		t.Fatalf("overflow = (%d, %d), want (1, 2)", dropped, size)
	}
	snapshot := buffer.Snapshot(10)
	if len(snapshot) != 2 || snapshot[0].Samples[0].Value != 2 || snapshot[1].Samples[0].Value != 3 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if remaining := buffer.Commit(1); remaining != 1 {
		t.Fatalf("remaining = %d, want 1", remaining)
	}
	if got := buffer.Snapshot(10)[0].Samples[0].Value; got != 3 {
		t.Fatalf("remaining value = %v, want 3", got)
	}
}

func TestSeriesBufferProtectsInflightSnapshot(t *testing.T) {
	buffer := newSeriesBuffer(2)
	series := func(value float64) prompb.TimeSeries {
		return prompb.TimeSeries{Samples: []prompb.Sample{{Value: value}}}
	}
	buffer.Enqueue(series(1))
	buffer.Enqueue(series(2))
	firstSnapshot := buffer.Snapshot(2)
	if dropped, size := buffer.Enqueue(series(3)); dropped != 1 || size != 2 {
		t.Fatalf("enqueue during inflight = (%d, %d), want dropped with size 2", dropped, size)
	}
	retrySnapshot := buffer.Snapshot(2)
	if len(retrySnapshot) != 2 || retrySnapshot[0].Samples[0].Value != 1 || retrySnapshot[1].Samples[0].Value != 2 {
		t.Fatalf("inflight snapshot changed: first=%#v retry=%#v", firstSnapshot, retrySnapshot)
	}
	if remaining := buffer.Commit(2); remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
}
