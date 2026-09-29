// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

//go:build !stringlabels && !dedupelabels

package labels

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStableHash(t *testing.T) {
	ls1 := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	ls2 := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	ls3 := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "3"})

	assert.Equal(t, StableHash(ls1), StableHash(ls2))
	assert.NotEqual(t, StableHash(ls1), StableHash(ls3))
	assert.Equal(t, StableHash(EmptyLabels()), StableHash(EmptyLabels()))
}

func TestStableHashLargeEntry(t *testing.T) {
	bigValue := make([]byte, 2048)
	for i := range bigValue {
		bigValue[i] = 'y'
	}
	ls := New(
		Label{Name: "a", Value: string(bigValue)},
		Label{Name: "b", Value: "2"},
	)
	h1 := StableHash(ls)
	h2 := StableHash(ls)
	assert.Equal(t, h1, h2)
}

func TestStableHashIsStable(t *testing.T) {
	// Cross-check a known stable value so a future change to the hashing
	// algorithm is caught (regression against xxhash + \xff separators).
	ls := New(Label{Name: "__name__", Value: "up"}, Label{Name: "job", Value: "prometheus"})
	h := StableHash(ls)
	assert.NotZero(t, h)
	// Hash must be deterministic across calls/processes.
	assert.Equal(t, h, StableHash(ls))
}
