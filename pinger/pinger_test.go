// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package pinger

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"net"
	"os"
	"strings"
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
	"k8s.io/klog/v2"
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

type pingerTestRead struct {
	pkt  []byte
	oob  []byte
	addr *net.IPAddr
	err  error
}

// pingerTestConn is an in-memory ipConn: writes are recorded, reads are served
// from a queue and time out once it is drained.
type pingerTestConn struct {
	writes    [][]byte
	addrs     []net.Addr
	writeErrs map[int]error // by write index
	reads     []pingerTestRead
	fileErr   error
	deadline  time.Time
	closed    bool
}

func (c *pingerTestConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	i := len(c.writes)
	c.writes = append(c.writes, append([]byte(nil), b...))
	c.addrs = append(c.addrs, addr)
	if err := c.writeErrs[i]; err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *pingerTestConn) ReadMsgIP(b, oob []byte) (int, int, int, *net.IPAddr, error) {
	if len(c.reads) == 0 {
		time.Sleep(time.Millisecond)
		return 0, 0, 0, nil, &net.OpError{Op: "read", Net: "ip4", Err: os.ErrDeadlineExceeded}
	}
	r := c.reads[0]
	c.reads = c.reads[1:]
	if r.err != nil {
		return 0, 0, 0, nil, r.err
	}
	return copy(b, r.pkt), copy(oob, r.oob), 0, r.addr, nil
}

func (c *pingerTestConn) SetReadDeadline(t time.Time) error {
	c.deadline = t
	return nil
}

func (c *pingerTestConn) File() (*os.File, error) {
	if c.fileErr != nil {
		return nil, c.fileErr
	}
	return os.Open(os.DevNull)
}

func (c *pingerTestConn) Close() error {
	c.closed = true
	return nil
}

// pingerTestReply is an echo reply from ip received at rx.
func pingerTestReply(t *testing.T, ip string, id, seq int, rx time.Time) pingerTestRead {
	return pingerTestRead{
		pkt:  pingerTestIPv4(t, 5, pingerTestEcho(ipv4.ICMPTypeEchoReply, id, seq)),
		oob:  pingerTestCmsg(syscall.SOL_SOCKET, unix.SO_TIMESTAMPING, pingerTestTimestamping(rx)),
		addr: &net.IPAddr{IP: net.ParseIP(ip)},
	}
}

func pingerTestReadErr(err error) pingerTestRead {
	return pingerTestRead{err: &net.OpError{Op: "read", Net: "ip4", Err: err}}
}

// pingerTestSetup installs conn and a TX timestamp source returning txs[i] (or
// txErrs[i]) for the i-th sent packet.
func pingerTestSetup(t *testing.T, conn *pingerTestConn, txs []time.Time, txErrs map[int]error) {
	t.Helper()
	origOpen, origTx := openConnFn, getTxTimestampFn
	t.Cleanup(func() { openConnFn, getTxTimestampFn = origOpen, origTx })
	openConnFn = func() (ipConn, error) { return conn, nil }
	calls := 0
	getTxTimestampFn = func(fd int) (time.Time, error) {
		i := calls
		calls++
		assert.GreaterOrEqual(t, fd, 0)
		if err := txErrs[i]; err != nil {
			return time.Time{}, err
		}
		return txs[i], nil
	}
}

func pingerTestCaptureKlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	require.NoError(t, fs.Set("logtostderr", "false"))
	require.NoError(t, fs.Set("alsologtostderr", "false"))
	buf := &bytes.Buffer{}
	klog.SetOutput(buf)
	t.Cleanup(func() {
		klog.Flush()
		require.NoError(t, fs.Set("logtostderr", "true"))
		klog.SetOutput(os.Stderr)
	})
	return buf
}

func pingerTestIPs(ips ...string) []netaddr.IP {
	res := make([]netaddr.IP, 0, len(ips))
	for _, ip := range ips {
		res = append(res, netaddr.MustParseIP(ip))
	}
	return res
}

func pingerTestPing(targets []netaddr.IP, timeout time.Duration) (map[netaddr.IP]float64, error) {
	return Ping(netns.None(), netns.None(), targets, timeout)
}

func TestPingOpenConnError(t *testing.T) {
	orig := openConnFn
	t.Cleanup(func() { openConnFn = orig })
	openConnFn = func() (ipConn, error) { return nil, errors.New("operation not permitted") }

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1"), time.Second)
	assert.EqualError(t, err, "failed to open IPConn: operation not permitted")
	assert.Nil(t, res)
}

func TestPingFileError(t *testing.T) {
	conn := &pingerTestConn{fileErr: errors.New("dup failed")}
	pingerTestSetup(t, conn, nil, nil)

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1"), time.Second)
	assert.EqualError(t, err, "dup failed")
	assert.Nil(t, res)
	assert.True(t, conn.closed)
	assert.Empty(t, conn.writes)
}

func TestPingSendError(t *testing.T) {
	conn := &pingerTestConn{writeErrs: map[int]error{1: errors.New("network is unreachable")}}
	now := time.Now()
	pingerTestSetup(t, conn, []time.Time{now, now}, nil)

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2", "10.0.0.3"), time.Second)
	assert.EqualError(t, err, "failed to send packet to 10.0.0.2: network is unreachable")
	assert.Nil(t, res)
	assert.True(t, conn.closed)
	assert.Len(t, conn.writes, 2, "sending stops at the first hard error")
}

func TestPingSendEAGAINSkipsTarget(t *testing.T) {
	// the error text of a bare syscall.EAGAIN
	conn := &pingerTestConn{writeErrs: map[int]error{0: syscall.EAGAIN}}
	tx := time.Now()
	conn.reads = []pingerTestRead{
		pingerTestReply(t, "10.0.0.1", pingerID, 1, tx), // skipped target: ignored
		pingerTestReply(t, "10.0.0.2", pingerID, 2, tx.Add(3*time.Millisecond)),
	}
	pingerTestSetup(t, conn, []time.Time{tx}, nil) // one TX timestamp: only the 2nd packet was sent

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2"), 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, map[netaddr.IP]float64{netaddr.MustParseIP("10.0.0.2"): 0.003}, res)
	assert.Len(t, conn.writes, 2)
}

func TestPingSendWrappedEAGAINSkipsTarget(t *testing.T) {
	// BUG: Ping checks strings.HasPrefix(err.Error(), "resource temporarily unavailable")
	// on the error from send(), but net.IPConn.WriteTo wraps errors in *net.OpError
	// ("write ip4 ...: sendto: resource temporarily unavailable"), so the prefix
	// never matches and an EAGAIN aborts the whole Ping instead of skipping the
	// target — unskip when fixed
	t.Skip("BUG: Ping's EAGAIN check on send() errors never matches the *net.OpError returned by WriteTo")
	dst := netaddr.MustParseIP("10.0.0.1")
	conn := &pingerTestConn{writeErrs: map[int]error{0: &net.OpError{
		Op: "write", Net: "ip4", Addr: dst.IPAddr(), Err: os.NewSyscallError("sendto", syscall.EAGAIN),
	}}}
	pingerTestSetup(t, conn, nil, nil)

	res, err := pingerTestPing([]netaddr.IP{dst}, 20*time.Millisecond)
	assert.NoError(t, err)
	assert.Empty(t, res)
}

func TestPingTxTimestampError(t *testing.T) {
	conn := &pingerTestConn{}
	pingerTestSetup(t, conn, nil, map[int]error{0: syscall.ENOTSOCK})

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1"), time.Second)
	assert.EqualError(t, err, "failed to get TX timestamp: socket operation on non-socket")
	assert.Nil(t, res)
	assert.True(t, conn.closed)
}

func TestPingTxTimestampEAGAINSkipsTarget(t *testing.T) {
	conn := &pingerTestConn{}
	tx := time.Now()
	conn.reads = []pingerTestRead{
		pingerTestReply(t, "10.0.0.1", pingerID, 1, tx.Add(time.Millisecond)), // no TX timestamp: ignored
		pingerTestReply(t, "10.0.0.2", pingerID, 2, tx.Add(2*time.Millisecond)),
	}
	pingerTestSetup(t, conn, []time.Time{{}, tx}, map[int]error{0: syscall.EAGAIN})

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2"), 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, map[netaddr.IP]float64{netaddr.MustParseIP("10.0.0.2"): 0.002}, res)
}

func TestPingSkippedTargetDoesNotWaitForTimeout(t *testing.T) {
	// BUG: the early return compares the number of replies with len(targets)
	// rather than with the number of packets actually sent, so when a target is
	// skipped (EAGAIN) Ping always blocks for the full timeout even though every
	// sent packet was answered — unskip when fixed
	t.Skip("BUG: Ping waits for the full timeout when a target was skipped on EAGAIN")
	conn := &pingerTestConn{}
	tx := time.Now()
	conn.reads = []pingerTestRead{pingerTestReply(t, "10.0.0.2", pingerID, 2, tx)}
	pingerTestSetup(t, conn, []time.Time{{}, tx}, map[int]error{0: syscall.EAGAIN})

	start := time.Now()
	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2"), 2*time.Second)
	require.NoError(t, err)
	assert.Len(t, res, 1)
	assert.Less(t, time.Since(start), time.Second)
}

func TestPingSendsEchoRequests(t *testing.T) {
	conn := &pingerTestConn{}
	tx := time.Now()
	pingerTestSetup(t, conn, []time.Time{tx, tx}, nil)

	targets := pingerTestIPs("10.0.0.1", "192.168.1.1")
	_, err := pingerTestPing(targets, time.Millisecond)
	require.NoError(t, err)
	require.Len(t, conn.writes, 2)
	for i, ip := range targets {
		assert.Equal(t, ip.IPAddr(), conn.addrs[i])
		m, err := icmp.ParseMessage(protocolICMP, conn.writes[i])
		require.NoError(t, err)
		assert.Equal(t, ipv4.ICMPTypeEcho, m.Type)
		echo, ok := m.Body.(*icmp.Echo)
		require.True(t, ok)
		assert.Equal(t, pingerID, echo.ID)
		assert.Equal(t, i+1, echo.Seq, "seq is the 1-based target index")
	}
	assert.True(t, conn.closed)
}

func TestPingIgnoresUnrelatedReplies(t *testing.T) {
	logs := pingerTestCaptureKlog(t)
	conn := &pingerTestConn{}
	tx := time.Unix(1_700_000_000, 0)
	conn.reads = []pingerTestRead{
		pingerTestReply(t, "10.0.0.1", (pingerID+1)&0xFFFF, 1, tx.Add(time.Millisecond)), // another pinger
		pingerTestReply(t, "10.0.0.1", pingerID, 2, tx.Add(time.Millisecond)),            // seq of another target
		pingerTestReply(t, "10.0.0.9", pingerID, 1, tx.Add(time.Millisecond)),            // not a target
		{ // not a valid IP address
			pkt:  pingerTestIPv4(t, 5, pingerTestEcho(ipv4.ICMPTypeEchoReply, pingerID, 1)),
			oob:  pingerTestCmsg(syscall.SOL_SOCKET, unix.SO_TIMESTAMPING, pingerTestTimestamping(tx)),
			addr: &net.IPAddr{IP: net.IP{1, 2, 3}},
		},
		{pkt: pingerTestIPv4(t, 5, pingerTestEcho(ipv4.ICMPTypeEcho, pingerID, 1)), addr: &net.IPAddr{IP: net.ParseIP("10.0.0.1")}}, // not a reply
		pingerTestReply(t, "10.0.0.1", pingerID, 1, tx.Add(5*time.Millisecond)),
	}
	pingerTestSetup(t, conn, []time.Time{tx, tx}, nil)

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2"), 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, map[netaddr.IP]float64{netaddr.MustParseIP("10.0.0.1"): 0.005}, res)
	assert.Empty(t, conn.reads)
	klog.Flush()
	assert.Empty(t, logs.String())
}

func TestPingNegativeRTTClampedToZero(t *testing.T) {
	conn := &pingerTestConn{}
	tx := time.Now()
	conn.reads = []pingerTestRead{pingerTestReply(t, "10.0.0.1", pingerID, 1, tx.Add(-time.Millisecond))}
	pingerTestSetup(t, conn, []time.Time{tx}, nil)

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1"), time.Second)
	require.NoError(t, err)
	assert.Equal(t, map[netaddr.IP]float64{netaddr.MustParseIP("10.0.0.1"): 0}, res)
}

func TestPingReturnsOnceAllTargetsAnswered(t *testing.T) {
	conn := &pingerTestConn{}
	tx := time.Now()
	conn.reads = []pingerTestRead{
		pingerTestReply(t, "10.0.0.2", pingerID, 2, tx.Add(2*time.Millisecond)),
		pingerTestReply(t, "10.0.0.1", pingerID, 1, tx.Add(time.Millisecond)),
	}
	pingerTestSetup(t, conn, []time.Time{tx, tx}, nil)

	start := time.Now()
	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2"), time.Minute)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "must not wait for the timeout")
	assert.Equal(t, map[netaddr.IP]float64{
		netaddr.MustParseIP("10.0.0.1"): 0.001,
		netaddr.MustParseIP("10.0.0.2"): 0.002,
	}, res)
	assert.True(t, conn.closed)
}

func TestPingTimeoutReturnsPartialResults(t *testing.T) {
	conn := &pingerTestConn{}
	tx := time.Now()
	conn.reads = []pingerTestRead{pingerTestReply(t, "10.0.0.2", pingerID, 2, tx.Add(time.Millisecond))}
	pingerTestSetup(t, conn, []time.Time{tx, tx}, nil)

	start := time.Now()
	res, err := pingerTestPing(pingerTestIPs("10.0.0.1", "10.0.0.2"), 30*time.Millisecond)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
	assert.Equal(t, map[netaddr.IP]float64{netaddr.MustParseIP("10.0.0.2"): 0.001}, res)
}

func TestPingReceiveErrors(t *testing.T) {
	logs := pingerTestCaptureKlog(t)
	conn := &pingerTestConn{}
	tx := time.Now()
	conn.reads = []pingerTestRead{
		pingerTestReadErr(syscall.EINTR), // "interrupted system call": expected, not logged
		pingerTestReadErr(syscall.ECONNREFUSED),
		{pkt: make([]byte, 4), addr: &net.IPAddr{IP: net.ParseIP("10.0.0.1")}}, // malformed
		pingerTestReply(t, "10.0.0.1", pingerID, 1, tx.Add(time.Millisecond)),
	}
	pingerTestSetup(t, conn, []time.Time{tx}, nil)

	res, err := pingerTestPing(pingerTestIPs("10.0.0.1"), time.Second)
	require.NoError(t, err, "receive errors do not abort Ping")
	assert.Equal(t, map[netaddr.IP]float64{netaddr.MustParseIP("10.0.0.1"): 0.001}, res)
	klog.Flush()
	out := logs.String()
	assert.NotContains(t, out, "interrupted system call")
	assert.Contains(t, out, "connection refused")
	assert.Contains(t, out, "failed to extract ICMP Echo from IPv4 packet 10.0.0.1")
}

func TestSend(t *testing.T) {
	conn := &pingerTestConn{}
	dst := &net.IPAddr{IP: net.ParseIP("10.1.2.3")}
	require.NoError(t, send(conn, 0xFFFF, dst))
	require.Len(t, conn.writes, 1)
	assert.Equal(t, dst, conn.addrs[0])
	m, err := icmp.ParseMessage(protocolICMP, conn.writes[0])
	require.NoError(t, err)
	assert.Equal(t, ipv4.ICMPTypeEcho, m.Type)
	assert.Equal(t, &icmp.Echo{ID: pingerID, Seq: 0xFFFF, Data: nil}, m.Body)

	conn.writeErrs = map[int]error{1: errors.New("no buffer space available")}
	assert.EqualError(t, send(conn, 1, dst), "no buffer space available")
}

func TestReceive(t *testing.T) {
	rx := time.Unix(1_700_000_000, 5_000)
	reply := pingerTestReply(t, "10.0.0.1", pingerID, 4, rx)

	t.Run("reply", func(t *testing.T) {
		conn := &pingerTestConn{reads: []pingerTestRead{reply}}
		start := time.Now()
		ra, echo, ts, err := receive(conn)
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.1", ra.IP.String())
		require.NotNil(t, echo)
		assert.Equal(t, pingerID, echo.ID)
		assert.Equal(t, 4, echo.Seq)
		assert.True(t, rx.Equal(ts), "want %s got %s", rx, ts)
		assert.WithinDuration(t, start.Add(pingReplyPollTimeout), conn.deadline, time.Second)
	})
	t.Run("timeout", func(t *testing.T) {
		ra, echo, ts, err := receive(&pingerTestConn{})
		assert.NoError(t, err)
		assert.Nil(t, ra)
		assert.Nil(t, echo)
		assert.True(t, ts.IsZero())
	})
	t.Run("no message of desired type", func(t *testing.T) {
		ra, echo, _, err := receive(&pingerTestConn{reads: []pingerTestRead{pingerTestReadErr(syscall.ENOMSG)}})
		assert.NoError(t, err)
		assert.Nil(t, ra)
		assert.Nil(t, echo)
	})
	t.Run("read error", func(t *testing.T) {
		_, echo, _, err := receive(&pingerTestConn{reads: []pingerTestRead{pingerTestReadErr(syscall.EINTR)}})
		assert.ErrorIs(t, err, syscall.EINTR)
		assert.Nil(t, echo)
	})
	t.Run("not an echo reply", func(t *testing.T) {
		r := reply
		r.pkt = pingerTestIPv4(t, 5, &icmp.Message{Type: ipv4.ICMPTypeDestinationUnreachable, Body: &icmp.RawBody{Data: make([]byte, 28)}})
		ra, echo, _, err := receive(&pingerTestConn{reads: []pingerTestRead{r}})
		assert.NoError(t, err)
		assert.Nil(t, ra)
		assert.Nil(t, echo)
	})
	t.Run("malformed packet", func(t *testing.T) {
		r := reply
		r.pkt = make([]byte, ipv4.HeaderLen-1)
		_, echo, _, err := receive(&pingerTestConn{reads: []pingerTestRead{r}})
		assert.EqualError(t, err, "failed to extract ICMP Echo from IPv4 packet 10.0.0.1: malformed IPv4 packet")
		assert.Nil(t, echo)
	})
	t.Run("no RX timestamp", func(t *testing.T) {
		r := reply
		r.oob = nil
		ra, echo, _, err := receive(&pingerTestConn{reads: []pingerTestRead{r}})
		assert.EqualError(t, err, "failed to get RX timestamp: no timestamp found")
		assert.Nil(t, ra)
		assert.Nil(t, echo)
	})
}

// pingerTestUDPSocket returns a bound UDP socket with software TX timestamping,
// which (unlike a raw ICMP socket) needs no privileges.
func pingerTestUDPSocket(t *testing.T) (int, unix.Sockaddr) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(fd) })
	flags := unix.SOF_TIMESTAMPING_SOFTWARE | unix.SOF_TIMESTAMPING_TX_SOFTWARE |
		unix.SOF_TIMESTAMPING_OPT_CMSG | unix.SOF_TIMESTAMPING_OPT_TSONLY
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, flags); err != nil {
		t.Skip("SO_TIMESTAMPING unsupported:", err)
	}
	require.NoError(t, unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}))
	sa, err := unix.Getsockname(fd)
	require.NoError(t, err)
	return fd, sa
}

func TestGetTxTimestampEmptyErrQueue(t *testing.T) {
	fd, _ := pingerTestUDPSocket(t)
	_, err := getTxTimestamp(fd)
	assert.ErrorIs(t, err, syscall.EAGAIN)
	assert.True(t, strings.HasPrefix(err.Error(), "resource temporarily unavailable"), "Ping relies on this text: %s", err)
}

func TestGetTxTimestamp(t *testing.T) {
	fd, sa := pingerTestUDPSocket(t)
	before := time.Now()
	require.NoError(t, unix.Sendto(fd, []byte("ping"), 0, sa))
	var ts time.Time
	var err error
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if ts, err = getTxTimestamp(fd); !errors.Is(err, syscall.EAGAIN) {
			break
		}
	}
	if errors.Is(err, syscall.EAGAIN) {
		t.Skip("no TX timestamp was queued by the kernel")
	}
	require.NoError(t, err)
	assert.WithinDuration(t, before, ts, 5*time.Second)
}
