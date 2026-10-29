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

func TestValidateContainerAllowlist(t *testing.T) {
	for _, allowList := range [][]string{
		nil,
		{},
		{""},
		{"   "},
		{`^/k8s/default/.+`, ""},
		{"*"},
		{" * "},
		{"**"},
		{`^/k8s/default/.+`, "*"},
	} {
		_, err := validateContainerAllowlist(allowList)
		assert.Error(t, err, "allowlist %q", allowList)
	}

	res, err := validateContainerAllowlist([]string{` ^/k8s/default/.+ `, `^/system\.slice/nginx\.service$`, ".*"})
	require.NoError(t, err)
	assert.Equal(t, []string{`^/k8s/default/.+`, `^/system\.slice/nginx\.service$`, ".*"}, res)
}
