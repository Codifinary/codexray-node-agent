// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsJvm(t *testing.T) {
	assert.True(t, IsJvm([]byte("/usr/bin/java\x00-jar\x00app.jar")))
	assert.True(t, IsJvm([]byte("java\x00-cp\x00.")))
	assert.True(t, IsJvm([]byte("/opt/jdk-17/bin/java\x00")))
	assert.False(t, IsJvm([]byte("/usr/bin/python3\x00java")))
	assert.False(t, IsJvm([]byte("/usr/bin/javac\x00Main.java")))
	assert.False(t, IsJvm(nil))
	assert.False(t, IsJvm([]byte{}))
	assert.False(t, IsJvm([]byte("/usr/bin/node")))
}
