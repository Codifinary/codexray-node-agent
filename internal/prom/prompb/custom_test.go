// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package prompb

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSampleTV(t *testing.T) {
	s := Sample{Value: 3.14, Timestamp: 12345}
	assert.Equal(t, int64(12345), s.T())
	assert.Equal(t, 3.14, s.V())

	zero := Sample{}
	assert.Equal(t, int64(0), zero.T())
	assert.Equal(t, 0.0, zero.V())
}

func TestHistogramIsFloatHistogram(t *testing.T) {
	intHist := Histogram{Count: &Histogram_CountInt{CountInt: 5}}
	assert.False(t, intHist.IsFloatHistogram())

	floatHist := Histogram{Count: &Histogram_CountFloat{CountFloat: 5.5}}
	assert.True(t, floatHist.IsFloatHistogram())

	nilCount := Histogram{}
	assert.False(t, nilCount.IsFloatHistogram())
}

func TestChunkedReadResponsePooledMarshal(t *testing.T) {
	resp := &ChunkedReadResponse{
		ChunkedSeries: []*ChunkedSeries{
			{
				Labels: []Label{{Name: "a", Value: "1"}},
				Chunks: []Chunk{{Type: Chunk_XOR, Data: []byte("chunkdata")}},
			},
		},
		QueryIndex: 7,
	}

	t.Run("empty pool falls back to Marshal", func(t *testing.T) {
		p := &sync.Pool{}
		data, err := resp.PooledMarshal(p)
		require.NoError(t, err)
		expected, err := resp.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expected, data)
	})

	t.Run("pool with large enough buffer reuses it", func(t *testing.T) {
		size := resp.Size()
		buf := make([]byte, size*2)
		p := &sync.Pool{}
		p.Put(&buf)
		data, err := resp.PooledMarshal(p)
		require.NoError(t, err)
		expected, err := resp.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expected, data)
	})

	t.Run("pool buffer too small falls back to Marshal", func(t *testing.T) {
		small := make([]byte, 0)
		p := &sync.Pool{}
		p.Put(&small)
		data, err := resp.PooledMarshal(p)
		require.NoError(t, err)
		expected, err := resp.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expected, data)
	})

	t.Run("pool holds a non-*[]byte value", func(t *testing.T) {
		p := &sync.Pool{}
		p.Put("not a byte slice")
		data, err := resp.PooledMarshal(p)
		require.NoError(t, err)
		expected, err := resp.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expected, data)
	})
}
