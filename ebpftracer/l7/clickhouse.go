// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package l7

import (
	"bytes"
	"encoding/binary"

	"github.com/ClickHouse/ch-go/proto"
	"go.opentelemetry.io/otel/trace"
)

// ParseClickhouse extracts the query text from a captured ClickHouse native-protocol Query
// packet.
//
// The payload is a bounded eBPF capture of arbitrary TCP data, so every length prefix in it
// is untrusted. The previous implementation decoded it with ch-go's proto.Reader, which
// allocates the full declared length of every string *before* reading it
// (Reader.StrRaw -> Buffer.Ensure(n)). A misidentified or corrupted packet could therefore
// declare a multi-gigabyte string and make the agent allocate it, which was observed as
// the dominant heap owner (ParseClickhouse -> Setting.Decode -> Buffer.Ensure) on OOM-killed
// node agents. This decoder walks the same wire format over the captured bytes and rejects
// any length that exceeds what was actually captured; strings are sliced, never copied.
func ParseClickhouse(payload []byte) string {
	r := chReader{b: payload}
	if _, ok := r.byte(); !ok { // packet type
		return ""
	}
	if _, ok := r.str(); !ok { // query id
		return ""
	}
	version := int(proto.FeatureServerQueryTimeInProgress)
	protocolVersion, ok := r.skipClientInfo(version)
	if !ok {
		return ""
	}
	if protocolVersion > version {
		return ""
	}
	for { // settings, terminated by an empty key
		key, ok := r.str()
		if !ok {
			return ""
		}
		if len(key) == 0 {
			break
		}
		if _, ok = r.uvarint(); !ok { // flags
			return ""
		}
		if _, ok = r.str(); !ok { // value
			return ""
		}
	}
	if _, ok := r.str(); !ok { // inter-server secret
		return ""
	}
	if stage, ok := r.uvarint(); !ok || stage > 2 {
		return ""
	}
	if c, ok := r.uvarint(); !ok || c > 1 { // compression
		return ""
	}
	l, ok := r.length()
	if !ok {
		return ""
	}
	// The query itself may be cut off by the capture limit: take what is there (max 1 KB).
	n := min(l, 1024, len(r.b)-r.off)
	query := bytes.TrimSpace(r.b[r.off : r.off+n])
	if len(query) == 0 {
		return ""
	}
	if n < l {
		return string(query[:len(query)-1]) + "...<TRUNCATED>"
	}
	return string(query)
}

// chReader decodes ClickHouse native-protocol primitives from an in-memory buffer without
// allocating. Every read is bounds-checked against the buffer.
type chReader struct {
	b   []byte
	off int
}

func (r *chReader) remaining() int { return len(r.b) - r.off }

func (r *chReader) byte() (byte, bool) {
	if r.remaining() < 1 {
		return 0, false
	}
	v := r.b[r.off]
	r.off++
	return v, true
}

func (r *chReader) skip(n int) bool {
	if n < 0 || n > r.remaining() {
		return false
	}
	r.off += n
	return true
}

func (r *chReader) uvarint() (uint64, bool) {
	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		return 0, false
	}
	r.off += n
	return v, true
}

// int mirrors ch-go's Reader.Int: a uvarint converted to int.
func (r *chReader) int() (int, bool) {
	v, ok := r.uvarint()
	return int(v), ok
}

// length reads a string length prefix. Lengths are never used to allocate; values that
// cannot be a length (ch-go rejects negative ints, here anything above 2 GiB) fail.
func (r *chReader) length() (int, bool) {
	v, ok := r.uvarint()
	if !ok || v > 1<<31 {
		return 0, false
	}
	return int(v), true
}

// str returns the next length-prefixed string as a sub-slice. A string extending past the
// captured bytes is rejected, as ch-go's io.ReadFull would.
func (r *chReader) str() ([]byte, bool) {
	l, ok := r.length()
	if !ok || l > r.remaining() {
		return nil, false
	}
	s := r.b[r.off : r.off+l]
	r.off += l
	return s, true
}

// skipClientInfo walks proto.ClientInfo as ch-go's ClientInfo.DecodeAware does for the given
// version, with the same validation, and returns the client's protocol version.
func (r *chReader) skipClientInfo(version int) (int, bool) {
	kind, ok := r.byte()
	if !ok || !proto.ClientQueryKind(kind).IsAClientQueryKind() {
		return 0, false
	}
	for i := 0; i < 3; i++ { // initial user, initial query id, initial address
		if _, ok = r.str(); !ok {
			return 0, false
		}
	}
	if proto.FeatureQueryStartTime.In(version) && !r.skip(8) { // Int64 microseconds
		return 0, false
	}
	iface, ok := r.byte()
	if !ok || proto.Interface(iface) != proto.InterfaceTCP {
		return 0, false
	}
	for i := 0; i < 3; i++ { // OS user, client hostname, client name
		if _, ok = r.str(); !ok {
			return 0, false
		}
	}
	if _, ok = r.int(); !ok { // major
		return 0, false
	}
	if _, ok = r.int(); !ok { // minor
		return 0, false
	}
	protocolVersion, ok := r.int()
	if !ok {
		return 0, false
	}
	if proto.FeatureQuotaKeyInClientInfo.In(version) {
		if _, ok = r.str(); !ok {
			return 0, false
		}
	}
	if proto.FeatureDistributedDepth.In(version) {
		if _, ok = r.int(); !ok {
			return 0, false
		}
	}
	if proto.FeatureVersionPatch.In(version) {
		if _, ok = r.int(); !ok {
			return 0, false
		}
	}
	if proto.FeatureOpenTelemetry.In(version) {
		hasTrace, ok := r.byte()
		if !ok || hasTrace > 1 { // Bool: only 0 or 1 are valid
			return 0, false
		}
		if hasTrace == 1 {
			if !r.skip(16 + 8) { // trace id, span id
				return 0, false
			}
			state, ok := r.str()
			if !ok {
				return 0, false
			}
			if _, err := trace.ParseTraceState(string(state)); err != nil {
				return 0, false
			}
			if _, ok = r.byte(); !ok { // trace flags
				return 0, false
			}
		}
	}
	if proto.FeatureParallelReplicas.In(version) {
		for i := 0; i < 3; i++ {
			if _, ok = r.int(); !ok {
				return 0, false
			}
		}
	}
	return protocolVersion, true
}
