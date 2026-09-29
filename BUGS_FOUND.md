# Bugs found by tests (test/code-coverage branch)

While adding test coverage on this branch, the tests turned up real bugs in the agent. None of them are fixed yet. Each one is pinned by a test that asserts the **correct** behavior and is currently skipped with `t.Skip("BUG: ...")`, so it doesn't fail the suite.

**To fix one:** change the code at **Where**, delete the `t.Skip` line in the **Test**, and run that test. It passes once the bug is gone and then guards against regressions.

**Each entry has:**
- **Where**: the code that's wrong, as a clickable link.
- **Test**: the skipped test that reproduces it.
- **Problem**: what the code does wrong and why, with the offending lines quoted.
- **What happens now** / **Expected**: a concrete input with the current and the correct result.
- **Impact**: what it means for the running agent.
- **Severity**: one of Security, Crash, Data race, Resource leak / hang, Wrong or missing data, Minor.
- **Fix direction**: included only where the fix is obvious.

Every entry was checked against the current code. Line numbers are a snapshot. Some packages (`logs`, `node`, `pinger`, `proc`, `profiling`) are still being edited, so lines there may shift. To list every pinned bug at any time, run:

```sh
grep -rn 't.Skip("BUG' --include='*_test.go' .
```

The `ebpftracer/ebpf/ctest` tests need cgo and a C compiler. The `gpu` tests need `-tags gpu`.

<!-- common/ and containers/ sections go here -->

## containers/

### A systemd bus that rejects the agent's uid crashes the agent at startup
- **Where:** [containers/systemd.go:37-39](containers/systemd.go#L37-L39) in the dialer passed to `dbus.NewConnection` by `systemdInit` (run from the package `init()`)
- **Test:** [containers/systemd_test.go:196](containers/systemd_test.go#L196) `TestSystemdInitAuthRejected` — skipped; remove the `t.Skip` once fixed
- **Problem:** When authentication on `/run/systemd/private` fails, the dialer closes `dbusConn` instead of the connection it just opened. `dbusConn` is only assigned after `dbus.NewConnection` returns, so it is still nil here.
```go
methods := []gdbus.Auth{gdbus.AuthExternal(strconv.Itoa(os.Getuid()))}
if err = c.Auth(methods); err != nil {
	dbusConn.Close()
	return nil, err
}
```
- **What happens now:** a bus that answers `AUTH EXTERNAL` with `REJECTED` makes `(*dbus.Conn).Close` dereference nil, and the package `init()` panics.
- **Expected:** the failed connection `c` is closed, `dbusConn` stays nil and the agent starts without systemd `TriggeredBy` lookups.
- **Impact:** the agent crashes before `main` on hosts where the systemd private socket is reachable but refuses the agent's uid (for example a non-root agent with the socket mounted). The rejected gdbus connection also leaks.
- **Severity:** Crash
- **Fix direction:** call `c.Close()` instead of `dbusConn.Close()`.

## ebpftracer/ (Go side of the tracer)

### A conntrack entry with no protocol number crashes the agent at startup
- **Where:** [ebpftracer/init.go:40-46](ebpftracer/init.go#L40-L46) in `ipTupleValid`, dereferenced at [ebpftracer/init.go:80](ebpftracer/init.go#L80) in `getConntrack`
- **Test:** [ebpftracer/init_test.go:43](ebpftracer/init_test.go#L43) `TestIpTupleValidNilProtoNumber` — skipped; remove the `t.Skip` once fixed
- **Problem:** `ipTupleValid` rejects conntrack tuples with missing fields (`Src`, `Dst`, `Proto`, ports) but not a missing `Proto.Number` (a `*uint8`), which `getConntrack` dereferences right after the check passes.
```go
if t.Src == nil || t.Dst == nil || t.Proto == nil {
	return false
}
if t.Proto.SrcPort == nil || t.Proto.DstPort == nil {
	return false
}
return true
```
```go
if *conn.Origin.Proto.Number != IPProtoTCP {
```
- **What happens now:** a conntrack entry that has ports but no protocol number passes the check, and the dereference panics with a nil pointer.
- **Expected:** `ipTupleValid` returns false, so the entry is skipped.
- **Impact:** `getConntrack` runs at startup for the host and every network namespace, so one such netlink message crashes the agent. The kernel normally sends the protocol number, so this is unlikely in practice.
- **Severity:** Crash
- **Fix direction:** add `t.Proto.Number == nil` to the nil checks.

### The OpenSSL version is reported as `"v"` when none is found
- **Where:** [ebpftracer/tls.go:303-308](ebpftracer/tls.go#L303-L308) in `getSslLibPathAndVersion`
- **Test:** [ebpftracer/tls_test.go:120](ebpftracer/tls_test.go#L120) `TestGetSslLibPathAndVersionUnknownFlavor` — skipped; remove the `t.Skip` once fixed
- **Problem:** The function looks for an `OpenSSL x.y.z` string in libcrypto and always returns `"v" + version`, so when nothing is found it returns `"v"` rather than `""`. The caller, `AttachOpenSslUprobes`, treats `""` as "not supported, skip quietly", so that check never fires.
```go
	if m := opensslVersionRe.FindStringSubmatch(s); len(m) > 1 {
		version = m[1]
	}
}
return libsslPath, "v" + version
```
- **What happens now:** a process using LibreSSL or BoringSSL gets version `"v"`; the caller then fails to parse it and logs an error (`libssl_version=v: failed to determine version`).
- **Expected:** `""`, so the process is skipped without an error.
- **Impact:** one error-level log line per process using a non-OpenSSL TLS library. No uprobes are attached, which is the intended outcome anyway.
- **Severity:** Minor
- **Fix direction:** return `""` when `version` is empty, `"v" + version` otherwise.

### The Go TLS write uprobe leaks when `crypto/tls.(*Conn).Read` is missing
- **Where:** [ebpftracer/tls.go:218-221](ebpftracer/tls.go#L218-L221) in `AttachGoTlsUprobes`
- **Test:** [ebpftracer/tls_test.go:381](ebpftracer/tls_test.go#L381) `TestAttachGoTlsUprobesWithoutReadSymbol` — skipped; remove the `t.Skip` once fixed
- **Problem:** The write uprobe is attached first. If the Read symbol lookup then fails, the function returns without `closeLinks()`, unlike every later error path in the same function.
```go
links = append(links, l)

rs, err := ef.GetSymbol(goTlsReadSymbol)
if err != nil {
	log("failed to get symbol", err)
	return nil, isGolangApp
}
```
- **What happens now:** for a Go app that writes to TLS connections but never reads from one (so the linker drops `(*Conn).Read`), the `crypto/tls.(*Conn).Write` uprobe stays attached, but the caller gets `nil` and can never close it.
- **Expected:** the write link is closed before returning, as on the read-entry and read-exit failure paths.
- **Impact:** one leaked uprobe (a perf event plus its fd) per such process, kept for the lifetime of the agent and never detached when the process exits or is rescanned. The probe keeps firing into `go_crypto_tls_write_enter`.
- **Severity:** Resource leak / hang
- **Fix direction:** call `closeLinks()` before that `return`.

## ebpftracer/l7/ (Go protocol parsers)

These parse payloads captured in the kernel, which copies at most 1024 bytes of each read or write. Payloads can be cut short and can come from any remote peer.

### The ClickHouse parser allocates string lengths taken from the payload
- **Where:** [ebpftracer/l7/clickhouse.go:19-26](ebpftracer/l7/clickhouse.go#L19-L26) in `ParseClickhouse`
- **Test:** [ebpftracer/l7/clickhouse_test.go:176](ebpftracer/l7/clickhouse_test.go#L176) `TestClickhouseParseHugeStringLength` — skipped; remove the `t.Skip` once fixed
- **Problem:** Strings are decoded with ch-go's `Reader.Str` (directly and inside `ClientInfo.DecodeAware`). ch-go reads the length prefix and allocates `make([]byte, n)` before checking that `n` bytes exist. The payload is at most 1024 bytes, but the length can be up to 2^63-1.
```go
if _, err = r.Str(); err != nil {
	return ""
}
version := int(proto.FeatureServerQueryTimeInProgress)
info := proto.ClientInfo{}
if err = info.DecodeAware(r, version); err != nil {
	return ""
}
```
- **What happens now:** the 12-byte payload `01 00 01 ff ff ff ff ff ff ff ff 7f` passes the kernel classifier, declares a string length of 2^63-1, and `make` panics (`makeslice: len out of range`). A merely large length, e.g. 2^36, tries to allocate that much memory. Nothing recovers the panic.
- **Expected:** `""`, with no panic and no allocation larger than the payload.
- **Impact:** any peer that can send bytes on a connection that looks like ClickHouse can crash the agent or get it OOM-killed.
- **Severity:** Security
- **Fix direction:** read length-prefixed strings with a helper that rejects any length larger than the remaining payload before allocating.

### ClickHouse queries that carry any setting are never parsed
- **Where:** [ebpftracer/l7/clickhouse.go:33-42](ebpftracer/l7/clickhouse.go#L33-L42) in `ParseClickhouse`
- **Test:** [ebpftracer/l7/clickhouse_test.go:97](ebpftracer/l7/clickhouse_test.go#L97) `TestClickhouseParseWithSettings` — skipped; remove the `t.Skip` once fixed
- **Problem:** The settings loop reuses one `proto.Setting` and stops when `s.Key == ""`. ch-go's `Setting.Decode` returns early on the empty end-of-settings key without clearing `s.Key`, so after one real setting the key stays non-empty and the loop reads past the end of the settings until a decode error.
```go
var s proto.Setting

for {
	if err = s.Decode(r); err != nil {
		return ""
	}
	if s.Key == "" {
		break
	}
}
```
- **What happens now:** `SELECT 1` sent with `max_threads=4` returns `""`.
- **Expected:** `"SELECT 1"`.
- **Impact:** queries from clients that send settings, which is most real clients, are never recorded.
- **Severity:** Wrong or missing data
- **Fix direction:** declare `var s proto.Setting` inside the loop, or reset it before each `Decode`.

### DNS responses cut by the capture limit are dropped entirely
- **Where:** [ebpftracer/l7/dns.go:15-18](ebpftracer/l7/dns.go#L15-L18) in `ParseDns`
- **Test:** [ebpftracer/l7/dns_test.go:140](ebpftracer/l7/dns_test.go#L140) `TestDnsParseResponseTruncatedByCapture` — skipped; remove the `t.Skip` once fixed
- **Problem:** The whole message is unpacked at once and any error discards it. When the capture cuts a large response inside the answer section, the question is still complete, but unpacking fails and nothing is returned.
```go
var msg dnsmessage.Message
if err := msg.Unpack(payload); err != nil {
	return "", "", nil
}
```
- **What happens now:** an A response for `big.example.com` with 80 answers, cut to 1023 bytes, returns `("", "", nil)`.
- **Expected:** `"TypeA"`, `"big.example.com"`, and the IPs that were fully captured.
- **Impact:** DNS lookups with responses over ~1 KB (EDNS, many records) are missing from DNS metrics.
- **Severity:** Wrong or missing data
- **Fix direction:** use `dnsmessage.Parser`: read the header and question, then answers one by one until an error.

### HTTP/2 HEADERS frames with the PADDED or PRIORITY flag are decoded wrongly
- **Where:** [ebpftracer/l7/http2.go:139-143](ebpftracer/l7/http2.go#L139-L143) in `(*Http2Parser).Parse`
- **Test:** [ebpftracer/l7/http2_test.go:292](ebpftracer/l7/http2_test.go#L292) `TestHttp2HeadersWithPriorityAndPadding` — skipped; remove the `t.Skip` once fixed
- **Problem:** A HEADERS frame can start with a 1-byte pad length (PADDED, flag 0x8) and a 5-byte priority block (PRIORITY, flag 0x20), and end with padding (RFC 7540 §6.2). The parser ignores the flags and feeds the whole frame, extra fields included, to the HPACK decoder.
```go
next := offset + h.Length
if next > len(payload) {
	next = len(payload)
}
if _, err := decoder.Write(payload[offset:next]); err != nil {
	continue
}
```
- **What happens now:** a `GET /prio` HEADERS frame with padding and a priority block decodes to an empty method and path.
- **Expected:** the extra fields are stripped first, giving `GET` and `/prio`.
- **Impact:** requests from clients that set priority or padding lose their method and path, and the garbage bytes can put bogus entries in the connection's HPACK table, corrupting later requests on it.
- **Severity:** Wrong or missing data
- **Fix direction:** check `h.Flags` for `FlagHeadersPadded` / `FlagHeadersPriority` and trim before `decoder.Write`.

### HTTP/2 CONTINUATION frames are skipped
- **Where:** [ebpftracer/l7/http2.go:94-100](ebpftracer/l7/http2.go#L94-L100) in `(*Http2Parser).Parse`
- **Test:** [ebpftracer/l7/http2_test.go:316](ebpftracer/l7/http2_test.go#L316) `TestHttp2Continuation` — skipped; remove the `t.Skip` once fixed
- **Problem:** A header block can be split across a HEADERS frame and one or more CONTINUATION frames (RFC 7540 §6.10). Every frame that isn't HEADERS is skipped, so the rest of the block never reaches the decoder.
```go
if h.Type != http2.FrameHeaders {
	if len(payload)-offset < h.Length {
		break
	}
	offset += h.Length
	continue
}
```
- **What happens now:** a header block split inside the `:path` value gives `GET` with an empty path.
- **Expected:** CONTINUATION payloads for the same stream are decoded too, giving the full path.
- **Impact:** requests with large header blocks lose their path and other headers, and HPACK table entries in the skipped part are lost, so later requests on that connection can decode wrongly.
- **Severity:** Wrong or missing data
- **Fix direction:** treat `FrameContinuation` like HEADERS for the same stream.

### The HTTP parser writes `...` into the caller's buffer
- **Where:** [ebpftracer/l7/http.go:19-22](ebpftracer/l7/http.go#L19-L22) in `ParseHttp`
- **Test:** [ebpftracer/l7/http_test.go:79](ebpftracer/l7/http_test.go#L79) `TestHttpParseDoesNotModifyInput` — skipped; remove the `t.Skip` once fixed
- **Problem:** When the request line was cut before the space after the URI, `uri` is a sub-slice ending at `len(payload)` but with spare capacity in the caller's array, so `append` writes `...` into the caller's memory instead of allocating.
```go
uri, _, ok := bytes.Cut(rest, space)
if !ok {
	uri = append(uri, []byte("...")...)
}
```
- **What happens now:** with backing array `GET /partial-uriXXXXXXXX` and `payload` its first 16 bytes, the result `/partial-uri...` is right, but the array becomes `GET /partial-uri...XXXXX`.
- **Expected:** the same result, with the caller's buffer untouched.
- **Impact:** the tracer passes a sub-slice of the event record, so 3 bytes of event data that may be read later are silently overwritten.
- **Severity:** Wrong or missing data
- **Fix direction:** build a new string, e.g. `string(uri) + "..."`.

### Memcached `delete <key>` is not recognized
- **Where:** [ebpftracer/l7/memcached.go:24-27](ebpftracer/l7/memcached.go#L24-L27) in `ParseMemcached`
- **Test:** [ebpftracer/l7/memcached_test.go:48](ebpftracer/l7/memcached_test.go#L48) `TestMemcachedParseDeleteWithoutNoreply` — skipped; remove the `t.Skip` once fixed
- **Problem:** For storage, delete, incr/decr and touch commands, the key is taken only if a space follows it. The usual form `delete <key>\r\n` has nothing after the key.
```go
case "set", "add", "cas", "append", "prepend", "replace", "delete", "incr", "decr", "touch":
	if key, _, ok := bytes.Cut(rest, space); ok {
		return command, []string{string(key)}
	}
```
- **What happens now:** `delete k\r\n` → `("", nil)`.
- **Expected:** `("delete", ["k"])`.
- **Impact:** Memcached deletes in their common form (no `noreply`) are not recorded.
- **Severity:** Wrong or missing data
- **Fix direction:** end the key at the first space or `\r\n`, whichever comes first.

### The MySQL parser panics on a packet whose length field is 0
- **Where:** [ebpftracer/l7/mysql.go:38-44](ebpftracer/l7/mysql.go#L38-L44) in `(*MysqlParser).Parse` (the `readQuery` closure)
- **Test:** [ebpftracer/l7/mysql_test.go:149](ebpftracer/l7/mysql_test.go#L149) `TestMysqlZeroLengthPacketDoesNotPanic` — skipped; remove the `t.Skip` once fixed
- **Problem:** The query always starts at index 5 but ends at `4 + length`. With length 0 the end (4) is before the start (5).
```go
to := mysqlMsgHeaderSize + msgSize
partial := false
if to > payloadSize {
	to = payloadSize
	partial = true
}
query = string(payload[mysqlMsgHeaderSize+1 : to])
```
- **What happens now:** `[0,0,0,0, 0x03, 'S','E','L','E']` panics with `slice bounds out of range [5:4]`, with no recover in the call path.
- **Expected:** `""`, no panic.
- **Impact:** latent crash. Today the kernel classifier (`length+4 == size`, `size >= 5`) stops such packets reaching Go, but any change to it would expose this.
- **Severity:** Crash
- **Fix direction:** return `""` when `to <= mysqlMsgHeaderSize+1`.

## ebpftracer/ebpf/l7/ (kernel-side C protocol classifiers)

These C functions run inside the kernel on every captured read/write and decide which protocol a payload belongs to. If a classifier says "no", the event is dropped before any Go code sees it, so each bug below means missing requests, statuses or latencies with no error anywhere. They are tested on the host through `ebpftracer/ebpf/ctest` (needs cgo and a C compiler).

Four of them share one root cause, so it's explained once here. `TRUNCATE_PAYLOAD_SIZE` caps a size at 1023 (`MAX_PAYLOAD_SIZE - 1`):

```c
// ebpftracer/ebpf/l7/l7.c:31-34
#define TRUNCATE_PAYLOAD_SIZE(size) ({                                  \
    size = MIN(size, MAX_PAYLOAD_SIZE-1);                               \
    asm volatile ("%0 &= %1" : "+r"(size) : "i"(MAX_PAYLOAD_SIZE-1));   \
})
```

Several classifiers apply it to the size of the whole read/write and then look for the message terminator at `buf + size - 2`. For anything longer than 1023 bytes that reads bytes 1021-1022 in the middle of the message instead of its real end. The kernel passes the full syscall size (`ret` in `trace_exit_read`, [l7.c:462-464](ebpftracer/ebpf/l7/l7.c#L462-L464)), so this is hit by every message over ~1 KB.

### Memcached `TOUCHED` replies are never recognized
- **Where:** [ebpftracer/ebpf/l7/memcached.c:73-76](ebpftracer/ebpf/l7/memcached.c#L73-L76) in `is_memcached_response`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:417](ebpftracer/ebpf/ctest/classifiers_test.go#L417) `TestMemcachedResponseTouched` — skipped; remove the `t.Skip` once fixed
- **Problem:** The check for `TOUCHED` compares the first three bytes with `T`,`O`,`C`, but the reply starts with `T`,`O`,`U`. The branch can never match.
```c
if (r[0] == 'T' && r[1] == 'O' && r[2] == 'C') { //TOUCHED
    *status = STATUS_OK;
    return 1;
}
```
- **What happens now:** `TOUCHED\r\n` → not a Memcached response (returns 0).
- **Expected:** returns 1 with `STATUS_OK`.
- **Impact:** every `touch` command is left without a response status or latency.
- **Severity:** Wrong or missing data
- **Fix direction:** compare `r[2] == 'U'`.

### Memcached cache misses (`END`) are never recognized
- **Where:** [ebpftracer/ebpf/l7/memcached.c:61-100](ebpftracer/ebpf/l7/memcached.c#L61-L100) in `is_memcached_response`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:427](ebpftracer/ebpf/ctest/classifiers_test.go#L427) `TestMemcachedResponseCacheMiss` — skipped; remove the `t.Skip` once fixed
- **Problem:** A `get`/`gets` for a key that doesn't exist replies with just `END\r\n`. The list of recognized replies (`VALUE`, `STORED`, `DELETED`, `NOT_…`, `EXISTS`, errors, numbers) has no case for `END`.
```c
if (r[0] == 'V' && r[1] == 'A' && r[2] == 'L') { //VALUE
    *status = STATUS_OK;
    return 1;
}
// ... STORED, DELETED, TOUCHED, NOT_*, EXISTS, ERROR, CLIENT_ERROR, SERVER_ERROR, digits — no END
```
- **What happens now:** `END\r\n` → not a Memcached response (returns 0).
- **Expected:** returns 1 with `STATUS_OK` (a miss is a successful request).
- **Impact:** every cache miss is left without a response status or latency, which also skews hit/miss analysis.
- **Severity:** Wrong or missing data
- **Fix direction:** add `if (r[0] == 'E' && r[1] == 'N' && r[2] == 'D')` returning `STATUS_OK`.

### Postgres replies that start with ParseComplete + BindComplete are not recognized
- **Where:** [ebpftracer/ebpf/l7/postgres.c:44-56](ebpftracer/ebpf/l7/postgres.c#L44-L56) in `is_postgres_response`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:211](ebpftracer/ebpf/ctest/classifiers_test.go#L211) `TestPostgresResponseParseAndBindComplete` — skipped; remove the `t.Skip` once fixed
- **Problem:** The classifier skips one leading `ParseComplete` (`1`) or `BindComplete` (`2`) message and looks at the next one. The normal reply to Parse+Bind+Execute+Sync starts with both, so after skipping `1` it lands on `2`, which is not in the accepted list.
```c
if ((cmd == '1' || cmd == '2') && length == 4 && buf_size >= 10) {
    bpf_read(buf+5, cmd);
    bpf_read(buf+5+1, length);
}
if (cmd == 'E') { ... return 1; }
if (cmd == 't' || cmd == 'T' || cmd == 'D' || cmd == 'C') { ... return 1; }
return 0;
```
- **What happens now:** `1` `2` `T` … → not a Postgres response (returns 0).
- **Expected:** returns 1 with `STATUS_OK`.
- **Impact:** queries sent through the extended protocol with an unnamed statement (for example JDBC, before it switches to server-side prepared statements) are left without a status or latency.
- **Severity:** Wrong or missing data
- **Fix direction:** skip both a leading `1` and a following `2` before checking the message type.

### Postgres extended-protocol queries over ~1 KB are not recognized
- **Where:** [ebpftracer/ebpf/l7/postgres.c:24-30](ebpftracer/ebpf/l7/postgres.c#L24-L30) in `is_postgres_query`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:174](ebpftracer/ebpf/ctest/classifiers_test.go#L174) `TestPostgresExtendedQueryLongerThanPayloadLimit` — skipped; remove the `t.Skip` once fixed
- **Problem:** Extended-protocol batches are recognized by the `Sync` message at their end. The code looks for it at the capped size (see the note at the top of this section), not at the real end.
```c
char sync[5];
TRUNCATE_PAYLOAD_SIZE(buf_size);
bpf_read(buf+buf_size-5, sync);
if (sync[0] == 'S' && sync[1] == 0 && sync[2] == 0 && sync[3] == 0 && sync[4] == 4) {
    return 1;
}
```
- **What happens now:** Parse(1.5 KB query)+Bind+Execute+Sync → not a Postgres query (returns 0). Simple `Q` queries of any size are fine, because they are recognized by their length field instead.
- **Expected:** returns 1 with request type `P`.
- **Impact:** long queries sent through the extended protocol (most drivers) are not traced at all.
- **Severity:** Wrong or missing data

### Redis replies over ~1 KB are not recognized
- **Where:** [ebpftracer/ebpf/l7/redis.c:29-34](ebpftracer/ebpf/l7/redis.c#L29-L34) in `is_redis_response`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:339](ebpftracer/ebpf/ctest/classifiers_test.go#L339) `TestRedisResponseLongerThanPayloadLimit` — skipped; remove the `t.Skip` once fixed
- **Problem:** A reply is accepted only if it ends with `\r\n`, but that is checked at the capped size (see the note at the top of this section), not at the real end.
```c
char end[2];
TRUNCATE_PAYLOAD_SIZE(buf_size);
bpf_read(buf+buf_size-2, end);
if (end[0] != '\r' || end[1] != '\n') {
    return 0;
}
```
- **What happens now:** a 4 KB bulk reply (`$4000\r\n…\r\n`) → not a Redis response (returns 0).
- **Expected:** returns 1 with `STATUS_OK`.
- **Impact:** requests with large replies (big values, `LRANGE`, `HGETALL`, `KEYS`) get no status or latency.
- **Severity:** Wrong or missing data

### Memcached requests over ~1 KB are not recognized
- **Where:** [ebpftracer/ebpf/l7/memcached.c:9-14](ebpftracer/ebpf/l7/memcached.c#L9-L14) in `is_memcached_query`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:382](ebpftracer/ebpf/ctest/classifiers_test.go#L382) `TestMemcachedQueryLongerThanPayloadLimit` — skipped; remove the `t.Skip` once fixed
- **Problem:** Same as the Redis case: the trailing `\r\n` is checked at the capped size, not at the real end. `is_memcached_response` (lines 55-60) has the same pattern.
```c
char end[2];
TRUNCATE_PAYLOAD_SIZE(buf_size);
bpf_read(buf+buf_size-2, end);
if (end[0] != '\r' || end[1] != '\n') {
    return 0;
}
```
- **What happens now:** `set k 0 0 2000\r\n` + 2000-byte value + `\r\n` → not a Memcached request (returns 0).
- **Expected:** returns 1.
- **Impact:** storing values over ~1 KB is not traced.
- **Severity:** Wrong or missing data

### NATS messages over ~1 KB are not recognized
- **Where:** [ebpftracer/ebpf/l7/nats.c:10-15](ebpftracer/ebpf/l7/nats.c#L10-L15) in `nats_method`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:660](ebpftracer/ebpf/ctest/classifiers_test.go#L660) `TestNatsPublishLongerThanPayloadLimit` — skipped; remove the `t.Skip` once fixed
- **Problem:** Same as the Redis case: the trailing `\r\n` is checked at the capped size, not at the real end.
```c
char end[2];
TRUNCATE_PAYLOAD_SIZE(buf_size);
bpf_read(buf+buf_size-2, end);
if (end[0] != '\r' || end[1] != '\n') {
    return 0;
}
```
- **What happens now:** `PUB foo 2000\r\n` + 2000-byte payload + `\r\n` → method 0 (not NATS).
- **Expected:** `METHOD_PRODUCE`. The same applies to `MSG` on the consume side.
- **Impact:** publishes and deliveries of messages over ~1 KB are not counted.
- **Severity:** Wrong or missing data

### ZooKeeper `setAuth` / `setWatches` can never be recognized
- **Where:** [ebpftracer/ebpf/l7/zookeeper.c:37-47](ebpftracer/ebpf/l7/zookeeper.c#L37-L47) in `is_zk_request`
- **Test:** [ebpftracer/ebpf/ctest/classifiers_test.go:877](ebpftracer/ebpf/ctest/classifiers_test.go#L877) `TestZKRequestAuthAndSetWatches` — skipped; remove the `t.Skip` once fixed
- **Problem:** The op-code whitelist includes `ZK_OP_SET_AUTH` (100) and `ZK_OP_SET_WATCHES` (101), but ZooKeeper clients send those packets with reserved xids `-4` (auth) and `-8` (set watches). The xid check runs first and only lets `-1` and `-2` through, so these whitelist entries are unreachable.
```c
__s32 xid = bpf_ntohl(req.xid);
if (xid < 0 && xid != -1 && xid != -2) {
    return 0;
}
...
if (op == ZK_OP_CREATE_TTL || op == ZK_OP_CLOSE || op == ZK_OP_SET_AUTH || op == ZK_OP_SET_WATCHES || op == ZK_OP_ERROR) {
    return 1;
}
```
- **What happens now:** xid `-4`, op 100 → not a ZooKeeper request (returns 0). Same for xid `-8`, op 101.
- **Expected:** returns 1, as the whitelist intends.
- **Impact:** authentication and watch re-registration (sent after every reconnect) are never traced.
- **Severity:** Minor
- **Fix direction:** also allow xids `-4` and `-8`, or drop the two op codes from the whitelist if they're not wanted.

### HTTP `TRACE` requests are dropped in the kernel
- **Where:** [ebpftracer/ebpf/l7/http.c:3-33](ebpftracer/ebpf/l7/http.c#L3-L33) in `is_http_request`
- **Test:** [ebpftracer/ebpf/ctest/consistency_test.go:119](ebpftracer/ebpf/ctest/consistency_test.go#L119) `TestHTTPClassifierAcceptsTrace` — skipped; remove the `t.Skip` once fixed
- **Problem:** The classifier accepts GET, POST, HEAD, PUT, DELETE, CONNECT, OPTIONS and PATCH, but not TRACE. The Go parser that runs afterwards (`ebpftracer/l7/http.go`) does support TRACE, so the two disagree.
```c
if (b[0] == 'P' && b[1] == 'A' && b[2] == 'T' && b[3] == 'C' && b[4] == 'H') {
    return 1;
}
return 0;   // no TRACE case before this
```
- **What happens now:** `TRACE /x HTTP/1.1` → not an HTTP request (returns 0).
- **Expected:** returns 1, matching the Go parser.
- **Impact:** TRACE requests are never traced. Rare in practice, but the Go support for them is dead code.
- **Severity:** Minor
- **Fix direction:** add a TRACE check, or remove TRACE from the Go parser if it's intentionally unsupported.

## internal/pyroscope-ebpf/ (vendored Grafana profiler)

### Python frames keep their directory when the last `/` is at index 1
- **Where:** [internal/pyroscope-ebpf/session_python.go:189-192](internal/pyroscope-ebpf/session_python.go#L189-L192) in `WalkPythonStack`
- **Test:** none. `WalkPythonStack` needs `python.LazySymbols` / `python.Proc` values that can't be built from outside the `python` package.
- **Problem:** The code is meant to cut a file path down to its base name when a `/` exists, but it compares the index with `1` instead of `-1`.
```go
iSep := strings.LastIndexByte(filename, '/')
if iSep != 1 {
    filename = filename[iSep+1:]
}
```
- **What happens now:** it works by coincidence in most cases (no slash gives `-1`, and slicing from 0 is harmless). But for a path whose last `/` is at index 1, e.g. `a/rest.py`, the condition is false and the full path is kept.
- **Expected:** `rest.py`.
- **Impact:** inconsistent frame names in Python profiles for such paths, which splits one function into two in flame graphs.
- **Severity:** Minor
- **Fix direction:** `if iSep != -1`.

### Python profiler `Load` / `LoadError` counters are never exposed
- **Where:** [internal/pyroscope-ebpf/metrics/python.go:53-62](internal/pyroscope-ebpf/metrics/python.go#L53-L62) in `NewPythonMetrics`
- **Test:** [internal/pyroscope-ebpf/metrics/metrics_test.go:103](internal/pyroscope-ebpf/metrics/metrics_test.go#L103) `TestNewPythonMetricsLoadCountersNotRegistered` — skipped; remove the `t.Skip` once fixed
- **Problem:** Eight counters are created, but only six are passed to `MustRegister`. `Load` and `LoadError` are left out, even though `session_python.go:89` and `:93` increment them.
```go
reg.MustRegister(
    m.PidDataError,
    m.LostSamples,
    m.SymbolLookup,
    m.StacktraceError,
    m.UnknownSymbols,
    m.ProcessInitSuccess,
)
```
- **What happens now:** `pyroscope_pyperf_load` and `pyroscope_pyperf_load_error_total` count, but never appear in a scrape.
- **Expected:** both are registered and scraped.
- **Impact:** failures to load the Python profiler are invisible in metrics.
- **Severity:** Wrong or missing data
- **Fix direction:** add `m.Load, m.LoadError` to the `MustRegister` call.

### `typeName` panics on type names with a double underscore
- **Where:** [internal/pyroscope-ebpf/dwarfdump/dwarfdump.go:259-267](internal/pyroscope-ebpf/dwarfdump/dwarfdump.go#L259-L267) in `typeName`
- **Test:** [internal/pyroscope-ebpf/dwarfdump/dwarfdump_test.go:40](internal/pyroscope-ebpf/dwarfdump/dwarfdump_test.go#L40) `TestTypeNameDoubleTrailingUnderscorePanics` — skipped; remove the `t.Skip` once fixed
- **Problem:** The single trailing `_` is trimmed before the double `__`, so `x__` becomes `x_` and keeps one underscore. Splitting on `_` then yields an empty part, and `parts[i][:1]` on an empty string panics. Any `__` in the middle of a name hits the same path.
```go
n = strings.TrimSuffix(n, "_")
n = strings.TrimSuffix(n, "__")
...
parts := strings.Split(n, "_")
for i := range parts {
    p1 := parts[i][:1]
```
- **What happens now:** `typeName` on `task_struct__` (or `a__b`) → panic: slice bounds out of range.
- **Expected:** returns a CamelCase name, e.g. `TaskStruct`.
- **Impact:** only affects the offline `cmd/*_dwarfdump` code generators, not the running agent. They crash on such a struct name.
- **Severity:** Minor
- **Fix direction:** skip empty parts in the loop.

## gpu/

Both files are only compiled with `-tags gpu`, so run these tests with `go test -tags gpu ./gpu/`.

### The GPU collector's `Close` panics when no NVIDIA library is present
- **Where:** [gpu/gpu.go:236-238](gpu/gpu.go#L236-L238) in `(*Collector).Close`
- **Test:** [gpu/gpu_test.go:212](gpu/gpu_test.go#L212) `TestCloseWithoutNVML` — skipped; remove the `t.Skip` once fixed
- **Problem:** When `libnvidia-ml` isn't found, `NewCollector` still returns a usable collector with `c.iface == nil`. `Close` calls `Shutdown` on it without checking.
```go
func (c *Collector) Close() {
	c.iface.Shutdown()
}
```
- **What happens now:** on a node without NVIDIA drivers, `Close()` panics with a nil pointer.
- **Expected:** `Close()` does nothing when NVML was never loaded.
- **Impact:** latent: `main.go` doesn't call `Close` today, but any shutdown path on non-GPU nodes would crash.
- **Severity:** Crash
- **Fix direction:** return early when `c.iface == nil`.

### The per-process GPU poller shares one timestamp across devices and never stops
- **Where:** [gpu/gpu.go:143-165](gpu/gpu.go#L143-L165) in `(*Collector).processUtilizationPoller`
- **Test:** [gpu/gpu_test.go:242](gpu/gpu_test.go#L242) `TestProcessUtilizationPollerPerDeviceTimestamps` — skipped; remove the `t.Skip` once fixed
- **Problem:** One `lastTs` is the "newer than" cut-off for every GPU and is overwritten by each device's samples, so a device whose newest sample is older than another device's is filtered out. The ticker loop also has no exit.
```go
ticker := time.NewTicker(1 * time.Second)
lastTs := uint64(time.Now().UnixMicro())
for range ticker.C {
	for _, dev := range c.devices {
		samples, _ := dev.device.GetProcessUtilization(lastTs)
		for _, sample := range samples {
			if sample.TimeStamp <= lastTs {
				continue
			}
			...
			lastTs = sample.TimeStamp
```
- **What happens now:** GPU-A reports a sample at `now+100` and GPU-B at `now+50`. After GPU-A, `lastTs = now+100`, so GPU-B's samples are dropped on every tick.
- **Expected:** each device keeps its own last-seen timestamp, and the poller stops when the collector is closed.
- **Impact:** on multi-GPU nodes, per-process GPU usage for some GPUs is silently lost; the goroutine and ticker also leak and would keep calling NVML after shutdown.
- **Severity:** Wrong or missing data
- **Fix direction:** store `lastTs` per device and add a stop channel that `Close` signals.

## jvm/

### JVM perf-map support ignores a later `-XX:-PreserveFramePointer`
- **Where:** [jvm/perfmap.go:24-29](jvm/perfmap.go#L24-L29) in `IsPerfmapDumpSupported`
- **Test:** [jvm/perfmap_test.go:40](jvm/perfmap_test.go#L40) `TestIsPerfmapDumpSupportedLastFlagWins` — skipped; remove the `t.Skip` once fixed
- **Problem:** The function returns true if `-XX:+PreserveFramePointer` appears anywhere in the command line. HotSpot applies `-XX` flags in order, so a later `-XX:-PreserveFramePointer` turns it off again.
```go
func IsPerfmapDumpSupported(cmdline []byte) bool {
	if !bytes.Contains(cmdline, []byte("-XX:+PreserveFramePointer")) {
		return false
	}
	return true
}
```
- **What happens now:** `java -XX:+PreserveFramePointer -XX:-PreserveFramePointer -jar a.jar` → true.
- **Expected:** false: the last occurrence wins.
- **Impact:** the profiler attaches to such JVMs to dump perf maps every cycle even though frame pointers are off, causing needless attaches and broken Java stacks.
- **Severity:** Wrong or missing data
- **Fix direction:** split the command line on `\0` and use the last `±PreserveFramePointer` argument.

## logs/

### `TailReader` loses the start of a line written in three or more pieces
- **Where:** [logs/tail_reader.go:63-72](logs/tail_reader.go#L63-L72) in `NewTailReader` (reader goroutine)
- **Test:** [logs/tail_reader_test.go:193](logs/tail_reader_test.go#L193) `TestTailReaderPartialLineThreeChunks` — skipped; remove the `t.Skip` once fixed
- **Problem:** When `ReadString` hits end-of-file before a newline, the unfinished text is kept in `prefix`. Each new piece overwrites `prefix` instead of appending to it, so a line written in three or more pieces loses every piece but the last unfinished one.
```go
line, err := r.reader.ReadString('\n')
if err != nil {
	prefix = line
	r.poll(ctx)
	continue
}
if prefix != "" {
	line = prefix + line
```
- **What happens now:** the file receives `aaa`, then `bbb`, then `ccc\n`. `prefix` becomes `aaa`, then `bbb` (`aaa` is lost), and the entry sent on is `bbbccc`.
- **Expected:** one entry, `aaabbbccc`.
- **Impact:** container log lines flushed in several pieces (long lines, slow writers) reach log parsing cut short, which damages messages and pattern detection.
- **Severity:** Wrong or missing data
- **Fix direction:** `prefix += line`.

### `TailReader.Stop` hangs forever if nobody is reading the channel
- **Where:** [logs/tail_reader.go:73-77](logs/tail_reader.go#L73-L77) in the reader goroutine, with [logs/tail_reader.go:87-88](logs/tail_reader.go#L87-L88) in `Stop`
- **Test:** [logs/tail_reader_test.go:294](logs/tail_reader_test.go#L294) `TestTailReaderStopWhileConsumerNotReading` — skipped; remove the `t.Skip` once fixed
- **Problem:** The goroutine sends each line to `r.ch` with a plain blocking send and checks `ctx.Done()` only at the top of its loop. If the consumer has stopped reading, it waits on the send forever and never signals `r.stopped`, while `Stop` cancels the context and then waits on `r.stopped` with no timeout.
```go
// reader goroutine
case <-ctx.Done():
	r.stopped <- struct{}{}
	return
...
r.ch <- logparser.LogEntry{

// Stop
r.stop()
<-r.stopped
```
- **What happens now:** with an unbuffered channel nobody reads, the goroutine blocks on `r.ch <- ...` after one line and `Stop()` never returns.
- **Expected:** `Stop()` returns promptly whether or not the channel is being drained.
- **Impact:** whatever stops a container's log reader hangs for good, the file stays open, and the reader goroutine leaks.
- **Severity:** Resource leak / hang
- **Fix direction:** send inside a `select` that also watches `ctx.Done()`.

### `NewTailReader` leaks the open file when setup fails after opening it
- **Where:** [logs/tail_reader.go:44-52](logs/tail_reader.go#L44-L52) in `NewTailReader`
- **Test:** [logs/tail_reader_test.go:337](logs/tail_reader_test.go#L337) `TestTailReaderNoFdLeakOnSetupError` — skipped only when a leak is detected (the `t.Skip` is inside `if after > before`); remove it once fixed
- **Problem:** After `os.Open` succeeds, `Stat` or `Seek` can still fail. Those paths return without closing `r.file`, and the context from `context.WithCancel` is never cancelled.
```go
if r.file, err = os.Open(fileName); err != nil {
	return nil, err
}
if r.info, err = r.file.Stat(); err != nil {
	return nil, err
}
if _, err = r.file.Seek(0, io.SeekEnd); err != nil {
	return nil, err
}
```
- **What happens now:** 10 calls on a file where `Seek` fails each return an error, and the process gains an open file descriptor on every call.
- **Expected:** each failed call closes what it opened.
- **Impact:** every failed attempt to tail a container log leaks a file descriptor. It only happens on the rare Stat/Seek failure.
- **Severity:** Resource leak / hang
- **Fix direction:** call `r.file.Close()` (and `cancel()`) before returning those errors.

### `NewJournaldReader` returns a broken reader when the last journal path fails `GetUsage`
- **Where:** [logs/journald_reader.go:57-61](logs/journald_reader.go#L57-L61) in `NewJournaldReader`, with the nil check at [logs/journald_reader.go:73-76](logs/journald_reader.go#L73-L76)
- **Test:** [logs/journald_reader_test.go:219](logs/journald_reader_test.go#L219) `TestNewJournaldReaderUsageErrorOnLastPath` — skipped; remove the `t.Skip` once fixed
- **Problem:** When `GetUsage` fails, the loop moves to the next path but leaves `r.journal` pointing at the unusable journal (the "empty journal" branch resets it to nil; this one doesn't). If it was the last path, the `r.journal == nil` check after the loop passes, and `follow()` starts on the bad journal.
```go
usage, err := r.journal.GetUsage()
if err != nil {
	klog.Errorf("failed to read journal disk space usage at %s: %s", journalPath, err)
	continue
}
```
- **What happens now:** a single path that opens but whose `GetUsage` returns "permission denied" gives a non-nil reader and a nil error.
- **Expected:** `nil` and an error saying the systemd journal was not found.
- **Impact:** on a host where `/var/log/journal` opens but can't be read, journald setup looks successful, subscriptions are accepted, and no journald logs ever arrive, with no startup error to explain why.
- **Severity:** Wrong or missing data
- **Fix direction:** close the journal and set `r.journal = nil` before `continue`.

### `NewJournaldReader` never closes journals it opens and then rejects
- **Where:** [logs/journald_reader.go:57-69](logs/journald_reader.go#L57-L69) in `NewJournaldReader`
- **Test:** [logs/journald_reader_test.go:231](logs/journald_reader_test.go#L231) `TestNewJournaldReaderClosesRejectedJournals` — skipped; remove the `t.Skip` once fixed
- **Problem:** A journal that opens but is then rejected (a `GetUsage` error, zero usage, or a `SeekRealtimeUsec` error) is dropped without `Close()`, so the underlying `sd_journal` keeps its file descriptors and memory maps.
```go
if usage == 0 {
	klog.Errorf("journal at %s is empty", journalPath)
	r.journal = nil
	continue
}
if err = r.journal.SeekRealtimeUsec(uint64(time.Now().Add(time.Millisecond).UnixNano() / 1000)); err != nil {
	return nil, err
}
```
- **What happens now:** with paths `/a` (empty), `/b` (`GetUsage` error) and `/c` (good), the reader uses `/c` and the journals for `/a` and `/b` are never closed.
- **Expected:** each rejected journal is closed exactly once.
- **Impact:** each rejected journal directory leaks file descriptors and mappings for the life of the agent. Happens once at startup, so it's small.
- **Severity:** Resource leak / hang
- **Fix direction:** call `r.journal.Close()` on each rejection path.

### `JournaldReader.Close` doesn't stop the follow goroutine
- **Where:** [logs/journald_reader.go:139-141](logs/journald_reader.go#L139-L141) in `Close`, loop at [logs/journald_reader.go:80-94](logs/journald_reader.go#L80-L94) in `follow`
- **Test:** [logs/journald_reader_test.go:365](logs/journald_reader_test.go#L365) `TestJournaldReaderCloseStopsFollow` — skipped; remove the `t.Skip` once fixed
- **Problem:** `Close` only closes the journal. The `follow()` goroutine has no stop signal and keeps calling `Next`/`Wait` on it; the `until` channel is created but never used. With the real `sdjournal` that's a use-after-free of the C `sd_journal` handle.
```go
func (r *JournaldReader) Close() {
	_ = r.journal.Close()
}
```
- **What happens now:** after `Close()`, the journal still receives `Next`/`Wait` calls.
- **Expected:** no calls reach the journal once `Close` has returned.
- **Impact:** nothing in production calls `Close` today (`containers/journald.go` never closes the reader), so it's latent, but any future shutdown path that calls it risks a crash in native code.
- **Severity:** Minor
- **Fix direction:** have `follow()` watch a done channel (e.g. the unused `until`), and make `Close` signal it and wait before closing the journal.

## node/

### `cpuStat` crashes on a short `cpu ` line in `/proc/stat`
- **Where:** [node/cpu.go:28-53](node/cpu.go#L28-L53) in `cpuStat`
- **Test:** [node/cpu_test.go:72](node/cpu_test.go#L72) `TestNodeCpuStatTruncatedLine` — skipped; remove the `t.Skip` once fixed
- **Problem:** The aggregate `cpu ` line is split into fields and `parts[1]` to `parts[8]` are read without checking how many fields there are.
```go
if strings.HasPrefix(line, "cpu ") {
	parts := strings.Fields(line)
	if stat.TotalUsage.User, err = strconv.ParseFloat(parts[1], 64); err != nil {
		return stat, err
	}
```
- **What happens now:** `cpu 1 2 3` has 4 fields, and reading `parts[4]` panics with index out of range.
- **Expected:** an error, no panic.
- **Impact:** `cpuStat` runs during every metrics scrape (`node/collector.go:201`); an unusual `/proc/stat` (old kernels, gVisor and other sandboxes) crashes the agent.
- **Severity:** Crash
- **Fix direction:** return an error when `len(parts) < 9`.

### Node memory is under-reported: kB is treated as 1000 bytes
- **Where:** [node/memory.go:28-31](node/memory.go#L28-L31) in `memoryInfo`
- **Test:** [node/memory_test.go:51](node/memory_test.go#L51) `TestMemoryInfoKibibytes` — skipped; remove the `t.Skip` once fixed
- **Problem:** `/proc/meminfo` says "kB" but the values are KiB (1024 bytes); the kernel prints `pages << (PAGE_SHIFT - 10)`. The code multiplies by 1000.
```go
mul := float64(1)
if len(parts) == 3 && parts[2] == "kB" {
	mul = 1000
}
```
- **What happens now:** `MemTotal: 1024 kB` gives 1,024,000 bytes. Free, Available and Cached are off the same way.
- **Expected:** 1,048,576 bytes (×1024).
- **Impact:** every `node_resources_memory_*_bytes` metric is about 2.3% low (roughly 1.5 GB short on a 64 GB host), while container (cgroup) memory is in real bytes, so container-to-node ratios are wrong.
- **Severity:** Wrong or missing data
- **Fix direction:** `mul = 1024`, and update `TestNode_memory`, which currently expects the ×1000 values.

### The metadata HTTP helper leaks the response body on non-200 replies
- **Where:** [node/metadata/metadata.go:124-130](node/metadata/metadata.go#L124-L130) in `httpCallWithTimeout`
- **Test:** [node/metadata/metadata_test.go:156](node/metadata/metadata_test.go#L156) `TestHttpCallWithTimeoutClosesBodyOnNon200` — skipped; remove the `t.Skip` once fixed (its message cites `metadata.go:122`; the code has since moved)
- **Problem:** On a non-200 status the function returns an error and drops `resp` without closing its body, and the caller never sees `resp`, so nobody can.
```go
resp, err := client.Do(r)
if err != nil {
	return nil, err
}
if resp.StatusCode != 200 {
	return nil, fmt.Errorf("metadata service response: %s", resp.Status)
}
```
- **What happens now:** a 404 from the metadata server returns an error and the body stays open.
- **Expected:** the body is closed before returning the error.
- **Impact:** every non-200 metadata reply (e.g. a 404 on a path a provider doesn't support) leaks a connection and its buffers.
- **Severity:** Resource leak / hang
- **Fix direction:** `resp.Body.Close()` before returning.

### The metadata HTTP helper changes the global default HTTP client timeout
- **Where:** [node/metadata/metadata.go:122-123](node/metadata/metadata.go#L122-L123) in `httpCallWithTimeout`
- **Test:** [node/metadata/metadata_test.go:169](node/metadata/metadata_test.go#L169) `TestHttpCallWithTimeoutDoesNotMutateDefaultClient` — skipped; remove the `t.Skip` once fixed (its message cites `metadata.go:116-117`; the code has since moved)
- **Problem:** `client := http.DefaultClient` copies the pointer, not the client, so setting `client.Timeout` changes the shared `http.DefaultClient` used by the whole process.
```go
client := http.DefaultClient
client.Timeout = metadataServiceTimeout
```
- **What happens now:** `http.DefaultClient.Timeout` goes from 0 to 5 s after one call and stays that way.
- **Expected:** it stays 0.
- **Impact:** after cloud metadata is fetched, every other user of `http.DefaultClient` in the agent gets a 5 s timeout it never asked for, and the unsynchronized write can race with goroutines using the client.
- **Severity:** Wrong or missing data
- **Fix direction:** use a dedicated `&http.Client{Timeout: metadataServiceTimeout}` or a request context with a deadline.


## pinger/

### Received packet length is ignored when parsing an ICMP reply
- **Where:** [pinger/pinger.go:208-213](pinger/pinger.go#L208-L213) in `extractEchoFromPacket`
- **Test:** [pinger/pinger_test.go:107](pinger/pinger_test.go#L107) `TestExtractEchoFromPacketHonoursLength` — skipped; remove the `t.Skip` once fixed
- **Problem:** The function checks that `n` (the number of bytes actually received) covers the IPv4 header, then ignores `n` and parses everything after byte 20 of the whole 1024-byte receive buffer. Bytes that were never received (zeros in a fresh buffer) get decoded as the ICMP message.
```go
if n < ipv4.HeaderLen {
	return nil, errors.New("malformed IPv4 packet")
}
pktBuf = pktBuf[ipv4.HeaderLen:]
var m *icmp.Message
m, err := icmp.ParseMessage(protocolICMP, pktBuf)
```
- **What happens now:** a 20-byte read with an IP header and no ICMP data (`n = 20`, 1024-byte buffer) parses 1004 zero bytes. ICMP type 0 is "echo reply", so the result is `&icmp.Echo{ID: 0, Seq: 0, ...}` with no error.
- **Expected:** an error and a nil echo, because nothing past `n` was received.
- **Impact:** truncated or empty replies are decoded as fake echo replies with ID 0. `Ping` drops them because the ID doesn't match, except when the agent's PID is a multiple of 65536, which makes `pingerID` 0 and the fake reply is accepted.
- **Severity:** Minor
- **Fix direction:** parse `pktBuf[ipv4.HeaderLen:n]` instead of `pktBuf[ipv4.HeaderLen:]`.

### The IPv4 header is assumed to be 20 bytes, so IP options break reply parsing
- **Where:** [pinger/pinger.go:208-213](pinger/pinger.go#L208-L213) in `extractEchoFromPacket`
- **Test:** [pinger/pinger_test.go:117](pinger/pinger_test.go#L117) `TestExtractEchoFromPacketWithIPOptions` — skipped; remove the `t.Skip` once fixed
- **Problem:** A raw `ip4:icmp` socket returns the full IPv4 header. Its real length is the IHL field (low 4 bits of byte 0) × 4, which is more than 20 bytes when IP options are present. The code always skips a fixed 20 bytes (`ipv4.HeaderLen`), so with options it reads them as the start of the ICMP message (same code as the entry above).
- **What happens now:** an echo reply with IHL = 6 (a 24-byte header whose last 4 bytes are NOP options `0x01`) is parsed from byte 20. The first "ICMP" byte is `0x01` (type 1, not echo reply), so the function returns `nil, nil` and the reply is silently dropped.
- **Expected:** skip `IHL*4` bytes and return the echo reply.
- **Impact:** a target whose replies carry IP options never gets an RTT and looks unreachable. Rare, since the agent doesn't send options itself.
- **Severity:** Wrong or missing data
- **Fix direction:** read `ihl := int(pktBuf[0]&0x0f) * 4`, check `20 <= ihl <= n`, and parse `pktBuf[ihl:n]`.

### The timestamp control message is matched with `||` instead of `&&`
- **Where:** [pinger/pinger.go:155-161](pinger/pinger.go#L155-L161) in `getTimestampFromOutOfBandData`
- **Test:** [pinger/pinger_test.go:185](pinger/pinger_test.go#L185) `TestGetTimestampFromOutOfBandDataSkipsOtherSocketMessages` — skipped; remove the `t.Skip` once fixed
- **Problem:** The loop should pick the control message whose level is `SOL_SOCKET` **and** whose type is `SO_TIMESTAMPING`. With `||`, it takes the first message matching either condition and decodes its data as a 48-byte `scm_timestamping` struct.
```go
for _, cm := range cms {
	if cm.Header.Level == syscall.SOL_SOCKET || cm.Header.Type == syscall.SO_TIMESTAMPING {
		var t unix.ScmTimestamping
		if err := binary.Read(bytes.NewBuffer(cm.Data), binary.LittleEndian, &t); err != nil {
			return time.Time{}, err
		}
		return time.Unix(t.Ts[0].Unix()), nil
```
- **What happens now:** if a 12-byte `SOL_SOCKET/SCM_CREDENTIALS` message comes before the real `SO_TIMESTAMPING` one, it's picked first, `binary.Read` fails with `unexpected EOF`, and the real timestamp is never reached. If the other message were 48 bytes or more, its bytes would come back as a wrong timestamp with no error.
- **Expected:** skip non-matching messages and return the timestamp from the `SO_TIMESTAMPING` message.
- **Impact:** `receive` fails with "failed to get RX timestamp" and drops the reply, or computes an RTT from garbage.
- **Severity:** Wrong or missing data
- **Fix direction:** change `||` to `&&`.

### A send-side EAGAIN aborts the whole ping round instead of skipping one target
- **Where:** [pinger/pinger.go:79-84](pinger/pinger.go#L79-L84) in `Ping`
- **Test:** [pinger/pinger_test.go:413](pinger/pinger_test.go#L413) `TestPingSendWrappedEAGAINSkipsTarget` — skipped; remove the `t.Skip` once fixed
- **Problem:** EAGAIN ("resource temporarily unavailable") is meant to skip just that target, and `Ping` detects it by checking whether the error text starts with that phrase. But `send` returns the error from `net.IPConn.WriteTo`, which is always wrapped in a `*net.OpError` whose text starts with `write ip4 ...`, so the check never matches. (The same check on the TX-timestamp error at lines 86-87 does work, because `syscall.Recvmsg` returns the bare error.)
```go
if err := send(conn, pkt.seq, ip.IPAddr()); err != nil {
	if strings.HasPrefix(err.Error(), "resource temporarily unavailable") {
		continue
	}
	return nil, fmt.Errorf("failed to send packet to %s: %s", ip, err)
}
```
- **What happens now:** the error text is `write ip4 10.0.0.1: sendto: resource temporarily unavailable`, so `Ping` returns `failed to send packet to 10.0.0.1: ...` and a nil result.
- **Expected:** skip that target and carry on with no error.
- **Impact:** one EAGAIN on send throws away the RTTs of every target in that network namespace for that round.
- **Severity:** Wrong or missing data
- **Fix direction:** use `errors.Is(err, syscall.EAGAIN)`, which works through the wrapping, at both call sites.

### `Ping` waits the full timeout when a target was skipped
- **Where:** [pinger/pinger.go:98-105](pinger/pinger.go#L98-L105) in `Ping`
- **Test:** [pinger/pinger_test.go:454](pinger/pinger_test.go#L454) `TestPingSkippedTargetDoesNotWaitForTimeout` — skipped; remove the `t.Skip` once fixed
- **Problem:** `Ping` should return early once every sent packet has been answered, but it compares the number of replies with the number of targets instead of the number of packets actually sent (`len(ids)`). A target skipped on EAGAIN is never added to `ids` and can never reply, so the counts never match.
```go
for {
	select {
	case <-timeoutTicker.C:
		return rttByIp, nil
	default:
		if len(rttByIp) == len(targets) {
			return rttByIp, nil
		}
```
- **What happens now:** two targets with a 2 s timeout; one is skipped (TX timestamp returns EAGAIN) and the other replies right away. 1 result ≠ 2 targets, so `Ping` keeps polling until the 2 s timer fires.
- **Expected:** return as soon as all sent packets are answered: well under 1 s, with 1 result.
- **Impact:** every round with a skipped target blocks for the full timeout and delays the next measurement cycle.
- **Severity:** Minor
- **Fix direction:** compare with `len(ids)` instead of `len(targets)`.


## proc/

### `ReadFds` logs the harmless `Readlink` error and hides the real ones
- **Where:** [proc/fd.go:39-45](proc/fd.go#L39-L45) in `ReadFds`
- **Test:** [proc/fd_test.go:75](proc/fd_test.go#L75) `TestReadFdsWarnsOnUnexpectedReadlinkError` — skipped; remove the `t.Skip` once fixed
- **Problem:** The logging condition is inverted. An fd that disappears between `ReadDir` and `Readlink` (ENOENT) is normal and is the only case that logs; real failures such as EACCES or EINVAL are skipped silently.
```go
dest, err := os.Readlink(path.Join(fdDir, entry.Name()))
if err != nil {
	if os.IsNotExist(err) {
		klog.Warningf("failed to read link '%s': %s", entry.Name(), err)
	}
	continue
```
- **What happens now:** an fd entry that isn't a symlink makes `Readlink` return EINVAL; it's skipped with no log. Short-lived fds that vanish produce warnings.
- **Expected:** ENOENT ignored silently; any other error logged.
- **Impact:** fds skipped for real reasons are invisible, while normal process churn fills the logs.
- **Severity:** Minor
- **Fix direction:** `if !os.IsNotExist(err)`.

### `GetNsPid` fails for processes in nested PID namespaces
- **Where:** [proc/proc.go:47-56](proc/proc.go#L47-L56) in `GetNsPid`
- **Test:** [proc/proc_test.go:158](proc/proc_test.go#L158) `TestGetNsPidNestedNamespaces` — skipped; remove the `t.Skip` once fixed
- **Problem:** The `NSpid:` line lists the pid once per nested PID namespace, outermost first; the pid in the process's own namespace is the last field. The code accepts only one or two pids and errors on three or more (docker-in-docker, sysbox and similar).
```go
if fields[0] == "NSpid:" {
	var f string
	switch len(fields) {
	case 2:
		f = fields[1]
	case 3:
		f = fields[2]
	default:
		return 0, errors.New("invalid NSpid value")
```
- **What happens now:** `NSpid:	6	60	1` → `invalid NSpid value`.
- **Expected:** `1` (the last field).
- **Impact:** JVM attach (`jvm/jattach.go`, `containers/jvm.go`) and .NET monitoring (`containers/dotnet.go`) fail in nested containers, so their data is missing.
- **Severity:** Wrong or missing data
- **Fix direction:** use `fields[len(fields)-1]`.

### `ExecuteInNetNs` hands a thread stuck in the wrong network namespace back to the Go runtime
- **Where:** [proc/ns.go:44-54](proc/ns.go#L44-L54) in `ExecuteInNetNs`
- **Test:** [proc/ns_test.go:153](proc/ns_test.go#L153) `TestExecuteInNetNsRestoreErrorKeepsThreadLocked` — skipped; remove the `t.Skip` once fixed
- **Problem:** The function locks the OS thread, switches it to `newNs`, runs `f`, then switches back. If switching back fails, the deferred `UnlockOSThread` still runs, and the thread, still in `newNs`, goes back to the scheduler's pool where unrelated goroutines can run on it.
```go
runtime.LockOSThread()
defer runtime.UnlockOSThread()
if err := setNetNs(newNs); err != nil {
	return err
}

errF := f()

if err := setNetNs(curNs); err != nil {
	return err
}
```
- **What happens now:** the switch back fails with "operation not permitted"; the error is returned and the thread is unlocked while still in the target namespace.
- **Expected:** the thread stays locked, so the runtime ends it when the goroutine exits.
- **Impact:** unrelated agent goroutines (metrics export, conntrack, HTTP clients) can silently run in the host's or a container's network namespace. Callers include `containers/registry.go`, `pinger/pinger.go`, `ebpftracer/tracer.go` and the AWS/IBM metadata code.
- **Severity:** Wrong or missing data
- **Fix direction:** drop the `defer`, and unlock only after the switch back succeeds.

## profiling/

### `TargetFinder` writes `now` without holding its lock
- **Where:** [profiling/profiling.go:277-279](profiling/profiling.go#L277-L279) in `Update`, and [profiling/profiling.go:129](profiling/profiling.go#L129) in `Start`
- **Test:** [profiling/profiling_test.go:353](profiling/profiling_test.go#L353) `TestTargetFinderUpdateRacesFindTarget` — skipped; remove the `t.Skip` once fixed
- **Problem:** `FindTarget` reads `tf.now` under `tf.lock`, but `Update` and `Start` write it without the lock. `Update` runs on the collect goroutine; `FindTarget` runs on the profiling session's pid-info and pid-exec goroutines, so they overlap.
```go
func (tf *TargetFinder) Update(_ sd.TargetsOptions) {
	tf.now = time.Now().UnixNano()
}
```
- **What happens now:** `go test -race` reports a data race when `Update` and `FindTarget` run concurrently.
- **Expected:** every access to `tf.now` is guarded by `tf.lock`.
- **Impact:** an unsynchronized read and write on every collect cycle; `FindTarget` can see a stale or torn value, which affects the "old enough to profile" check and the once-per-cycle JVM perf-map dump.
- **Severity:** Data race
- **Fix direction:** take `tf.lock` in `Update`, and call `targetFinder.Update(...)` from `Start` instead of writing the field.

## prom/

### An explicit `+Inf` histogram bucket is sent twice
- **Where:** [prom/remote_writer.go:352-361](prom/remote_writer.go#L352-L361) in `addTimeseries`
- **Test:** [prom/remote_writer_test.go:192](prom/remote_writer_test.go#L192) `TestBuildWriteRequestExplicitInfBucket` — skipped; remove the `t.Skip` once fixed
- **Problem:** The code writes one `_bucket` series per bucket, then always adds a separate `le="+Inf"` series from the total sample count. If the histogram already has a +Inf bucket, `fmt.Sprint(math.Inf(1))` is `"+Inf"` too, so that series appears twice.
```go
for _, b := range m.GetHistogram().Bucket {
	wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
		Samples: []prompb.Sample{{Timestamp: timestamp, Value: float64(b.GetCumulativeCount())}},
		Labels:  makeLabels(labels, "_bucket", fmt.Sprint(b.GetUpperBound())),
	})
}
wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
	Samples: []prompb.Sample{{Timestamp: timestamp, Value: float64(m.Histogram.GetSampleCount())}},
	Labels:  makeLabels(labels, "_bucket", "+Inf"),
```
- **What happens now:** a histogram with buckets `{le=1: 2, le=+Inf: 3}` and sample count 3 produces `h_bucket{le="+Inf"}` twice in one write request.
- **Expected:** exactly one `h_bucket{le="+Inf"}` series.
- **Impact:** the remote-write payload carries a duplicate series with the same timestamp, which the receiver may reject or store twice. Only collectors that emit an explicit +Inf bucket (e.g. const histograms whose bucket map includes +Inf) trigger it.
- **Severity:** Wrong or missing data
- **Fix direction:** skip buckets whose upper bound is +Inf inside the loop, or only add the extra series when the last bucket isn't already +Inf.

### A permanent HTTP 4xx rejection blocks the metrics spool queue
- **Where:** [prom/remote_writer.go:160-162](prom/remote_writer.go#L160-L162) in `send`, retried by `sendLoop` at [prom/remote_writer.go:118-131](prom/remote_writer.go#L118-L131)
- **Test:** [prom/remote_writer_test.go:585](prom/remote_writer_test.go#L585) `TestSendPermanentClientErrorDoesNotBlockQueue` — skipped; remove the `t.Skip` once fixed
- **Problem:** `send` treats every status ≥ 300 as a failure, and `sendLoop` handles every failure the same way: keep the file and retry the oldest spool file after a backoff (5 s, doubling up to 1 min). A 400/401/404 will never succeed on retry, so that file stays at the head of the queue and newer files are never sent.
```go
if resp.StatusCode >= 300 {
	return errors.New(resp.Status)
}
```
- **What happens now:** with two spool files and a server that answers 400 to the first, the first file is retried every backoff period until `truncateSpoolIfNeeded` deletes it because the spool went over `--max-spool-size`.
- **Expected:** a permanent client error (400, 401, 404) drops or parks the file so the newer one is sent next.
- **Impact:** one rejected payload stops all metric delivery from the node until the spool fills up; after that, data is still delayed and old files are deleted for size reasons rather than because they were sent.
- **Severity:** Wrong or missing data
- **Fix direction:** have `send` return a distinct "permanent" error for 4xx other than 429, and have `sendLoop` remove the file on it instead of retrying.

## tracing/

### HTTP/2 spans with status 400 are not marked as errors
- **Where:** [tracing/tracing.go:181](tracing/tracing.go#L181) in `Http2Request`
- **Test:** [tracing/tracing_test.go:204](tracing/tracing_test.go#L204) `TestHttp2Request400IsError` — skipped; remove the `t.Skip` once fixed
- **Problem:** `HttpRequest` (HTTP/1, line 152) marks a span as an error when `status >= 400`. `Http2Request` uses `status > 400`, so exactly 400 Bad Request slips through.
```go
t.createSpan(method, duration, status > 400 || grpcStatus > 0, attrs...)
```
- **What happens now:** an HTTP/2 400 span gets status Unset; the same response over HTTP/1 gets status Error.
- **Expected:** both have status `codes.Error`.
- **Impact:** HTTP/2 (including gRPC over HTTP/2) 400 responses don't show up as errors in traces or in error rates built from traces.
- **Severity:** Wrong or missing data
- **Fix direction:** change `status > 400` to `status >= 400`.

### A NaN trace sampling rate is accepted and drops every span
- **Where:** [tracing/tracing.go:47-51](tracing/tracing.go#L47-L51) in `Init`, used by `shouldSample` at [tracing/tracing.go:86-94](tracing/tracing.go#L86-L94)
- **Test:** [tracing/tracing_test.go:425](tracing/tracing_test.go#L425) `TestInitNaNSampling` — skipped; remove the `t.Skip` once fixed
- **Problem:** `--traces-sampling` / `TRACES_SAMPLING` is parsed as a float, and Go's parser accepts `NaN`. Every comparison with NaN is false, so the range check lets it through without a warning. In `shouldSample`, `>= 1.0`, `<= 0.0` and `rand.Float64() < NaN` are all false too, so it always returns false.
```go
samplingRate = *flags.TracesSampling
if samplingRate < 0.0 || samplingRate > 1.0 {
	klog.Warningf("invalid traces-sampling value %f, must be between 0.0 and 1.0, using default 1.0", samplingRate)
	samplingRate = 1.0
}
```
- **What happens now:** `--traces-sampling=NaN` keeps `samplingRate = NaN`, logs nothing, and every span is dropped.
- **Expected:** treat NaN as invalid: warn and fall back to 1.0.
- **Impact:** the agent silently exports no traces at all.
- **Severity:** Wrong or missing data
- **Fix direction:** check `math.IsNaN(samplingRate) || samplingRate < 0.0 || samplingRate > 1.0`.
