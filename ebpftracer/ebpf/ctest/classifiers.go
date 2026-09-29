// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package ctest

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/..

#include "host_shim.h"

#include "l7/http.c"
#include "l7/postgres.c"
#include "l7/redis.c"
#include "l7/memcached.c"
#include "l7/mysql.c"
#include "l7/mongo.c"
#include "l7/kafka.c"
#include "l7/cassandra.c"
#include "l7/rabbitmq.c"
#include "l7/nats.c"
#include "l7/http2.c"
#include "l7/dubbo2.c"
#include "l7/dns.c"
#include "l7/clickhouse.c"
#include "l7/zookeeper.c"
#include "l7/foundationdb.c"

// Classifiers read at fixed offsets without always checking the size (and some
// read a couple of bytes before buf), so each payload is copied into zeroed
// memory with room on both sides.
#define CT_FRONT_PAD 16
#define CT_BACK_PAD  (MAX_PAYLOAD_SIZE + 64)

static char *ct_buf(const unsigned char *data, unsigned long long len, char **base) {
    *base = calloc(1, CT_FRONT_PAD + len + CT_BACK_PAD);
    if (len) {
        memcpy(*base + CT_FRONT_PAD, data, len);
    }
    return *base + CT_FRONT_PAD;
}

#define CT_CALL(call) do {                    \
    char *base;                               \
    char *buf = ct_buf(data, len, &base);     \
    int r = (call);                           \
    free(base);                               \
    return r;                                 \
} while (0)

#define CT_ARGS const unsigned char *data, unsigned long long len

static int ct_is_http_request(CT_ARGS) { CT_CALL(is_http_request(buf)); }
static int ct_is_http_response(CT_ARGS, int *status) { CT_CALL(is_http_response(buf, status)); }
static int ct_is_postgres_query(CT_ARGS, unsigned char *rt) { CT_CALL(is_postgres_query(buf, len, rt)); }
static int ct_is_postgres_response(CT_ARGS, int *status) { CT_CALL(is_postgres_response(buf, len, status)); }
static int ct_is_redis_query(CT_ARGS) { CT_CALL(is_redis_query(buf, len)); }
static int ct_is_redis_response(CT_ARGS, int *status) { CT_CALL(is_redis_response(buf, len, status)); }
static int ct_is_memcached_query(CT_ARGS) { CT_CALL(is_memcached_query(buf, len)); }
static int ct_is_memcached_response(CT_ARGS, int *status) { CT_CALL(is_memcached_response(buf, len, status)); }
static int ct_is_mysql_query(CT_ARGS, unsigned char *rt) { CT_CALL(is_mysql_query(buf, len, rt)); }
static int ct_is_mysql_response(CT_ARGS, unsigned char rt, unsigned int *stmt, int *status) { CT_CALL(is_mysql_response(buf, len, rt, stmt, status)); }
static int ct_is_mongo_query(CT_ARGS) { CT_CALL(is_mongo_query(buf, len)); }
static int ct_is_mongo_response(CT_ARGS, unsigned char partial) { CT_CALL(is_mongo_response(buf, len, partial)); }
static int ct_is_kafka_request(CT_ARGS, int *id) { CT_CALL(is_kafka_request(buf, len, id)); }
static int ct_is_kafka_response(CT_ARGS, int id) { CT_CALL(is_kafka_response(buf, id)); }
static int ct_is_cassandra_request(CT_ARGS, short *stream) { CT_CALL(is_cassandra_request(buf, len, stream)); }
static int ct_is_cassandra_response(CT_ARGS, short *stream, int *status) { CT_CALL(is_cassandra_response(buf, len, stream, status)); }
static int ct_is_rabbitmq_produce(CT_ARGS) { CT_CALL(is_rabbitmq_produce(buf, len)); }
static int ct_is_rabbitmq_consume(CT_ARGS) { CT_CALL(is_rabbitmq_consume(buf, len)); }
static int ct_nats_method(CT_ARGS) { CT_CALL(nats_method(buf, len)); }
static int ct_looks_like_http2_frame(CT_ARGS, unsigned char method) { CT_CALL(looks_like_http2_frame(buf, len, method)); }
static int ct_is_dubbo2_request(CT_ARGS) { CT_CALL(is_dubbo2_request(buf, len)); }
static int ct_is_dubbo2_response(CT_ARGS, int *status) { CT_CALL(is_dubbo2_response(buf, status)); }
static int ct_is_dns_request(CT_ARGS, short *stream) { CT_CALL(is_dns_request(buf, len, stream)); }
static int ct_is_dns_response(CT_ARGS, short *stream, int *status) { CT_CALL(is_dns_response(buf, len, stream, status)); }
static int ct_is_clickhouse_query(CT_ARGS) { CT_CALL(is_clickhouse_query(buf, len)); }
static int ct_is_clickhouse_response(CT_ARGS, int *status) { CT_CALL(is_clickhouse_response(buf, status)); }
static int ct_is_zk_request(CT_ARGS) { CT_CALL(is_zk_request(buf, len)); }
static int ct_is_zk_response(CT_ARGS, int *status, unsigned char partial) { CT_CALL(is_zk_response(buf, len, status, partial)); }
static int ct_is_foundationdb_request(CT_ARGS) { CT_CALL(is_foundationdb_request(buf, len)); }
static int ct_is_foundationdb_response(CT_ARGS, int *status) { CT_CALL(is_foundationdb_response(buf, len, status)); }
*/
import "C"

import "unsafe"

const (
	StatusUnknown = int32(C.STATUS_UNKNOWN)
	StatusOK      = int32(C.STATUS_OK)
	StatusFailed  = int32(C.STATUS_FAILED)

	MethodUnknown           = int(C.METHOD_UNKNOWN)
	MethodProduce           = int(C.METHOD_PRODUCE)
	MethodConsume           = int(C.METHOD_CONSUME)
	MethodStatementPrepare  = int(C.METHOD_STATEMENT_PREPARE)
	MethodStatementClose    = int(C.METHOD_STATEMENT_CLOSE)
	MethodHTTP2ClientFrames = int(C.METHOD_HTTP2_CLIENT_FRAMES)
	MethodHTTP2ServerFrames = int(C.METHOD_HTTP2_SERVER_FRAMES)

	MaxPayloadSize = int(C.MAX_PAYLOAD_SIZE)

	// Unset is the initial value of every out-parameter, so tests can tell
	// whether a classifier wrote it.
	Unset = -1 << 20
)

func cbuf(b []byte) (*C.uchar, C.ulonglong) {
	if len(b) == 0 {
		return nil, 0
	}
	return (*C.uchar)(unsafe.Pointer(&b[0])), C.ulonglong(len(b))
}

func IsHTTPRequest(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_http_request(p, n))
}

func IsHTTPResponse(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_http_response(p, n, &status)), int32(status)
}

func IsPostgresQuery(b []byte) (int, byte) {
	p, n := cbuf(b)
	var rt C.uchar
	return int(C.ct_is_postgres_query(p, n, &rt)), byte(rt)
}

func IsPostgresResponse(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_postgres_response(p, n, &status)), int32(status)
}

func IsRedisQuery(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_redis_query(p, n))
}

func IsRedisResponse(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_redis_response(p, n, &status)), int32(status)
}

func IsMemcachedQuery(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_memcached_query(p, n))
}

func IsMemcachedResponse(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_memcached_response(p, n, &status)), int32(status)
}

func IsMysqlQuery(b []byte) (int, byte) {
	p, n := cbuf(b)
	var rt C.uchar
	return int(C.ct_is_mysql_query(p, n, &rt)), byte(rt)
}

func IsMysqlResponse(b []byte, requestType byte) (ret int, statementID uint32, status int32) {
	p, n := cbuf(b)
	var stmt C.uint
	st := C.int(Unset)
	r := C.ct_is_mysql_response(p, n, C.uchar(requestType), &stmt, &st)
	return int(r), uint32(stmt), int32(st)
}

func IsMongoQuery(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_mongo_query(p, n))
}

func IsMongoResponse(b []byte, partial bool) int {
	p, n := cbuf(b)
	return int(C.ct_is_mongo_response(p, n, cbool(partial)))
}

func IsKafkaRequest(b []byte) (int, int32) {
	p, n := cbuf(b)
	id := C.int(Unset)
	return int(C.ct_is_kafka_request(p, n, &id)), int32(id)
}

func IsKafkaResponse(b []byte, requestID int32) int {
	p, n := cbuf(b)
	return int(C.ct_is_kafka_response(p, n, C.int(requestID)))
}

func IsCassandraRequest(b []byte) (int, int16) {
	p, n := cbuf(b)
	var stream C.short
	return int(C.ct_is_cassandra_request(p, n, &stream)), int16(stream)
}

func IsCassandraResponse(b []byte) (ret int, stream int16, status int32) {
	p, n := cbuf(b)
	var s C.short
	st := C.int(Unset)
	r := C.ct_is_cassandra_response(p, n, &s, &st)
	return int(r), int16(s), int32(st)
}

func IsRabbitmqProduce(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_rabbitmq_produce(p, n))
}

func IsRabbitmqConsume(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_rabbitmq_consume(p, n))
}

func NatsMethod(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_nats_method(p, n))
}

func LooksLikeHTTP2Frame(b []byte, method int) int {
	p, n := cbuf(b)
	return int(C.ct_looks_like_http2_frame(p, n, C.uchar(method)))
}

func IsDubbo2Request(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_dubbo2_request(p, n))
}

func IsDubbo2Response(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_dubbo2_response(p, n, &status)), int32(status)
}

func IsDNSRequest(b []byte) (int, int16) {
	p, n := cbuf(b)
	var stream C.short
	return int(C.ct_is_dns_request(p, n, &stream)), int16(stream)
}

func IsDNSResponse(b []byte) (ret int, stream int16, status int32) {
	p, n := cbuf(b)
	var s C.short
	st := C.int(Unset)
	r := C.ct_is_dns_response(p, n, &s, &st)
	return int(r), int16(s), int32(st)
}

func IsClickhouseQuery(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_clickhouse_query(p, n))
}

func IsClickhouseResponse(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_clickhouse_response(p, n, &status)), int32(status)
}

func IsZKRequest(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_zk_request(p, n))
}

func IsZKResponse(b []byte, partial bool) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_zk_response(p, n, &status, cbool(partial))), int32(status)
}

func IsFoundationDBRequest(b []byte) int {
	p, n := cbuf(b)
	return int(C.ct_is_foundationdb_request(p, n))
}

func IsFoundationDBResponse(b []byte) (int, int32) {
	p, n := cbuf(b)
	status := C.int(Unset)
	return int(C.ct_is_foundationdb_response(p, n, &status)), int32(status)
}

func cbool(v bool) C.uchar {
	if v {
		return 1
	}
	return 0
}
