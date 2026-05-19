// Copyright Codexray, Inc.
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import "bytes"

func IsJvm(cmdline []byte) bool {
	idx := bytes.Index(cmdline, []byte{0})
	if idx < 0 {
		return false
	}
	return bytes.HasSuffix(cmdline[:idx], []byte("java"))
}
