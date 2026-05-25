// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNode_uptime(t *testing.T) {
	v, err := uptime("fixtures/proc")
	assert.Nil(t, err)
	assert.Equal(t, 2659150.03, v)
}
