// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package l7

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisCommand encodes a command as a RESP array of bulk strings.
// https://redis.io/docs/latest/develop/reference/protocol-spec/
func redisCommand(args ...string) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}
	return []byte(sb.String())
}

func TestRedisParseCommands(t *testing.T) {
	cases := []struct {
		args       []string
		cmd, argsS string
	}{
		{[]string{"PING"}, "PING", ""},
		{[]string{"GET", "user:1"}, "GET", "user:1"},
		{[]string{"SET", "k", "v"}, "SET", "k ..."},
		{[]string{"HSET", "h", "f1", "v1", "f2", "v2"}, "HSET", "h ..."},
		{[]string{"set", ""}, "set", ""},
	}
	for _, c := range cases {
		cmd, args := ParseRedis(redisCommand(c.args...))
		assert.Equal(t, c.cmd, cmd, "%v", c.args)
		assert.Equal(t, c.argsS, args, "%v", c.args)
	}
}

func TestRedisParseInvalid(t *testing.T) {
	for _, in := range []string{
		"",
		"PING\r\n",         // inline command, not an array
		"+OK\r\n",          // simple string reply
		"*\r\n",            // no length
		"*-1\r\n",          // null array
		"*abc\r\n",         // non numeric
		"*99999999999\r\n", // does not fit 32 bits
		"*0\r\n",           // empty array
		"*1\r\n:1\r\n",     // integer instead of bulk string
		"*1\r\n$4\r\nPING", // bulk string without CRLF (truncated)
		"*1",               // no CRLF at all
		"*2\r\n$3\r\n\r\n", // empty command
	} {
		cmd, args := ParseRedis([]byte(in))
		assert.Equal(t, "", cmd, "%q", in)
		assert.Equal(t, "", args, "%q", in)
	}
}

func TestRedisParseTruncated(t *testing.T) {
	full := redisCommand("GET", "some:key")
	for _, b := range l7Prefixes(full) {
		var cmd, args string
		require.Nil(t, l7Recover(func() { cmd, args = ParseRedis(b) }), "%q", b)
		switch {
		case len(b) < len("*2\r\n$3\r\nGET\r\n"):
			assert.Equal(t, "", cmd, "%q", b)
			assert.Equal(t, "", args, "%q", b)
		case len(b) < len(full):
			assert.Equal(t, "GET", cmd, "%q", b)
			assert.Equal(t, "", args, "%q", b)
		default:
			assert.Equal(t, "GET", cmd)
			assert.Equal(t, "some:key", args)
		}
	}
}

func TestRedisParseRobustness(t *testing.T) {
	inputs := l7Garbage()
	inputs = append(inputs, l7Mutations(redisCommand("SET", "key", "value"))...)
	inputs = append(inputs, redisCommand("SET", "k", strings.Repeat("v", 2000))[:1024])
	for i, in := range inputs {
		var cmd, args string
		require.Nil(t, l7Recover(func() { cmd, args = ParseRedis(in) }), "input #%d", i)
		assert.LessOrEqual(t, len(cmd)+len(args), len(in)+4)
	}
}
