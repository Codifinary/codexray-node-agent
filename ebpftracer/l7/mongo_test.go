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
	"go.mongodb.org/mongo-driver/bson"
)

// mongoMessage builds a wire message: MsgHeader{length, requestID, responseTo, opCode}, flagBits, then sections.
// https://www.mongodb.com/docs/manual/reference/mongodb-wire-protocol/
func mongoMessage(opCode int32, sections ...[]byte) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[4:], 7)
	binary.LittleEndian.PutUint32(b[12:], uint32(opCode))
	for _, s := range sections {
		b = append(b, s...)
	}
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	return b
}

func mongoBodySection(t *testing.T, doc any) []byte {
	data, err := bson.Marshal(doc)
	require.NoError(t, err)
	return append([]byte{mongoSectionKindBody}, data...)
}

func TestMongoParseOpMsg(t *testing.T) {
	doc := bson.D{{Key: "find", Value: "users"}, {Key: "filter", Value: bson.D{{Key: "age", Value: bson.D{{Key: "$gt", Value: 30}}}}}, {Key: "$db", Value: "test"}}
	res := ParseMongo(mongoMessage(MongoOpMSG, mongoBodySection(t, doc)))
	assert.Contains(t, res, `"find": "users"`)
	assert.Contains(t, res, `"$gt"`)
	assert.Contains(t, res, `"$db": "test"`)

	// a trailing document-sequence section (kind 1) after the body does not break parsing
	seq := []byte{1, 9, 0, 0, 0, 'd', 'o', 'c', 's', 0}
	res2 := ParseMongo(mongoMessage(MongoOpMSG, mongoBodySection(t, doc), seq))
	assert.Equal(t, res, res2)
}

func TestMongoParseUnsupported(t *testing.T) {
	doc := bson.D{{Key: "isMaster", Value: 1}}
	// OP_QUERY (2004), OP_COMPRESSED (2012), OP_REPLY (1)
	for _, op := range []int32{2004, 2012, 1, 0, -1} {
		assert.Equal(t, "<truncated>", ParseMongo(mongoMessage(op, mongoBodySection(t, doc))), "op %d", op)
	}
	// first section is a document sequence (kind 1)
	assert.Equal(t, "<truncated>", ParseMongo(mongoMessage(MongoOpMSG, []byte{1, 9, 0, 0, 0, 'd', 'o', 'c', 's', 0})))
}

func TestMongoParseSectionLength(t *testing.T) {
	doc := bson.D{{Key: "ping", Value: 1}}
	payload := mongoMessage(MongoOpMSG, mongoBodySection(t, doc))
	for _, l := range []uint32{0, 0xffffffff, 0x80000000, uint32(len(payload))} {
		b := append([]byte(nil), payload...)
		binary.LittleEndian.PutUint32(b[21:], l)
		assert.Equal(t, "<truncated>", ParseMongo(b), "length 0x%x", l)
	}
	// declared lengths 1..4 are below the minimum BSON document size (5): no panic, no document
	for l := uint32(1); l <= 4; l++ {
		b := append([]byte(nil), payload...)
		binary.LittleEndian.PutUint32(b[21:], l)
		var res string
		require.Nil(t, l7Recover(func() { res = ParseMongo(b) }))
		assert.NotContains(t, res, "ping", "length %d", l)
	}
}

func TestMongoParseCorruptBson(t *testing.T) {
	doc := bson.D{{Key: "k", Value: "value"}}
	payload := mongoMessage(MongoOpMSG, mongoBodySection(t, doc))
	// element type 0x02 (string): int32 length at offset 21+4+1+2; make it lie
	for _, l := range []uint32{0, 0xffffffff, 0x7fffffff, 1000} {
		b := append([]byte(nil), payload...)
		binary.LittleEndian.PutUint32(b[21+4+1+2:], l)
		var res string
		require.Nil(t, l7Recover(func() { res = ParseMongo(b) }), "string length 0x%x", l)
		assert.NotContains(t, res, "value")
	}
	// unknown element type
	b := append([]byte(nil), payload...)
	b[21+4] = 0x7e
	require.Nil(t, l7Recover(func() { ParseMongo(b) }))
}

func TestMongoParseTruncatedAndMaxPayload(t *testing.T) {
	doc := bson.D{{Key: "insert", Value: "c"}, {Key: "documents", Value: bson.A{bson.D{{Key: "blob", Value: strings.Repeat("z", 3000)}}}}}
	payload := mongoMessage(MongoOpMSG, mongoBodySection(t, doc))
	require.Greater(t, len(payload), 1024)
	assert.Equal(t, "<truncated>", ParseMongo(payload[:1024]))

	small := mongoMessage(MongoOpMSG, mongoBodySection(t, bson.D{{Key: "ping", Value: 1}}))
	for _, b := range l7Prefixes(small) {
		var res string
		require.Nil(t, l7Recover(func() { res = ParseMongo(b) }), "%x", b)
		if len(b) < len(small) {
			assert.Equal(t, "<truncated>", res, "len %d", len(b))
		} else {
			assert.Equal(t, `{"ping": {"$numberInt":"1"}}`, res)
		}
	}
}

func TestMongoParseRobustness(t *testing.T) {
	inputs := l7Garbage()
	inputs = append(inputs, l7Mutations(mongoMessage(MongoOpMSG, mongoBodySection(t, bson.D{{Key: "find", Value: "x"}, {Key: "n", Value: 1.5}})))...)
	for i, in := range inputs {
		require.Nil(t, l7Recover(func() { ParseMongo(in) }), "input #%d %x", i, in)
	}
	// garbage bodies behind a valid OP_MSG header
	for i, g := range l7Garbage() {
		in := append(mongoMessage(MongoOpMSG, []byte{0}), g...)
		if len(in) > 1024 {
			in = in[:1024]
		}
		require.Nil(t, l7Recover(func() { ParseMongo(in) }), "input #%d %x", i, in)
	}
}
