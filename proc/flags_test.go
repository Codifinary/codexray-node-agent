// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFlags(t *testing.T) {
	dir := procTestRoot(t)

	_, err := GetFlags(1)
	assert.Error(t, err, "exited process")

	procTestWrite(t, dir, "2/environ", "PATH=/usr/bin\x00HOME=/root\x00")
	f, err := GetFlags(2)
	require.NoError(t, err)
	assert.Equal(t, Flags{}, f)

	procTestWrite(t, dir, "3/environ",
		"PATH=/usr/bin\x00CODEXRAY_EBPF_PROFILING=disabled\x00CODEXRAY_LOG_MONITORING=disabled\x00CODEXRAY_EBPF_TRACES=disabled\x00")
	f, err = GetFlags(3)
	require.NoError(t, err)
	assert.Equal(t, Flags{EbpfProfilingDisabled: true, EbpfTracesDisabled: true, LogMonitoringDisabled: true}, f)

	procTestWrite(t, dir, "4/environ",
		"CODEXRAY_EBPF_PROFILING=enabled\x00CODEXRAY_EBPF_TRACES=disabled\x00CODEXRAY_UNKNOWN=disabled\x00"+
			"COROOT_LOG_MONITORING=disabled\x00noequals\x00X_CODEXRAY_LOG_MONITORING=disabled\x00=\x00")
	f, err = GetFlags(4)
	require.NoError(t, err)
	assert.Equal(t, Flags{EbpfTracesDisabled: true}, f)

	// empty environ (kernel thread / zombie)
	procTestWrite(t, dir, "5/environ", "")
	f, err = GetFlags(5)
	require.NoError(t, err)
	assert.Equal(t, Flags{}, f)
}
