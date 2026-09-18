// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTaskstatsWithoutClient(t *testing.T) {
	saved := taskstatsClient
	taskstatsClient = nil
	t.Cleanup(func() { taskstatsClient = saved })

	s, err := TaskstatsTGID(1)
	assert.Error(t, err)
	assert.Nil(t, s)

	s, err = TaskstatsPID(1)
	assert.Error(t, err)
	assert.Nil(t, s)
}
