// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfNetNs(t *testing.T) {
	self, err := GetSelfNetNs()
	require.NoError(t, err)
	defer self.Close()
	byPid, err := GetNetNs(uint32(os.Getpid()))
	require.NoError(t, err)
	defer byPid.Close()
	assert.True(t, self.Equal(byPid))

	// same namespace: f runs directly and its error is propagated
	called := false
	errF := errors.New("boom")
	err = ExecuteInNetNs(self, byPid, func() error { called = true; return errF })
	assert.True(t, called)
	assert.Equal(t, errF, err)

	_, err = GetNetNs(0x7fffffff)
	assert.Error(t, err, "non-existent pid")
}

func TestGetNsIpsSelf(t *testing.T) {
	self, err := GetSelfNetNs()
	require.NoError(t, err)
	defer self.Close()
	ips, err := GetNsIps(self)
	if err != nil {
		t.Skipf("netlink handle needs CAP_SYS_ADMIN (setns) here: %s", err)
	}
	for _, ip := range ips {
		assert.False(t, ip.IsLinkLocalUnicast(), ip.String())
		assert.False(t, ip.IsMulticast(), ip.String())
	}
}
