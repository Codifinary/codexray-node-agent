// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"testing"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
)

func TestJournaldWithoutReader(t *testing.T) {
	saved := journaldReader
	journaldReader = nil
	t.Cleanup(func() { journaldReader = saved })

	cg := &cgroup.Cgroup{Id: "/system.slice/nginx.service", ContainerType: cgroup.ContainerTypeSystemdService, ContainerId: "/system.slice/nginx.service"}
	ch := make(chan logparser.LogEntry, 1)
	assert.Error(t, JournaldSubscribe(cg, ch))
	assert.NotPanics(t, func() { JournaldUnsubscribe(cg) })
	assert.Empty(t, ch)
}
