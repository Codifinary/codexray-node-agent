// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package l7

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postgresMsg builds a frontend message: Byte1(type) Int32(length incl. itself) body.
// https://www.postgresql.org/docs/current/protocol-message-formats.html
func postgresMsg(typ byte, parts ...[]byte) []byte {
	b := []byte{typ, 0, 0, 0, 0}
	for _, p := range parts {
		b = append(b, p...)
	}
	binary.BigEndian.PutUint32(b[1:], uint32(len(b)-1))
	return b
}

func postgresCStr(s string) []byte {
	return append([]byte(s), 0)
}

func postgresQuery(q string) []byte {
	return postgresMsg(PostgresFrameQuery, postgresCStr(q))
}

func postgresParse(name, q string) []byte {
	return postgresMsg(PostgresFrameParse, postgresCStr(name), postgresCStr(q), []byte{0, 0}) // 0 parameter types
}

func postgresBind(portal, name string) []byte {
	// portal, statement, int16 #formats=0, int16 #params=0, int16 #result formats=0
	return postgresMsg(PostgresFrameBind, postgresCStr(portal), postgresCStr(name), []byte{0, 0, 0, 0, 0, 0})
}

func postgresClose(kind byte, name string) []byte {
	return postgresMsg(PostgresFrameClose, []byte{kind}, postgresCStr(name))
}

func TestPostgresSimpleQuery(t *testing.T) {
	p := NewPostgresParser()
	assert.Equal(t, "SELECT 1", p.Parse(postgresQuery("SELECT 1")))
	assert.Equal(t, "", p.Parse(postgresQuery("")))

	// the kernel truncates the payload: no terminating NUL -> query is marked as partial
	full := postgresQuery("SELECT * FROM users WHERE id = 1")
	assert.Equal(t, "SELECT * FROM users...", p.Parse(full[:5+len("SELECT * FROM users")]))

	// a pipelined Sync after the query does not leak into the result
	assert.Equal(t, "BEGIN", p.Parse(append(postgresQuery("BEGIN"), postgresMsg('S')...)))
}

func TestPostgresExtendedQuery(t *testing.T) {
	p := NewPostgresParser()

	assert.Equal(t, "PREPARE stmt1 AS SELECT $1::int", p.Parse(postgresParse("stmt1", "SELECT $1::int")))
	// Bind references the prepared statement by name: the stored query is reported
	assert.Equal(t, "SELECT $1::int", p.Parse(postgresBind("", "stmt1")))
	// statements are reusable: a second Bind (named portal) resolves again
	assert.Equal(t, "SELECT $1::int", p.Parse(postgresBind("portal1", "stmt1")))

	// unknown statement name
	assert.Equal(t, "EXECUTE stmt2 /* unknown */", p.Parse(postgresBind("", "stmt2")))

	// re-preparing the same name replaces the query
	p.Parse(postgresParse("stmt1", "SELECT 2"))
	assert.Equal(t, "SELECT 2", p.Parse(postgresBind("", "stmt1")))

	// closing a portal with the same name must not drop the statement
	assert.Equal(t, "", p.Parse(postgresClose('P', "stmt1")))
	assert.Equal(t, "SELECT 2", p.Parse(postgresBind("", "stmt1")))

	// closing the statement forgets it
	assert.Equal(t, "", p.Parse(postgresClose('S', "stmt1")))
	assert.Equal(t, "EXECUTE stmt1 /* unknown */", p.Parse(postgresBind("", "stmt1")))
	assert.Empty(t, p.preparedStatements)
}

func TestPostgresUnnamedStatement(t *testing.T) {
	p := NewPostgresParser()
	res := p.Parse(postgresParse("", "SELECT now()"))
	assert.True(t, strings.HasSuffix(res, "AS SELECT now()"), res)
	assert.Equal(t, "SELECT now()", p.Parse(postgresBind("", "")))

	// the unnamed statement is replaced by the next unnamed Parse
	p.Parse(postgresParse("", "SELECT 42"))
	assert.Equal(t, "SELECT 42", p.Parse(postgresBind("", "")))

	p.Parse(postgresClose('S', ""))
	assert.Empty(t, p.preparedStatements)
}

func TestPostgresPreparedStatementsArePerParser(t *testing.T) {
	// one parser per connection: statement names are connection-scoped
	a, b := NewPostgresParser(), NewPostgresParser()
	a.Parse(postgresParse("s", "SELECT 'a'"))
	b.Parse(postgresParse("s", "SELECT 'b'"))
	assert.Equal(t, "SELECT 'a'", a.Parse(postgresBind("", "s")))
	assert.Equal(t, "SELECT 'b'", b.Parse(postgresBind("", "s")))
	assert.Equal(t, "EXECUTE s /* unknown */", NewPostgresParser().Parse(postgresBind("", "s")))
}

func TestPostgresTruncatedParse(t *testing.T) {
	p := NewPostgresParser()
	full := postgresParse("s1", "SELECT a, b, c FROM t")
	cut := full[:5+len("s1\x00SELECT a, b")]
	assert.Equal(t, "PREPARE s1 AS SELECT a, b...", p.Parse(cut))
	// the partial query is what a later Bind reports
	assert.Equal(t, "SELECT a, b...", p.Parse(postgresBind("", "s1")))

	// truncated inside the statement name: nothing can be stored
	p2 := NewPostgresParser()
	assert.Equal(t, "", p2.Parse(full[:6]))
	assert.Empty(t, p2.preparedStatements)
}

func TestPostgresTruncatedBindAndClose(t *testing.T) {
	p := NewPostgresParser()
	p.Parse(postgresParse("stmt", "SELECT 1"))

	bind := postgresBind("portal", "stmt")
	// cut inside the portal name, and inside the statement name
	assert.Equal(t, "", p.Parse(bind[:5+3]))
	assert.Equal(t, "", p.Parse(bind[:5+len("portal\x00st")]))

	cl := postgresClose('S', "stmt")
	for _, b := range l7Prefixes(cl)[:len(cl)] {
		assert.Equal(t, "", p.Parse(b))
	}
	// none of the truncated Close messages may drop the statement
	assert.Equal(t, "SELECT 1", p.Parse(postgresBind("", "stmt")))
}

func TestPostgresOtherMessages(t *testing.T) {
	p := NewPostgresParser()
	for _, typ := range []byte{'S', 'E', 'D', 'H', 'X', 'd', 'c', 'f', 'p', 0, 0xff} {
		assert.Equal(t, "", p.Parse(postgresMsg(typ, []byte("abc\x00def\x00"))), "type %q", typ)
	}
	assert.Empty(t, p.preparedStatements)
}

func TestPostgresRobustness(t *testing.T) {
	wellFormed := [][]byte{
		postgresQuery("SELECT 1"),
		postgresParse("s", "SELECT $1"),
		postgresBind("p", "s"),
		postgresClose('S', "s"),
		postgresClose('P', "p"),
	}
	var inputs [][]byte
	for _, w := range wellFormed {
		inputs = append(inputs, l7Prefixes(w)...)
		inputs = append(inputs, l7Mutations(w)...)
	}
	inputs = append(inputs, l7Garbage()...)
	// length field is ignored by the parser, but must not matter when hostile
	for _, typ := range []byte{'Q', 'P', 'B', 'C'} {
		inputs = append(inputs,
			[]byte{typ, 0xff, 0xff, 0xff, 0xff},
			[]byte{typ, 0x80, 0, 0, 0, 'S'},
			[]byte{typ, 0, 0, 0, 0, 'S', 0},
			append([]byte{typ, 0x7f, 0xff, 0xff, 0xff}, bytes.Repeat([]byte{'x'}, 1019)...),
		)
	}

	p := NewPostgresParser()
	for i, in := range inputs {
		var res string
		require.Nil(t, l7Recover(func() { res = p.Parse(in) }), "input #%d %q", i, in)
		if len(in) < 5 {
			assert.Equal(t, "", res, "input #%d %q", i, in)
		}
		assert.LessOrEqual(t, len(res), len(in)+64, "result is bounded by the payload size")
	}
}

func TestPostgresMaxPayload(t *testing.T) {
	// a 1024-byte capture of a long query: truncated, marked partial, bounded
	q := strings.Repeat("x", 2000)
	full := postgresQuery("SELECT '" + q + "'")
	res := NewPostgresParser().Parse(full[:1024])
	assert.True(t, strings.HasPrefix(res, "SELECT 'xxx"))
	assert.True(t, strings.HasSuffix(res, "..."))
	assert.Equal(t, 1024-5+3, len(res))
}
