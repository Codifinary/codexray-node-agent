// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/stretchr/testify/assert"
)

func TestAuthHeaders(t *testing.T) {
	prev := *flags.ApiKey
	t.Cleanup(func() { *flags.ApiKey = prev })

	*flags.ApiKey = ""
	assert.Empty(t, AuthHeaders())

	*flags.ApiKey = "secret-key"
	h := AuthHeaders()
	// mainv2 reads the canonicalized X-Api-Key header
	assert.Equal(t, map[string]string{"X-Api-Key": "secret-key"}, h)
}
