// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNode_memory(t *testing.T) {
	m, err := memoryInfo("fixtures/proc")
	assert.Nil(t, err)
	assert.Equal(t,
		MemoryStat{
			TotalBytes:     65871236 * 1000,
			FreeBytes:      7540732 * 1000,
			AvailableBytes: 23826720 * 1000,
			CachedBytes:    15878036 * 1000,
		},
		m,
	)
}
