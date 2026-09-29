// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package l7

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zookeeperRequest builds a jute-encoded request: int32 len, int32 xid, int32 type, body.
func zookeeperRequest(op int32, body ...[]byte) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[4:], 42)
	binary.BigEndian.PutUint32(b[8:], uint32(op))
	for _, p := range body {
		b = append(b, p...)
	}
	binary.BigEndian.PutUint32(b[0:], uint32(len(b)-4))
	return b
}

// zookeeperString encodes a jute ustring: int32 length followed by the bytes.
func zookeeperString(s string) []byte {
	b := make([]byte, 4, 4+len(s))
	binary.BigEndian.PutUint32(b, uint32(len(s)))
	return append(b, s...)
}

// zookeeperMultiHeader encodes a MultiHeader: int32 type, bool done, int32 err.
func zookeeperMultiHeader(op int32, done bool) []byte {
	b := make([]byte, 9)
	binary.BigEndian.PutUint32(b, uint32(op))
	if done {
		b[4] = 1
	}
	binary.BigEndian.PutUint32(b[5:], 0xffffffff)
	return b
}

func TestZookeeperParseOps(t *testing.T) {
	withPath := map[int32]string{
		zkOpCreate: "create", zkOpDelete: "delete", zkOpExists: "exists", zkOpGetData: "getData",
		zkOpSetData: "setData", zkOpGetAcl: "getAcl", zkOpSetAcl: "setAcl", zkOpGetChildren: "getChildren",
		zkOpSync: "sync", zkOpGetChildren2: "getChildren2", zkOpCheck: "check",
		zkOpCreateContainer: "createContainer", zkOpCreateTTL: "createTTL",
	}
	for op, name := range withPath {
		gotOp, arg := ParseZookeeper(zookeeperRequest(op, zookeeperString("/app/node-1"), []byte{0, 0, 0, 0}))
		assert.Equal(t, name, gotOp, "op %d", op)
		assert.Equal(t, "/app/node-1", arg, "op %d", op)
	}
	withoutPath := map[int32]string{
		zkOpPing: "ping", zkOpReconfig: "reconfig", zkOpClose: "close", zkOpSetAuth: "setAuth", zkOpSetWatches: "setWatches",
	}
	for op, name := range withoutPath {
		gotOp, arg := ParseZookeeper(zookeeperRequest(op, zookeeperString("/ignored")))
		assert.Equal(t, name, gotOp, "op %d", op)
		assert.Equal(t, "", arg, "op %d", op)
	}
	for _, op := range []int32{0, 10, 15, 17, 999, -1, -12} {
		gotOp, arg := ParseZookeeper(zookeeperRequest(op, zookeeperString("/x")))
		assert.Equal(t, "", gotOp, "op %d", op)
		assert.Equal(t, "", arg, "op %d", op)
	}
}

func TestZookeeperParseMulti(t *testing.T) {
	op, arg := ParseZookeeper(zookeeperRequest(zkOpMulti,
		zookeeperMultiHeader(zkOpCreate, false), zookeeperString("/a"), zookeeperString("data"),
		zookeeperMultiHeader(zkOpDelete, false), zookeeperString("/b"),
		zookeeperMultiHeader(-1, true),
	))
	assert.Equal(t, "multi(create, ...)", op)
	assert.Equal(t, "/a", arg)

	// a multi header with an unknown op
	op, arg = ParseZookeeper(zookeeperRequest(zkOpMulti, zookeeperMultiHeader(-1, true)))
	assert.Equal(t, "multi(, ...)", op)
	assert.Equal(t, "", arg)

	// truncated multi header
	op, arg = ParseZookeeper(zookeeperRequest(zkOpMulti, []byte{0, 0, 0, 1, 0}))
	assert.Equal(t, "", op)
	assert.Equal(t, "", arg)
}

func TestZookeeperParseNestedMultiIsBounded(t *testing.T) {
	// a hostile payload of nested multi headers: recursion depth is bounded by the payload size
	var body [][]byte
	for i := 0; i < 1024/9; i++ {
		body = append(body, zookeeperMultiHeader(zkOpMulti, false))
	}
	payload := zookeeperRequest(zkOpMulti, body...)[:1024]
	var op, arg string
	require.Nil(t, l7Recover(func() { op, arg = ParseZookeeper(payload) }))
	assert.Equal(t, "", arg)
	// the nested op names are reported verbatim, but stay bounded by the payload size
	assert.Less(t, len(op), 2*len(payload))
}

func TestZookeeperParseStringLengths(t *testing.T) {
	// null string (-1), oversized length, zero length
	for _, l := range []uint32{0xffffffff, 0x80000000, 1025, 1 << 20} {
		lb := make([]byte, 4)
		binary.BigEndian.PutUint32(lb, l)
		op, arg := ParseZookeeper(zookeeperRequest(zkOpGetData, lb, []byte("/abc")))
		assert.Equal(t, "getData", op)
		assert.Equal(t, "", arg, "length 0x%x", l)
	}
	op, arg := ParseZookeeper(zookeeperRequest(zkOpGetData, zookeeperString(""), []byte{0}))
	assert.Equal(t, "getData", op)
	assert.Equal(t, "", arg)

	// the path is truncated by the capture
	full := zookeeperRequest(zkOpGetData, zookeeperString("/clickhouse/tables/0/log"), []byte{0})
	op, arg = ParseZookeeper(full[:16+len("/clickhouse")])
	assert.Equal(t, "getData", op)
	assert.Equal(t, "/clickhouse...<TRUNCATED>", arg)

	// 1024-byte path (the maximum allowed), truncated at the 1024-byte capture
	long := strings.Repeat("p", 1024)
	op, arg = ParseZookeeper(zookeeperRequest(zkOpCreate, zookeeperString(long))[:1024])
	assert.Equal(t, "create", op)
	assert.Equal(t, long[:1024-16]+"...<TRUNCATED>", arg)
}

func TestZookeeperParseRobustness(t *testing.T) {
	wellFormed := [][]byte{
		zookeeperRequest(zkOpGetData, zookeeperString("/a/b/c"), []byte{1}),
		zookeeperRequest(zkOpMulti, zookeeperMultiHeader(zkOpSetData, false), zookeeperString("/x"), zookeeperString("v")),
		zookeeperRequest(zkOpPing),
	}
	var inputs [][]byte
	for _, w := range wellFormed {
		inputs = append(inputs, l7Prefixes(w)...)
		inputs = append(inputs, l7Mutations(w)...)
	}
	inputs = append(inputs, l7Garbage()...)
	for i, in := range inputs {
		var op, arg string
		require.Nil(t, l7Recover(func() { op, arg = ParseZookeeper(in) }), "input #%d %x", i, in)
		if len(in) < 12 {
			assert.Equal(t, "", op)
			assert.Equal(t, "", arg)
		}
		assert.LessOrEqual(t, len(arg), 1024+len("...<TRUNCATED>"))
	}
}
