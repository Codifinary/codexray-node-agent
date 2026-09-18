// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemdTriggeredByWithoutBus(t *testing.T) {
	// When the systemd private bus is unavailable (non-systemd host, no
	// privileges) lookups must degrade to "" rather than fail or block.
	saved := dbusConn
	dbusConn = nil
	t.Cleanup(func() { dbusConn = saved })
	for _, id := range []string{
		"/system.slice/cron.service",
		"/system.slice/logrotate.service",
		"cron.service",
		"",
		"/",
	} {
		assert.Equal(t, "", SystemdTriggeredBy(id), "id %q", id)
	}
}

func TestSystemdPackageInitDoesNotPanic(t *testing.T) {
	// Reaching this test at all means the package init() (dbus dial) survived
	// on this host; the dial timeout used by SystemdTriggeredBy stays short so
	// a slow bus can't stall container discovery.
	assert.LessOrEqual(t, dbusTimeout.Seconds(), 1.0)
}
