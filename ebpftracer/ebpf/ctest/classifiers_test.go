// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

package ctest

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// rawBE16 is how the C code sees a big-endian stream id it copies without
// byte-swapping: request and response ids are only compared with each other.
func rawBE16(v uint16) int16 { return int16(binary.LittleEndian.Uint16(be16(v))) }

// endsWithCRLF builds prefix + n filler bytes + "\r\n".
func endsWithCRLF(prefix string, n int) []byte {
	return cat([]byte(prefix), bytes.Repeat([]byte("x"), n), []byte("\r\n"))
}

// --- HTTP ---

func TestHTTPRequest(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
	}{
		{"GET", "GET /api/v1/items HTTP/1.1\r\nHost: x\r\n\r\n", 1},
		{"POST", "POST /api/v1/items HTTP/1.1\r\nContent-Length: 0\r\n\r\n", 1},
		{"HEAD", "HEAD / HTTP/1.1\r\nHost: x\r\n\r\n", 1},
		{"PUT", "PUT /items/1 HTTP/1.1\r\nHost: x\r\n\r\n", 1},
		{"DELETE", "DELETE /items/1 HTTP/1.1\r\nHost: x\r\n\r\n", 1},
		{"CONNECT", "CONNECT example.com:443 HTTP/1.1\r\n\r\n", 1},
		{"OPTIONS", "OPTIONS * HTTP/1.1\r\nHost: x\r\n\r\n", 1},
		{"PATCH", "PATCH /items/1 HTTP/1.1\r\nHost: x\r\n\r\n", 1},
		{"response is not a request", "HTTP/1.1 200 OK\r\n\r\n", 0},
		{"methods are case-sensitive", "get /api HTTP/1.1\r\nHost: x\r\n\r\n", 0},
		{"unknown method", "BREW /pot HTTP/1.1\r\nHost: x\r\n\r\n", 0},
		{"NUL right after the method", "GET\x00/ HTTP/1.1\r\nHost: x\r\n\r\n", 0},
		{"empty", "", 0},
		// Only the first 15 bytes are inspected and at least 15 are required.
		{"15 bytes is enough", "GET /a HTTP/1.0", 1},
		{"14 bytes is too short", "GET / HTTP/1.0", 0},
		// Only a prefix match: the byte after the method is not checked.
		{"method prefix without a space still matches", "GETX /foo HTTP/1.1\r\n\r\n", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsHTTPRequest([]byte(c.payload)))
		})
	}
}

func TestHTTPResponse(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
		status        int32
	}{
		{"200", "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n", 1, 200},
		{"404 on HTTP/1.0", "HTTP/1.0 404 Not Found\r\n\r\n", 1, 404},
		{"503", "HTTP/1.1 503 Service Unavailable\r\n\r\n", 1, 503},
		{"non-digit status", "HTTP/1.1 2x0 OK\r\n\r\n", 0, Unset},
		{"missing minor version dot", "HTTP/11 200 OK\r\n\r\n", 0, Unset},
		{"lowercase protocol", "http/1.1 200 OK\r\n\r\n", 0, Unset},
		{"two spaces before status", "HTTP/1.1  200 OK\r\n\r\n", 0, Unset},
		{"request is not a response", "GET / HTTP/1.1\r\nHost: x\r\n\r\n", 0, Unset},
		{"shorter than 15 bytes", "HTTP/1.1 200\r\n", 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsHTTPResponse([]byte(c.payload))
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
}

// --- HTTP/2 ---

func h2Frame(length uint32, typ, flags byte, stream uint32) []byte {
	return cat([]byte{byte(length >> 16), byte(length >> 8), byte(length), typ, flags}, be32(stream))
}

func TestHTTP2Frame(t *testing.T) {
	preface := []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	cases := []struct {
		name    string
		payload []byte
		method  int
		want    int
	}{
		{"HEADERS on client stream 1", cat(h2Frame(10, 0x1, 0x4, 1), make([]byte, 10)), MethodHTTP2ClientFrames, 1},
		{"HEADERS on client stream 3", cat(h2Frame(10, 0x1, 0x4, 3), make([]byte, 10)), MethodHTTP2ClientFrames, 1},
		{"client stream regardless of direction", cat(h2Frame(10, 0x1, 0x4, 1), make([]byte, 10)), MethodHTTP2ServerFrames, 1},
		{"DATA on even (server-initiated) stream", cat(h2Frame(4, 0x0, 0, 2), make([]byte, 4)), MethodHTTP2ServerFrames, 0},
		{"server SETTINGS preface", h2Frame(0, 0x4, 0, 0), MethodHTTP2ServerFrames, 1},
		{"SETTINGS on stream 0 from the client", h2Frame(0, 0x4, 0, 0), MethodHTTP2ClientFrames, 0},
		{"client connection preface", preface, MethodHTTP2ClientFrames, 1},
		{"preface seen on the server side", preface, MethodHTTP2ServerFrames, 0},
		{"truncated preface", preface[:20], MethodHTTP2ClientFrames, 0},
		{"frame longer than the buffer", cat(h2Frame(100, 0x1, 0x4, 1), make([]byte, 10)), MethodHTTP2ClientFrames, 0},
		{"frame type above 9", cat(h2Frame(4, 0xa, 0, 1), make([]byte, 4)), MethodHTTP2ClientFrames, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, LooksLikeHTTP2Frame(c.payload, c.method))
		})
	}
}

// --- Postgres ---

func pgMsg(typ byte, body []byte) []byte {
	return cat([]byte{typ}, be32(uint32(4+len(body))), body)
}

func pgExtendedQuery(query string, last []byte) []byte {
	return cat(
		pgMsg('P', cat([]byte{0}, []byte(query), []byte{0, 0, 0})),
		pgMsg('B', make([]byte, 8)),
		pgMsg('E', make([]byte, 5)),
		last,
	)
}

func TestPostgresQuery(t *testing.T) {
	sync := pgMsg('S', nil)
	cases := []struct {
		name    string
		payload []byte
		want    int
		reqType byte
	}{
		{"simple query", pgMsg('Q', []byte("SELECT 1\x00")), 1, 'Q'},
		{"close statement", pgMsg('C', []byte("Sstmt1\x00")), 1, 'C'},
		{"large simple query is sized by its own header", pgMsg('Q', cat(bytes.Repeat([]byte("a"), 2000), []byte{0})), 1, 'Q'},
		{"extended query ending in Sync", pgExtendedQuery("SELECT $1", sync), 1, 'P'},
		{"extended query without Sync", pgExtendedQuery("SELECT $1", pgMsg('H', nil)), 0, 'P'},
		{"simple query with a wrong length", cat([]byte{'Q'}, be32(100), []byte("SELECT 1\x00")), 0, 'Q'},
		{"shorter than a message header", []byte("Q\x00\x00\x00"), 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rt := IsPostgresQuery(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.reqType, rt)
		})
	}
}

func TestPostgresExtendedQueryLongerThanPayloadLimit(t *testing.T) {
	// BUG: is_postgres_query (postgres.c:25-26) looks for the trailing Sync at the
	// size capped to MAX_PAYLOAD_SIZE-1, not at the real end, so extended-protocol
	// batches over ~1 KB are never classified — unskip when fixed
	t.Skip("BUG: extended-protocol Postgres queries longer than MAX_PAYLOAD_SIZE-1 are not recognized (Sync read at the truncated offset)")
	got, rt := IsPostgresQuery(pgExtendedQuery(string(bytes.Repeat([]byte("a"), 1500)), pgMsg('S', nil)))
	assert.Equal(t, 1, got)
	assert.Equal(t, byte('P'), rt)
}

func TestPostgresResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
		status  int32
	}{
		{"CommandComplete", pgMsg('C', []byte("SELECT 1\x00")), 1, StatusOK},
		{"RowDescription + DataRow", cat(pgMsg('T', make([]byte, 6)), pgMsg('D', make([]byte, 6))), 1, StatusOK},
		{"DataRow", pgMsg('D', make([]byte, 6)), 1, StatusOK},
		{"ParameterDescription", pgMsg('t', make([]byte, 2)), 1, StatusOK},
		{"ErrorResponse", pgMsg('E', []byte("SERROR\x00\x00")), 1, StatusFailed},
		{"ParseComplete then CommandComplete", cat(pgMsg('1', nil), pgMsg('C', []byte("SELECT 1\x00"))), 1, StatusOK},
		{"BindComplete then DataRow", cat(pgMsg('2', nil), pgMsg('D', make([]byte, 6))), 1, StatusOK},
		{"ParseComplete then ErrorResponse", cat(pgMsg('1', nil), pgMsg('E', []byte("SERROR\x00\x00"))), 1, StatusFailed},
		{"ReadyForQuery alone", pgMsg('Z', []byte("I")), 0, Unset},
		{"length exceeds the buffer", cat([]byte{'C'}, be32(100), []byte("SEL")), 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsPostgresResponse(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
}

func TestPostgresResponseParseAndBindComplete(t *testing.T) {
	// BUG: is_postgres_response (postgres.c:44-47) skips only ONE leading
	// ParseComplete/BindComplete, so the usual reply to Parse+Bind+Execute+Sync
	// ("1","2","T"/"D",...) lands on '2' and is not recognized — unskip when fixed
	t.Skip("BUG: Postgres response starting with ParseComplete + BindComplete is not recognized")
	got, status := IsPostgresResponse(cat(pgMsg('1', nil), pgMsg('2', nil), pgMsg('T', make([]byte, 6))))
	assert.Equal(t, 1, got)
	assert.Equal(t, StatusOK, status)
}

// --- MySQL ---

func mysqlPacket(seq byte, payload []byte) []byte {
	n := len(payload)
	return cat([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, payload)
}

const (
	mysqlComQuery       = 0x03
	mysqlComStmtPrepare = 0x16
	mysqlComStmtExecute = 0x17
	mysqlComStmtClose   = 0x19
)

func TestMysqlQuery(t *testing.T) {
	query := mysqlPacket(0, cat([]byte{mysqlComQuery}, []byte("SELECT 1")))
	cases := []struct {
		name    string
		payload []byte
		want    int
		reqType byte
	}{
		{"COM_QUERY", query, 1, 0},
		{"COM_STMT_EXECUTE", mysqlPacket(0, []byte{mysqlComStmtExecute, 1, 0, 0, 0, 0, 1, 0, 0, 0}), 1, 0},
		{"COM_STMT_PREPARE sets the request type", mysqlPacket(0, cat([]byte{mysqlComStmtPrepare}, []byte("SELECT ?"))), 1, mysqlComStmtPrepare},
		{"COM_STMT_CLOSE sets the request type", mysqlPacket(0, []byte{mysqlComStmtClose, 1, 0, 0, 0}), 1, mysqlComStmtClose},
		{"sequence id must be 0", mysqlPacket(1, cat([]byte{mysqlComQuery}, []byte("SELECT 1"))), 0, 0},
		{"two packets in one write", cat(query, query), 0, 0},
		{"COM_PING is not tracked", mysqlPacket(0, []byte{0x0e}), 0, 0},
		{"shorter than a header + command", []byte{1, 0, 0, 0}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rt := IsMysqlQuery(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.reqType, rt)
		})
	}
}

func TestMysqlResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		reqType byte
		want    int
		stmtID  uint32
		status  int32
	}{
		{"OK packet", mysqlPacket(1, []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}), 0, 1, 0, StatusOK},
		{"OK to PREPARE carries the statement id", mysqlPacket(1, []byte{0x00, 0x2a, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0}), mysqlComStmtPrepare, 1, 42, StatusOK},
		{"statement id only read for PREPARE", mysqlPacket(1, []byte{0x00, 0x2a, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0}), 0, 1, 0, StatusOK},
		{"ERR packet", mysqlPacket(1, cat([]byte{0xff, 0x28, 0x04}, []byte("#42000oops"))), 0, 1, 0, StatusFailed},
		{"EOF packet", mysqlPacket(5, []byte{0xfe, 0, 0, 2, 0}), 0, 1, 0, StatusOK},
		{"result set column count", mysqlPacket(1, []byte{0x02}), 0, 1, 0, StatusOK},
		{"sequence id 0 is a request", mysqlPacket(0, []byte{0x00, 0, 0, 2, 0, 0, 0}), 0, 0, 0, Unset},
		{"row packet is not a status", mysqlPacket(3, []byte{0x01, '1'}), 0, 0, 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stmt, status := IsMysqlResponse(c.payload, c.reqType)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.stmtID, stmt)
			assert.Equal(t, c.status, status)
		})
	}
}

// --- Redis ---

func TestRedisQuery(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
	}{
		{"PING", "*1\r\n$4\r\nPING\r\n", 1},
		{"SET", "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n", 1},
		{"two-digit arg count", "*12\r\n$4\r\nMSET\r\n", 1},
		{"three-digit arg count is not recognized", "*123\r\n$4\r\nMSET\r\n", 0},
		{"inline command is not recognized", "PING\r\n", 0},
		{"non-digit count", "*a\r\n$4\r\nPING\r\n", 0},
		{"response is not a query", "+OK\r\n", 0},
		{"shorter than 5 bytes", "*1\r\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsRedisQuery([]byte(c.payload)))
		})
	}
}

func TestRedisResponse(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
		status        int32
	}{
		{"simple string", "+OK\r\n", 1, StatusOK},
		{"integer", ":1\r\n", 1, StatusOK},
		{"bulk string", "$5\r\nhello\r\n", 1, StatusOK},
		{"array", "*2\r\n$1\r\na\r\n$1\r\nb\r\n", 1, StatusOK},
		{"nil bulk string", "$-1\r\n", 1, StatusOK},
		{"error", "-ERR unknown command\r\n", 1, StatusFailed},
		{"no trailing CRLF", "+OK", 0, Unset},
		// RESP3-only types (null, boolean, double, map, set, ...) are not recognized.
		{"RESP3 null", "_\r\n", 0, Unset},
		{"RESP3 map", "%1\r\n+a\r\n:1\r\n", 0, Unset},
		{"empty", "", 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsRedisResponse([]byte(c.payload))
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
}

func TestRedisResponseLongerThanPayloadLimit(t *testing.T) {
	// BUG: is_redis_response (redis.c:30-31) checks for the trailing CRLF at the
	// size capped to MAX_PAYLOAD_SIZE-1, not at the real end of the read, so
	// replies over ~1 KB are not recognized and their requests get no status — unskip when fixed
	t.Skip("BUG: Redis responses longer than MAX_PAYLOAD_SIZE-1 are not recognized (CRLF read at the truncated offset)")
	got, status := IsRedisResponse(endsWithCRLF("$4000\r\n", 4000))
	assert.Equal(t, 1, got)
	assert.Equal(t, StatusOK, status)
}

// --- Memcached ---

func TestMemcachedQuery(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
	}{
		{"set", "set foo 0 0 3\r\nbar\r\n", 1},
		{"add", "add foo 0 0 3\r\nbar\r\n", 1},
		{"cas", "cas foo 0 0 3 99\r\nbar\r\n", 1},
		{"get", "get foo\r\n", 1},
		{"gets", "gets foo\r\n", 1},
		{"gat", "gat 0 foo\r\n", 1},
		{"gats", "gats 0 foo\r\n", 1},
		{"incr", "incr foo 1\r\n", 1},
		{"decr", "decr foo 1\r\n", 1},
		{"touch", "touch foo 10\r\n", 1},
		{"delete", "delete foo\r\n", 1},
		{"append", "append foo 0 0 1\r\nx\r\n", 1},
		{"prepend", "prepend foo 0 0 1\r\nx\r\n", 1},
		{"replace", "replace foo 0 0 1\r\nx\r\n", 1},
		{"no trailing CRLF", "set foo 0 0 3\r\nbar", 0},
		{"unknown command", "version\r\n", 0},
		// Requests shorter than 9 bytes are ignored, e.g. a get for a 1-2 char key.
		{"get with a 1-char key is too short", "get k\r\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsMemcachedQuery([]byte(c.payload)))
		})
	}
}

func TestMemcachedQueryLongerThanPayloadLimit(t *testing.T) {
	// BUG: is_memcached_query (memcached.c:10-11) checks for the trailing CRLF at
	// the size capped to MAX_PAYLOAD_SIZE-1, so storage commands with values over
	// ~1 KB are not recognized — unskip when fixed
	t.Skip("BUG: Memcached requests longer than MAX_PAYLOAD_SIZE-1 are not recognized (CRLF read at the truncated offset)")
	assert.Equal(t, 1, IsMemcachedQuery(endsWithCRLF("set k 0 0 2000\r\n", 2000)))
}

func TestMemcachedResponse(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
		status        int32
	}{
		{"VALUE", "VALUE foo 0 3\r\nbar\r\nEND\r\n", 1, StatusOK},
		{"STORED", "STORED\r\n", 1, StatusOK},
		{"DELETED", "DELETED\r\n", 1, StatusOK},
		{"NOT_STORED", "NOT_STORED\r\n", 1, StatusOK},
		{"NOT_FOUND", "NOT_FOUND\r\n", 1, StatusOK},
		{"EXISTS", "EXISTS\r\n", 1, StatusOK},
		{"incr/decr value", "42\r\n", 1, StatusOK},
		{"ERROR", "ERROR\r\n", 1, StatusFailed},
		{"CLIENT_ERROR", "CLIENT_ERROR bad data chunk\r\n", 1, StatusFailed},
		{"SERVER_ERROR", "SERVER_ERROR out of memory\r\n", 1, StatusFailed},
		{"no trailing CRLF", "STORED", 0, Unset},
		{"unknown reply", "HELLO\r\n", 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsMemcachedResponse([]byte(c.payload))
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
}

func TestMemcachedResponseTouched(t *testing.T) {
	// BUG: is_memcached_response (memcached.c:73) matches "TOC" for TOUCHED, but
	// the reply starts with "TOU", so every touch reply is left without a status — unskip when fixed
	t.Skip("BUG: Memcached \"TOUCHED\" reply is not recognized (prefix checked as \"TOC\")")
	got, status := IsMemcachedResponse([]byte("TOUCHED\r\n"))
	assert.Equal(t, 1, got)
	assert.Equal(t, StatusOK, status)
}

func TestMemcachedResponseCacheMiss(t *testing.T) {
	// BUG: is_memcached_response (memcached.c:61-100) has no case for "END", which
	// is the entire reply to a get/gets for a missing key, so every cache miss is
	// left without a status — unskip when fixed
	t.Skip("BUG: Memcached cache-miss reply \"END\\r\\n\" is not recognized")
	got, status := IsMemcachedResponse([]byte("END\r\n"))
	assert.Equal(t, 1, got)
	assert.Equal(t, StatusOK, status)
}

// --- MongoDB ---

func mongoHeader(length, requestID, responseTo, opCode int32) []byte {
	return cat(le32(uint32(length)), le32(uint32(requestID)), le32(uint32(responseTo)), le32(uint32(opCode)))
}

const (
	mongoOpReply      = 1
	mongoOpQuery      = 2004
	mongoOpCompressed = 2012
	mongoOpMsg        = 2013
)

func TestMongoQuery(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"OP_MSG", cat(mongoHeader(40, 7, 0, mongoOpMsg), make([]byte, 24)), 1},
		{"OP_COMPRESSED", cat(mongoHeader(40, 7, 0, mongoOpCompressed), make([]byte, 24)), 1},
		{"legacy OP_QUERY is not tracked", cat(mongoHeader(40, 7, 0, mongoOpQuery), make([]byte, 24)), 0},
		{"responseTo set means it is a reply", cat(mongoHeader(40, 8, 7, mongoOpMsg), make([]byte, 24)), 0},
		{"shorter than a header", mongoHeader(40, 7, 0, mongoOpMsg)[:15], 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsMongoQuery(c.payload))
		})
	}
}

func TestMongoResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		partial bool
		want    int
	}{
		{"length-only read asks for the rest", le32(40), false, 2},
		{"OP_MSG reply", cat(mongoHeader(40, 8, 7, mongoOpMsg), make([]byte, 24)), false, 1},
		{"OP_COMPRESSED reply", cat(mongoHeader(40, 8, 7, mongoOpCompressed), make([]byte, 24)), false, 1},
		{"responseTo 0 is a request", cat(mongoHeader(40, 8, 0, mongoOpMsg), make([]byte, 24)), false, 0},
		{"legacy OP_REPLY is not tracked", cat(mongoHeader(40, 8, 7, mongoOpReply), make([]byte, 24)), false, 0},
		// After a length-only read the next read starts at requestID.
		{"rest of a partial read", cat(le32(8), le32(7), le32(mongoOpMsg), make([]byte, 24)), true, 1},
		{"rest of a partial read, not a reply", cat(le32(8), le32(0), le32(mongoOpMsg), make([]byte, 24)), true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsMongoResponse(c.payload, c.partial))
		})
	}
}

// --- Kafka ---

func kafkaRequest(apiKey, apiVersion uint16, correlationID uint32, extra int) []byte {
	body := cat(be16(apiKey), be16(apiVersion), be32(correlationID), make([]byte, extra))
	return cat(be32(uint32(len(body))), body)
}

func TestKafkaRequest(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
		reqID   int32
	}{
		{"Produce", kafkaRequest(0, 9, 7, 20), 1, 7},
		{"highest accepted api key (67)", kafkaRequest(67, 0, 8, 4), 1, 8},
		{"api keys above 67 are not tracked", kafkaRequest(68, 0, 9, 4), 0, Unset},
		{"negative api key", kafkaRequest(0xffff, 0, 9, 4), 0, Unset},
		{"correlation id 0 is not tracked", kafkaRequest(0, 9, 0, 4), 0, Unset},
		{"negative correlation id", kafkaRequest(0, 9, 0x80000000, 4), 0, Unset},
		{"length does not match the write", cat(kafkaRequest(0, 9, 7, 4), []byte{0}), 0, Unset},
		{"shorter than a header", kafkaRequest(0, 9, 7, 0)[:11], 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, id := IsKafkaRequest(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.reqID, id)
		})
	}
}

func TestKafkaResponse(t *testing.T) {
	resp := cat(be32(12), be32(7), make([]byte, 8))
	assert.Equal(t, 1, IsKafkaResponse(resp, 7))
	assert.Equal(t, 0, IsKafkaResponse(resp, 8))
}

// --- Cassandra ---

func cqlFrame(version, flags byte, stream uint16, opcode byte, body int) []byte {
	return cat([]byte{version, flags}, be16(stream), []byte{opcode}, be32(uint32(body)), make([]byte, body))
}

const (
	cqlRequestV4  = 0x04
	cqlResponseV4 = 0x84
)

func TestCassandraRequest(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"QUERY", cqlFrame(cqlRequestV4, 0, 5, 0x07, 10), 1},
		{"EXECUTE", cqlFrame(cqlRequestV4, 0, 5, 0x0A, 10), 1},
		{"BATCH", cqlFrame(cqlRequestV4, 0, 5, 0x0D, 10), 1},
		{"PREPARE is not tracked", cqlFrame(cqlRequestV4, 0, 5, 0x09, 10), 0},
		{"STARTUP is not tracked", cqlFrame(cqlRequestV4, 0, 5, 0x01, 10), 0},
		{"protocol v3 is not tracked", cqlFrame(0x03, 0, 5, 0x07, 10), 0},
		{"protocol v5 is not tracked", cqlFrame(0x05, 0, 5, 0x07, 10), 0},
		{"response frame", cqlFrame(cqlResponseV4, 0, 5, 0x08, 10), 0},
		{"shorter than a header", cqlFrame(cqlRequestV4, 0, 5, 0x07, 0)[:5], 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stream := IsCassandraRequest(c.payload)
			assert.Equal(t, c.want, got)
			if c.want == 1 {
				assert.Equal(t, rawBE16(5), stream)
			}
		})
	}
}

func TestCassandraResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
		status  int32
	}{
		{"RESULT", cqlFrame(cqlResponseV4, 0, 5, 0x08, 10), 1, StatusOK},
		{"ERROR", cqlFrame(cqlResponseV4, 0, 5, 0x00, 10), 1, StatusFailed},
		{"READY is not a query result", cqlFrame(cqlResponseV4, 0, 5, 0x02, 0), 0, Unset},
		{"request frame", cqlFrame(cqlRequestV4, 0, 5, 0x08, 10), 0, Unset},
		{"shorter than a header", cqlFrame(cqlResponseV4, 0, 5, 0x08, 0)[:5], 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stream, status := IsCassandraResponse(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
			if c.want == 1 {
				assert.Equal(t, rawBE16(5), stream)
			}
		})
	}
}

func TestCassandraRequestResponseStreamIDsMatch(t *testing.T) {
	_, req := IsCassandraRequest(cqlFrame(cqlRequestV4, 0, 300, 0x07, 4))
	_, resp, _ := IsCassandraResponse(cqlFrame(cqlResponseV4, 0, 300, 0x08, 4))
	_, other, _ := IsCassandraResponse(cqlFrame(cqlResponseV4, 0, 301, 0x08, 4))
	assert.Equal(t, req, resp)
	assert.NotEqual(t, req, other)
}

// --- RabbitMQ (AMQP 0-9-1) ---

func amqpMethodFrame(frameType byte, class, method uint16, args []byte, end byte) []byte {
	payload := cat(be16(class), be16(method), args)
	return cat([]byte{frameType}, be16(1), be32(uint32(len(payload))), payload, []byte{end})
}

func TestRabbitmq(t *testing.T) {
	publishArgs := []byte{0, 0, 0, 3, 'q', '_', '1', 0}
	publish := amqpMethodFrame(1, 60, 40, publishArgs, 0xCE)
	deliver := amqpMethodFrame(1, 60, 60, publishArgs, 0xCE)
	contentHeader := cat([]byte{2}, be16(1), be32(3), []byte{0, 60, 0}, []byte{0xCE})

	cases := []struct {
		name             string
		payload          []byte
		produce, consume int
	}{
		{"basic.publish", publish, 1, 0},
		{"basic.publish followed by content frames", cat(publish, contentHeader), 1, 0},
		{"basic.deliver", deliver, 0, 1},
		{"wrong frame end", amqpMethodFrame(1, 60, 40, publishArgs, 0x00), 0, 0},
		{"content header frame", contentHeader, 0, 0},
		{"queue class", amqpMethodFrame(1, 50, 40, publishArgs, 0xCE), 0, 0},
		{"basic.get", amqpMethodFrame(1, 60, 70, publishArgs, 0xCE), 0, 0},
		{"size larger than the buffer", publish[:len(publish)-2], 0, 0},
		{"shorter than 12 bytes", publish[:11], 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.produce, IsRabbitmqProduce(c.payload))
			assert.Equal(t, c.consume, IsRabbitmqConsume(c.payload))
		})
	}
}

// --- NATS ---

func TestNatsMethod(t *testing.T) {
	cases := []struct {
		name, payload string
		want          int
	}{
		{"PUB", "PUB foo 5\r\nhello\r\n", MethodProduce},
		{"PUB with tab", "PUB\tfoo 5\r\nhello\r\n", MethodProduce},
		{"HPUB", "HPUB foo 12 17\r\nNATS/1.0\r\n\r\nhello\r\n", MethodProduce},
		{"MSG", "MSG foo 1 5\r\nhello\r\n", MethodConsume},
		{"HMSG", "HMSG foo 1 12 17\r\nNATS/1.0\r\n\r\nhello\r\n", MethodConsume},
		{"SUB is not tracked", "SUB foo 1\r\n", 0},
		{"PUBLISH is not PUB", "PUBLISH foo\r\n", 0},
		{"no trailing CRLF", "PUB foo 5\r\nhello", 0},
		{"shorter than 7 bytes", "PING\r\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, NatsMethod([]byte(c.payload)))
		})
	}
}

func TestNatsPublishLongerThanPayloadLimit(t *testing.T) {
	// BUG: nats_method (nats.c:11-12) checks for the trailing CRLF at the size
	// capped to MAX_PAYLOAD_SIZE-1, so messages over ~1 KB are not recognized — unskip when fixed
	t.Skip("BUG: NATS messages longer than MAX_PAYLOAD_SIZE-1 are not recognized (CRLF read at the truncated offset)")
	assert.Equal(t, MethodProduce, NatsMethod(endsWithCRLF("PUB foo 2000\r\n", 2000)))
}

// --- Dubbo2 ---

func dubboHeader(flag, status byte) []byte {
	h := make([]byte, 16)
	h[0], h[1], h[2], h[3] = 0xda, 0xbb, flag, status
	return h
}

const (
	dubboRequestTwoWay = 0x80 | 0x40 | 0x02 // request, two-way, serialization id 2
	dubboResponse      = 0x02
)

func TestDubbo2Request(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"two-way request", dubboHeader(dubboRequestTwoWay, 0), 1},
		{"one-way request is not tracked", dubboHeader(0x80|0x02, 0), 0},
		{"heartbeat event", dubboHeader(dubboRequestTwoWay|0x20, 0), 0},
		{"serialization id 0", dubboHeader(0x80|0x40, 0), 0},
		{"response flags", dubboHeader(dubboResponse, 20), 0},
		{"wrong magic", cat([]byte{0xda, 0xbc}, dubboHeader(dubboRequestTwoWay, 0)[2:]), 0},
		{"shorter than a header", dubboHeader(dubboRequestTwoWay, 0)[:15], 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsDubbo2Request(c.payload))
		})
	}
}

func TestDubbo2Response(t *testing.T) {
	for _, s := range []byte{30, 31, 40, 50, 60, 70, 80, 90, 100} {
		got, status := IsDubbo2Response(dubboHeader(dubboResponse, s))
		assert.Equal(t, 1, got, "status byte %d", s)
		assert.Equal(t, StatusFailed, status, "status byte %d", s)
	}
	cases := []struct {
		name    string
		payload []byte
		want    int
		status  int32
	}{
		{"OK", dubboHeader(dubboResponse, 20), 1, StatusOK},
		{"unlisted status byte is still a response", dubboHeader(dubboResponse, 25), 1, StatusUnknown},
		{"request frame", dubboHeader(dubboRequestTwoWay, 20), 0, Unset},
		{"heartbeat response", dubboHeader(dubboResponse|0x20, 20), 0, Unset},
		{"serialization id 0", dubboHeader(0x00, 20), 0, Unset},
		{"wrong magic", cat([]byte{0xdb, 0xbb}, dubboHeader(dubboResponse, 20)[2:]), 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsDubbo2Response(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
}

// --- DNS ---

func dnsHeader(id uint16, bits0, bits1 byte, qdcount uint16) []byte {
	return cat(be16(id), []byte{bits0, bits1}, be16(qdcount), make([]byte, 6))
}

func TestDNSRequest(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"standard query", dnsHeader(0x1234, 0x01, 0x00, 1), 1},
		{"response bit set", dnsHeader(0x1234, 0x81, 0x80, 1), 0},
		{"non-zero opcode (UPDATE)", dnsHeader(0x1234, 0x28, 0x00, 1), 0},
		{"two questions", dnsHeader(0x1234, 0x01, 0x00, 2), 0},
		{"no questions", dnsHeader(0x1234, 0x01, 0x00, 0), 0},
		{"shorter than a header", dnsHeader(0x1234, 0x01, 0x00, 1)[:5], 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stream := IsDNSRequest(c.payload)
			assert.Equal(t, c.want, got)
			if c.want == 1 {
				assert.Equal(t, rawBE16(0x1234), stream)
			}
		})
	}
}

func TestDNSResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
		status  int32
	}{
		{"NOERROR", dnsHeader(0x1234, 0x81, 0x80, 1), 1, 0},
		{"SERVFAIL", dnsHeader(0x1234, 0x81, 0x82, 1), 1, 2},
		{"NXDOMAIN", dnsHeader(0x1234, 0x81, 0x83, 1), 1, 3},
		{"query is not a response", dnsHeader(0x1234, 0x01, 0x00, 1), 0, Unset},
		{"NOTIFY opcode", dnsHeader(0x1234, 0xA0, 0x80, 1), 0, Unset},
		{"two questions", dnsHeader(0x1234, 0x81, 0x80, 2), 0, Unset},
		{"shorter than a header", dnsHeader(0x1234, 0x81, 0x80, 1)[:5], 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stream, status := IsDNSResponse(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
			if c.want == 1 {
				assert.Equal(t, rawBE16(0x1234), stream)
			}
		})
	}
}

// --- ClickHouse ---

func TestClickhouseQuery(t *testing.T) {
	uuid := []byte("0f8c5b2e-4a51-4d3e-9b1c-7a6e2d3f4b5c")
	assert.Len(t, uuid, 36)
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"initial query, empty query id", []byte{1, 0, 1}, 1},
		{"secondary query, empty query id", []byte{1, 0, 2}, 1},
		{"initial query with a UUID query id", cat([]byte{1, 36}, uuid, []byte{1}), 1},
		{"custom query id that is not 36 bytes", cat([]byte{1, 5}, []byte("abcde"), []byte{1}), 0},
		{"not a Query packet", []byte{2, 0, 1}, 0},
		{"no query kind", []byte{1, 0, 0}, 0},
		{"unknown query kind after a UUID", cat([]byte{1, 36}, uuid, []byte{3}), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsClickhouseQuery(c.payload))
		})
	}
}

func TestClickhouseResponse(t *testing.T) {
	cases := []struct {
		name   string
		code   byte
		want   int
		status int32
	}{
		{"Data", 1, 1, StatusOK},
		{"EndOfStream", 5, 1, StatusOK},
		{"Exception", 2, 1, StatusFailed},
		{"Progress", 3, 0, Unset},
		{"Pong", 4, 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsClickhouseResponse([]byte{c.code, 0, 0})
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
}

// --- ZooKeeper ---

func zkRequest(xid, op int32, body int) []byte {
	rest := cat(be32(uint32(xid)), be32(uint32(op)), make([]byte, body))
	return cat(be32(uint32(len(rest))), rest)
}

func zkReplyBody(xid int32, zxid int64, errCode int32) []byte {
	return cat(be32(uint32(xid)), be64(uint64(zxid)), be32(uint32(errCode)))
}

func zkResponse(xid int32, zxid int64, errCode int32) []byte {
	rest := zkReplyBody(xid, zxid, errCode)
	return cat(be32(uint32(len(rest))), rest)
}

func TestZKRequest(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"getData", zkRequest(1, 4, 8), 1},
		{"create", zkRequest(2, 1, 8), 1},
		{"notification op 0", zkRequest(3, 0, 0), 1},
		{"createContainer (19)", zkRequest(4, 19, 8), 1},
		{"createTTL (21)", zkRequest(5, 21, 8), 1},
		{"closeSession (-11)", zkRequest(6, -11, 0), 1},
		{"ping uses xid -2", zkRequest(-2, 11, 0), 1},
		{"watch notification xid -1", zkRequest(-1, 0, 0), 1},
		{"op 20 is not whitelisted", zkRequest(7, 20, 8), 0},
		{"op 22 is not whitelisted", zkRequest(8, 22, 8), 0},
		{"other negative xids are rejected", zkRequest(-3, 4, 8), 0},
		{"length does not match the write", cat(zkRequest(1, 4, 8), []byte{0}), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsZKRequest(c.payload))
		})
	}
}

func TestZKRequestAuthAndSetWatches(t *testing.T) {
	// BUG: is_zk_request (zookeeper.c:38-45) whitelists ZK_OP_SET_AUTH (100) and
	// ZK_OP_SET_WATCHES (101), but ZooKeeper clients send those with the reserved
	// xids -4 (AUTHPACKET_XID) and -8 (SET_WATCHES_XID), which the xid check
	// rejects first, so the whitelist entries can never match — unskip when fixed
	t.Skip("BUG: ZooKeeper setAuth/setWatches requests are whitelisted by op code but always rejected by the xid check")
	assert.Equal(t, 1, IsZKRequest(zkRequest(-4, 100, 8)))
	assert.Equal(t, 1, IsZKRequest(zkRequest(-8, 101, 8)))
}

func TestZKResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		partial bool
		want    int
		status  int32
	}{
		{"length-only read asks for the rest", be32(16), false, 2, Unset},
		{"OK", zkResponse(1, 100, 0), false, 1, 0},
		{"NONODE (-101)", zkResponse(1, 100, -101), false, 1, -101},
		{"lowest accepted error (-123)", zkResponse(1, 100, -123), false, 1, -123},
		{"ping reply xid -2", zkResponse(-2, 100, 0), false, 1, 0},
		{"rest of a partial read", zkReplyBody(1, 100, -101), true, 1, -101},
		{"other negative xids are rejected", zkResponse(-3, 100, 0), false, 0, Unset},
		{"length does not match the read", cat(zkResponse(1, 100, 0), []byte{0}), false, 0, Unset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsZKResponse(c.payload, c.partial)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.status, status)
		})
	}
	// The status is written before the range check, so rejected replies still set it.
	got, status := IsZKResponse(zkResponse(1, 100, -124), false)
	assert.Equal(t, 0, got, "errors below -123 are not recognized")
	assert.Equal(t, int32(-124), status)
	got, status = IsZKResponse(zkResponse(1, 100, 1), false)
	assert.Equal(t, 0, got, "positive error codes are not recognized")
	assert.Equal(t, int32(1), status)
}

// --- FoundationDB ---

const (
	fdbGetValueRequest       = 8454530
	fdbStorageServerIface    = 15302073
	fdbGetValueReply         = 1378929
	fdbVoid                  = 2010442
	fdbErrorOrWrapper uint32 = 2 << 24
)

func fdbPacket(fileID uint32, tls bool) []byte {
	b := make([]byte, 36)
	off := 32
	if tls {
		off = 24
	}
	copy(b[off:], le32(fileID))
	return b
}

func fdbConnectPacket() []byte {
	b := make([]byte, 24)
	copy(b, le32(20))
	b[10], b[11] = 0xDB, 0x1F // protocol version carries 0x1FDB at offset 10
	return b
}

func TestFoundationDBRequest(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"known request", fdbPacket(fdbGetValueRequest, false), 1},
		{"known request, TLS layout", fdbPacket(fdbGetValueRequest, true), 1},
		{"server interface id", fdbPacket(fdbStorageServerIface, false), 1},
		{"unknown file id", fdbPacket(12345, false), 0},
		{"reply id is not a request", fdbPacket(fdbGetValueReply, false), 0},
		{"ConnectPacket alone", fdbConnectPacket(), 0},
		{"ConnectPacket then a request", cat(fdbConnectPacket(), fdbPacket(fdbGetValueRequest, false)), 1},
		{"ConnectPacket then an unknown packet", cat(fdbConnectPacket(), fdbPacket(12345, false)), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsFoundationDBRequest(c.payload))
		})
	}
}

func TestFoundationDBResponse(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    int
	}{
		{"known reply wrapped in ErrorOr", fdbPacket(fdbErrorOrWrapper|fdbGetValueReply, false), 1},
		{"Void reply", fdbPacket(fdbErrorOrWrapper|fdbVoid, false), 1},
		{"known reply, TLS layout", fdbPacket(fdbErrorOrWrapper|fdbGetValueReply, true), 1},
		{"unknown reply", fdbPacket(fdbErrorOrWrapper|12345, false), 0},
		{"ConnectPacket alone asks for more", fdbConnectPacket(), 2},
		{"ConnectPacket then a reply", cat(fdbConnectPacket(), fdbPacket(fdbErrorOrWrapper|fdbGetValueReply, false)), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := IsFoundationDBResponse(c.payload)
			assert.Equal(t, c.want, got)
			assert.Equal(t, StatusOK, status, "status is set before classification")
		})
	}
}

// --- all classifiers ---

// TestClassifiersOnHostileInput feeds every classifier deterministic garbage
// and checks it only ever returns one of its documented values. A classifier
// that reads out of bounds crashes the test binary.
func TestClassifiersOnHostileInput(t *testing.T) {
	inputs := [][]byte{
		nil, {0}, {0xff},
		bytes.Repeat([]byte{0}, MaxPayloadSize),
		bytes.Repeat([]byte{0xff}, MaxPayloadSize),
		bytes.Repeat([]byte("\r\n"), MaxPayloadSize/2),
		bytes.Repeat([]byte{0xff}, 3*MaxPayloadSize),
	}
	rnd := rand.New(rand.NewSource(20260928))
	for i := 0; i < 500; i++ {
		b := make([]byte, rnd.Intn(2*MaxPayloadSize))
		rnd.Read(b)
		inputs = append(inputs, b)
	}

	in := func(v int, allowed ...int) bool {
		for _, a := range allowed {
			if v == a {
				return true
			}
		}
		return false
	}
	for i, b := range inputs {
		r, _ := IsHTTPResponse(b)
		pq, _ := IsPostgresQuery(b)
		pr, _ := IsPostgresResponse(b)
		rr, _ := IsRedisResponse(b)
		mr, _ := IsMemcachedResponse(b)
		myq, _ := IsMysqlQuery(b)
		myr, _, _ := IsMysqlResponse(b, mysqlComStmtPrepare)
		kq, _ := IsKafkaRequest(b)
		cq, _ := IsCassandraRequest(b)
		cr, _, _ := IsCassandraResponse(b)
		dr, _ := IsDubbo2Response(b)
		nq, _ := IsDNSRequest(b)
		nr, _, _ := IsDNSResponse(b)
		chr, _ := IsClickhouseResponse(b)
		zr, _ := IsZKResponse(b, false)
		zrp, _ := IsZKResponse(b, true)
		fr, _ := IsFoundationDBResponse(b)

		binary := map[string]int{
			"http request": IsHTTPRequest(b), "http response": r,
			"postgres query": pq, "postgres response": pr,
			"redis query": IsRedisQuery(b), "redis response": rr,
			"memcached query": IsMemcachedQuery(b), "memcached response": mr,
			"mysql query": myq, "mysql response": myr,
			"mongo query": IsMongoQuery(b), "mongo response (partial)": IsMongoResponse(b, true),
			"kafka request": kq, "kafka response": IsKafkaResponse(b, 1),
			"cassandra request": cq, "cassandra response": cr,
			"rabbitmq produce": IsRabbitmqProduce(b), "rabbitmq consume": IsRabbitmqConsume(b),
			"http2 client": LooksLikeHTTP2Frame(b, MethodHTTP2ClientFrames), "http2 server": LooksLikeHTTP2Frame(b, MethodHTTP2ServerFrames),
			"dubbo2 request": IsDubbo2Request(b), "dubbo2 response": dr,
			"dns request": nq, "dns response": nr,
			"clickhouse query": IsClickhouseQuery(b), "clickhouse response": chr,
			"zk request": IsZKRequest(b), "zk response (partial)": zrp,
			"foundationdb request": IsFoundationDBRequest(b),
		}
		for name, v := range binary {
			assert.True(t, in(v, 0, 1), "input #%d: %s returned %d", i, name, v)
		}
		assert.True(t, in(IsMongoResponse(b, false), 0, 1, 2), "input #%d: mongo response", i)
		assert.True(t, in(zr, 0, 1, 2), "input #%d: zk response", i)
		assert.True(t, in(fr, 0, 1, 2), "input #%d: foundationdb response", i)
		assert.True(t, in(NatsMethod(b), 0, MethodProduce, MethodConsume), "input #%d: nats", i)
	}
}
