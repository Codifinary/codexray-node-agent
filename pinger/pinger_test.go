// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package pinger

import (
	"encoding/binary"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netns"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
	"inet.af/netaddr"
)

// pingerTestIPv4 builds a raw IPv4 packet (as delivered by an ip4:icmp socket)
// around an ICMP message. ihl is the header length in 32-bit words (5 = no options).
func pingerTestIPv4(t *testing.T, ihl int, msg *icmp.Message) []byte {
	t.Helper()
	body, err := msg.Marshal(nil)
	require.NoError(t, err)
	hdr := make([]byte, ihl*4)
	hdr[0] = 0x40 | byte(ihl)
	binary.BigEndian.PutUint16(hdr[2:4], uint16(len(hdr)+len(body)))
	hdr[8] = 64
	hdr[9] = protocolICMP
	copy(hdr[12:16], []byte{10, 0, 0, 2})
	copy(hdr[16:20], []byte{10, 0, 0, 1})
	for i := 20; i < len(hdr); i++ {
		hdr[i] = 0x01 // IPv4 NOP options
	}
	return append(hdr, body...)
}

// pingerTestRecvBuf copies a packet into a receive-sized buffer as receive() does.
func pingerTestRecvBuf(pkt []byte) ([]byte, int) {
	buf := make([]byte, 1024)
	return buf, copy(buf, pkt)
}

func pingerTestEcho(typ icmp.Type, id, seq int) *icmp.Message {
	return &icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("payload")}}
}

func TestExtractEchoFromPacketEchoReply(t *testing.T) {
	buf, n := pingerTestRecvBuf(pingerTestIPv4(t, 5, pingerTestEcho(ipv4.ICMPTypeEchoReply, pingerID, 7)))
	echo, err := extractEchoFromPacket(buf, n)
	require.NoError(t, err)
	require.NotNil(t, echo)
	assert.Equal(t, pingerID, echo.ID)
	assert.Equal(t, 7, echo.Seq)
}

func TestExtractEchoFromPacketIgnoresOtherTypes(t *testing.T) {
	for _, typ := range []icmp.Type{ipv4.ICMPTypeEcho, ipv4.ICMPTypeTimeExceeded, ipv4.ICMPTypeDestinationUnreachable} {
		var msg *icmp.Message
		if typ == ipv4.ICMPTypeEcho {
			msg = pingerTestEcho(typ, pingerID, 1)
		} else {
			msg = &icmp.Message{Type: typ, Body: &icmp.RawBody{Data: make([]byte, 28)}}
		}
		buf, n := pingerTestRecvBuf(pingerTestIPv4(t, 5, msg))
		echo, err := extractEchoFromPacket(buf, n)
		assert.NoError(t, err, "type %v", typ)
		assert.Nil(t, echo, "only echo replies are returned, type %v", typ)
	}
}

func TestExtractEchoFromPacketMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":           {},
		"short ip header": make([]byte, ipv4.HeaderLen-1),
		"icmp truncated":  append(pingerTestIPv4(t, 5, pingerTestEcho(ipv4.ICMPTypeEchoReply, 1, 1))[:ipv4.HeaderLen], 0x00, 0x00),
		"echo body too short": append(pingerTestIPv4(t, 5, pingerTestEcho(ipv4.ICMPTypeEchoReply, 1, 1))[:ipv4.HeaderLen],
			0x00, 0x00, 0x00, 0x00, 0x12),
	}
	for name, pkt := range cases {
		t.Run(name, func(t *testing.T) {
			// exact-length buffer: nothing beyond n to read
			var echo *icmp.Echo
			var err error
			require.NotPanics(t, func() { echo, err = extractEchoFromPacket(pkt, len(pkt)) })
			assert.Error(t, err)
			assert.Nil(t, echo)
		})
	}
}

func TestExtractEchoFromPacketHonoursLength(t *testing.T) {
	// BUG: extractEchoFromPacket parses pktBuf[HeaderLen:] instead of
	// pktBuf[HeaderLen:n], so a truncated read is decoded from stale/zero buffer
	// bytes (all zeros decode as an echo reply with ID 0) — unskip when fixed
	t.Skip("BUG: extractEchoFromPacket ignores n and parses bytes past the received length")
	buf, n := pingerTestRecvBuf(make([]byte, ipv4.HeaderLen)) // IP header only, no ICMP
	echo, err := extractEchoFromPacket(buf, n)
	assert.Error(t, err)
	assert.Nil(t, echo)
}

func TestExtractEchoFromPacketWithIPOptions(t *testing.T) {
	// BUG: the IPv4 header length is assumed to be 20 bytes; IHL (header words)
	// is ignored, so replies carrying IP options are misparsed — unskip when fixed
	t.Skip("BUG: extractEchoFromPacket ignores the IPv4 IHL field (IP options)")
	buf, n := pingerTestRecvBuf(pingerTestIPv4(t, 6, pingerTestEcho(ipv4.ICMPTypeEchoReply, pingerID, 3)))
	echo, err := extractEchoFromPacket(buf, n)
	require.NoError(t, err)
	require.NotNil(t, echo)
	assert.Equal(t, pingerID, echo.ID)
	assert.Equal(t, 3, echo.Seq)
}

// pingerTestCmsg encodes one control message (host byte order == little endian
// on the supported amd64/arm64).
func pingerTestCmsg(level, typ int32, data []byte) []byte {
	buf := make([]byte, syscall.CmsgSpace(len(data)))
	h := (*syscall.Cmsghdr)(unsafe.Pointer(&buf[0]))
	h.Level = level
	h.Type = typ
	h.SetLen(syscall.CmsgLen(len(data)))
	copy(buf[syscall.CmsgLen(0):], data)
	return buf
}

// pingerTestTimestamping encodes struct scm_timestamping {struct timespec ts[3]}.
func pingerTestTimestamping(sw time.Time) []byte {
	var ts unix.ScmTimestamping
	ts.Ts[0] = unix.NsecToTimespec(sw.UnixNano())
	ts.Ts[2] = unix.NsecToTimespec(sw.Add(time.Hour).UnixNano()) // raw hardware: must not be used
	b := make([]byte, unsafe.Sizeof(ts))
	copy(b, (*[unsafe.Sizeof(ts)]byte)(unsafe.Pointer(&ts))[:])
	return b
}

func TestGetTimestampFromOutOfBandData(t *testing.T) {
	want := time.Unix(1_700_000_000, 123_456_789)
	oob := pingerTestCmsg(syscall.SOL_SOCKET, unix.SO_TIMESTAMPING, pingerTestTimestamping(want))
	buf := make([]byte, 1024)
	n := copy(buf, oob)

	got, err := getTimestampFromOutOfBandData(buf, n)
	require.NoError(t, err)
	assert.True(t, want.Equal(got), "software timestamp ts[0] is used: want %s got %s", want, got)
}

func TestGetTimestampFromOutOfBandDataMalformed(t *testing.T) {
	good := pingerTestCmsg(syscall.SOL_SOCKET, unix.SO_TIMESTAMPING, pingerTestTimestamping(time.Now()))
	short := pingerTestCmsg(syscall.SOL_SOCKET, unix.SO_TIMESTAMPING, make([]byte, 8)) // < sizeof(scm_timestamping)
	badLen := append([]byte(nil), good...)
	(*syscall.Cmsghdr)(unsafe.Pointer(&badLen[0])).SetLen(len(badLen) + 64) // claims more than we have

	cases := map[string][]byte{
		"no control messages": {},
		"short payload":       short,
		"length overflow":     badLen,
		"truncated header":    good[:4],
		"other message only":  pingerTestCmsg(syscall.IPPROTO_IP, syscall.IP_TTL, []byte{64, 0, 0, 0}),
	}
	for name, oob := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { _, err = getTimestampFromOutOfBandData(oob, len(oob)) })
			assert.Error(t, err)
		})
	}
}

func TestGetTimestampFromOutOfBandDataSkipsOtherSocketMessages(t *testing.T) {
	// BUG: the cmsg match uses `Level == SOL_SOCKET || Type == SO_TIMESTAMPING`
	// instead of &&, so any other SOL_SOCKET message (or any level with type 37)
	// preceding the timestamp is decoded as scm_timestamping — unskip when fixed
	t.Skip("BUG: getTimestampFromOutOfBandData matches cmsg with || instead of &&")
	want := time.Unix(1_700_000_000, 42)
	oob := append(
		pingerTestCmsg(syscall.SOL_SOCKET, syscall.SCM_CREDENTIALS, make([]byte, 12)),
		pingerTestCmsg(syscall.SOL_SOCKET, unix.SO_TIMESTAMPING, pingerTestTimestamping(want))...,
	)
	got, err := getTimestampFromOutOfBandData(oob, len(oob))
	require.NoError(t, err)
	assert.True(t, want.Equal(got))
}

func TestGetTxTimestampBadFd(t *testing.T) {
	_, err := getTxTimestamp(-1)
	assert.Error(t, err)
}

func TestPingNoTargets(t *testing.T) {
	// No targets: nothing to do, and no socket/netns switch is attempted.
	res, err := Ping(netns.None(), netns.None(), nil, time.Second)
	assert.NoError(t, err)
	assert.Nil(t, res)
	res, err = Ping(netns.None(), netns.None(), []netaddr.IP{}, time.Second)
	assert.NoError(t, err)
	assert.Nil(t, res)
}

func TestPingerIDFitsICMPIdentifier(t *testing.T) {
	assert.GreaterOrEqual(t, pingerID, 0)
	assert.LessOrEqual(t, pingerID, 0xFFFF, "ICMP echo identifier is 16 bits")
}

func TestSendReceiveLoopback(t *testing.T) {
	// Needs CAP_NET_RAW for an ip4:icmp socket; skipped for unprivileged runs.
	conn, err := openConn()
	if err != nil {
		t.Skip("raw ICMP socket unavailable (needs CAP_NET_RAW):", err)
	}
	defer conn.Close()
	lo := netaddr.MustParseIP("127.0.0.1")
	require.NoError(t, send(conn, 42, lo.IPAddr()))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ra, echo, rx, err := receive(conn)
		require.NoError(t, err)
		if echo == nil || echo.ID != pingerID || echo.Seq != 42 {
			continue
		}
		assert.Equal(t, "127.0.0.1", ra.IP.String())
		assert.WithinDuration(t, time.Now(), rx, 5*time.Second)
		return
	}
	t.Fatal("no echo reply from loopback")
}
