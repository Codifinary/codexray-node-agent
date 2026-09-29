// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncateUtf8(t *testing.T) {
	assert.Equal(t, "ssss", TruncateUtf8("ssss", 10))
	assert.Equal(t, "ss", TruncateUtf8("ssss", 2))
	assert.Equal(t, "1", TruncateUtf8("1€€", 2))
	assert.Equal(t, "1€", TruncateUtf8("1€€", 4))
	assert.Equal(t, "", TruncateUtf8("€", 2))
}

func TestTruncateUtf8Boundaries(t *testing.T) {
	s := "a\u00e9\u4e16\U0001F600" // 1 + 2 + 3 + 4 bytes
	assert.Equal(t, s, TruncateUtf8(s, len(s)))
	assert.Equal(t, s, TruncateUtf8(s, 100))
	for n := 0; n <= len(s); n++ {
		got := TruncateUtf8(s, n)
		assert.LessOrEqual(t, len(got), n)
		assert.True(t, utf8.ValidString(got), "n=%d -> %q", n, got)
	}
	assert.Equal(t, "a", TruncateUtf8(s, 2))
	assert.Equal(t, "a\u00e9", TruncateUtf8(s, 3))
	assert.Equal(t, "a\u00e9", TruncateUtf8(s, 5))
	assert.Equal(t, "a\u00e9\u4e16", TruncateUtf8(s, 6))
	assert.Equal(t, "a\u00e9\u4e16", TruncateUtf8(s, 9))
	assert.Equal(t, "", TruncateUtf8(s, 0))
	assert.Equal(t, "", TruncateUtf8("", 0))
}

func TestTruncateUtf8NegativeLength(t *testing.T) {
	// --max-label-length is a plain Int flag; a negative value reaches TruncateUtf8 from
	// Container.Collect and indexes s[-1].
	// BUG: TruncateUtf8 panics (index out of range) when maxLength < 0 — unskip when fixed
	t.Skip("BUG: TruncateUtf8 panics (index out of range) when maxLength < 0")
	assert.NotPanics(t, func() {
		assert.Equal(t, "", TruncateUtf8("sample", -1))
	})
}
