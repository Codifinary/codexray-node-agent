// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionFromString(t *testing.T) {
	cases := []struct {
		in   string
		want Version
	}{
		{"5.15.0-1034-aws", NewVersion(5, 15, 0)},
		{"4.16.0", NewVersion(4, 16, 0)},
		{"6.8.12+bpo-amd64", NewVersion(6, 8, 12)},
		{"v1.2.3", NewVersion(1, 2, 3)},
		{"1.2.3.4", NewVersion(1, 2, 3)},
		{"5", NewVersion(5, 0, 0)},
		{"5.4", NewVersion(5, 4, 0)},
		{"3.10.0-1160.el7.x86_64", NewVersion(3, 10, 0)},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			v, err := VersionFromString(c.in)
			require.NoError(t, err)
			assert.Equal(t, c.want, v)
		})
	}
	for _, bad := range []string{"", "abc", "linux-5.4", "-1.2"} {
		_, err := VersionFromString(bad)
		assert.Error(t, err, bad)
	}
}

func TestVersionString(t *testing.T) {
	assert.Equal(t, "4.16.0", NewVersion(4, 16, 0).String())
	assert.Equal(t, "0.0.0", Version{}.String())
}

func TestVersionGreaterOrEqual(t *testing.T) {
	min := NewVersion(4, 16, 0)
	assert.True(t, NewVersion(4, 16, 0).GreaterOrEqual(min))
	assert.True(t, NewVersion(4, 16, 1).GreaterOrEqual(min))
	assert.True(t, NewVersion(4, 17, 0).GreaterOrEqual(min))
	assert.True(t, NewVersion(5, 0, 0).GreaterOrEqual(min))
	assert.True(t, NewVersion(5, 0, 0).GreaterOrEqual(NewVersion(4, 99, 99)))
	assert.False(t, NewVersion(4, 15, 99).GreaterOrEqual(min))
	assert.False(t, NewVersion(3, 99, 99).GreaterOrEqual(min))
	assert.False(t, NewVersion(4, 16, 0).GreaterOrEqual(NewVersion(4, 16, 1)))
	// numeric, not lexicographic, comparison
	assert.True(t, NewVersion(5, 10, 0).GreaterOrEqual(NewVersion(5, 9, 0)))
}
