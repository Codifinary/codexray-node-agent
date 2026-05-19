// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import "unicode/utf8"

func TruncateUtf8(s string, maxLength int) string {
	if len(s) <= maxLength {
		return s
	}
	for maxLength > 0 && !utf8.RuneStart(s[maxLength]) {
		maxLength--
	}
	if maxLength == 0 {
		return ""
	}
	return s[:maxLength]
}
