// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"github.com/codifinary/codexray-node-agent/flags"
)

func AuthHeaders() map[string]string {
	res := map[string]string{}
	if apiKey := *flags.ApiKey; apiKey != "" {
		res["X-Api-Key"] = apiKey
	}
	return res
}
