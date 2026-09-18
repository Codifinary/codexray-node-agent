// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetKernelVersion(t *testing.T) {
	prev := kernelVersion
	t.Cleanup(func() { kernelVersion = prev })

	assert.NoError(t, SetKernelVersion("5.15.0-1034-aws"))
	assert.Equal(t, NewVersion(5, 15, 0), GetKernelVersion())

	// invalid inputs are rejected and keep the previous value
	assert.Error(t, SetKernelVersion("garbage"))
	assert.Error(t, SetKernelVersion(""))
	assert.Error(t, SetKernelVersion("0.1.2"))
	assert.Equal(t, NewVersion(5, 15, 0), GetKernelVersion())
}
