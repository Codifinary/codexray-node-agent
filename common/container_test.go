// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerFilter(t *testing.T) {
	f, err := newContainerFilter(nil, nil)
	require.NoError(t, err)

	assert.False(t, f.ShouldBeSkipped("/k8s/default/pod/container"))

	f, err = newContainerFilter([]string{`.+/default/.+`}, nil)
	require.NoError(t, err)
	assert.False(t, f.ShouldBeSkipped("/k8s/default/pod/container"))
	assert.True(t, f.ShouldBeSkipped("/k8s/default1/pod/container"))

	f, err = newContainerFilter(nil, []string{`.+/jobs/.+`})
	require.NoError(t, err)
	assert.False(t, f.ShouldBeSkipped("/k8s/default/pod/container"))
	assert.True(t, f.ShouldBeSkipped("/k8s/jobs/pod/container"))

	f, err = newContainerFilter([]string{`.+`}, []string{`.+/jobs/.+`})
	require.NoError(t, err)
	assert.False(t, f.ShouldBeSkipped("/k8s/default/pod/container"))
	assert.True(t, f.ShouldBeSkipped("/k8s/jobs/pod/container"))
}

func TestContainerFilterInvalidRegex(t *testing.T) {
	_, err := newContainerFilter([]string{"("}, nil)
	assert.Error(t, err)
	_, err = newContainerFilter(nil, []string{"[a-"})
	assert.Error(t, err)
}

func TestContainerFilterDenyWinsOverAllow(t *testing.T) {
	f, err := newContainerFilter([]string{`^/k8s/prod/`, `^/system\.slice/`}, []string{`/k8s/prod/debug-`})
	require.NoError(t, err)
	assert.False(t, f.ShouldBeSkipped("/k8s/prod/api/api"))
	assert.False(t, f.ShouldBeSkipped("/system.slice/docker.service"))
	assert.True(t, f.ShouldBeSkipped("/k8s/prod/debug-abc/shell"))
	assert.True(t, f.ShouldBeSkipped("/k8s/staging/api/api"))
	assert.True(t, f.ShouldBeSkipped(""))
}
