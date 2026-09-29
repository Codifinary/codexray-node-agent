// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

const netTestHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func TestReadSocketsStatesAndMalformed(t *testing.T) {
	dir := procTestRoot(t)
	procTestWrite(t, dir, "1/net/tcp", netTestHeader+
		// 127.0.0.1:8080 LISTEN
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 111 1 0 100 0 0 10 0\n"+
		// 10.0.0.1:443 -> 10.0.0.2:50000 ESTABLISHED
		"   1: 0100000A:01BB 0200000A:C350 01 00000000:00000000 00:00000000 00000000     0        0 222 1 0 100 0 0 10 0\n"+
		// TIME_WAIT (06) is ignored
		"   2: 0100000A:01BB 0200000A:C351 06 00000000:00000000 00:00000000 00000000     0        0 0 1 0 100 0 0 10 0\n"+
		// CLOSE_WAIT (08) is ignored
		"   3: 0100000A:01BB 0200000A:C352 08 00000000:00000000 00:00000000 00000000     0        0 333 1 0 100 0 0 10 0\n"+
		// truncated lines must not panic
		"   4: 0100000A:01BB\n"+
		"   5: 0100000A:01BB 0200000A:C353 01\n"+
		"\n"+
		// malformed addresses decode to zero values
		"   7: ZZ00000A:01BB 0200000A:C354 01 00000000:00000000 00:00000000 00000000     0        0 444 1 0 100 0 0 10 0\n")
	var res []Sock
	require.NotPanics(t, func() {
		var err error
		res, err = GetSockets(1)
		require.NoError(t, err)
	})
	assert.Equal(t, []Sock{
		{Inode: "111", SAddr: netaddr.MustParseIPPort("127.0.0.1:8080"), DAddr: netaddr.MustParseIPPort("0.0.0.0:0"), Listen: true},
		{Inode: "222", SAddr: netaddr.MustParseIPPort("10.0.0.1:443"), DAddr: netaddr.MustParseIPPort("10.0.0.2:50000")},
		{Inode: "444", SAddr: netaddr.IPPort{}, DAddr: netaddr.MustParseIPPort("10.0.0.2:50004")},
	}, res)
}

func TestReadSocketsTruncatedAfterState(t *testing.T) {
	dir := procTestRoot(t)
	procTestWrite(t, dir, "1/net/tcp", netTestHeader+"   6: 0100000A:01BB 0200000A:C353 01 00000000:00000000\n")
	assert.NotPanics(t, func() { _, _ = GetSockets(1) })
}

func TestGetSocketsMissingAndUnreadable(t *testing.T) {
	dir := procTestRoot(t)
	// process exited: no error, nothing returned
	res, err := GetSockets(1)
	assert.NoError(t, err)
	assert.Empty(t, res)

	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	procTestWrite(t, dir, "2/net/tcp", netTestHeader+
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 111 1 0 100 0 0 10 0\n")
	procTestWrite(t, dir, "2/net/tcp6", netTestHeader)
	require.NoError(t, os.Chmod(filepath.Join(dir, "2/net/tcp6"), 0))
	res, err = GetSockets(2)
	assert.Error(t, err)
	assert.Len(t, res, 1, "partial results from readable files are kept")
}

func TestDecodeAddr(t *testing.T) {
	cases := map[string]netaddr.IPPort{
		"0100007F:0050":                         netaddr.MustParseIPPort("127.0.0.1:80"),
		"00000000:0000":                         netaddr.MustParseIPPort("0.0.0.0:0"),
		"0000000000000000FFFF00000100007F:1F90": netaddr.MustParseIPPort("127.0.0.1:8080"), // v4-mapped v6 is unmapped
		"00000000000000000000000001000000:0016": netaddr.MustParseIPPort("[::1]:22"),
		"B80D0120000000000000000001000000:01BB": netaddr.MustParseIPPort("[2001:db8::1]:443"),
		"":                                      {},
		"0100007F":                              {},
		"01007F:0050":                           {},
		"0100007G:0050":                         {},
		"0100007F:ZZ":                           {},
		"0100007F:0":                            {},
	}
	for in, want := range cases {
		assert.Equal(t, want, decodeAddr([]byte(in)), in)
	}
}

func TestNextField(t *testing.T) {
	f, rest := nextField([]byte("   abc def"))
	assert.Equal(t, "abc", string(f))
	assert.Equal(t, " def", string(rest))
	f, rest = nextField([]byte("last"))
	assert.Nil(t, f)
	assert.Nil(t, rest)
	f, rest = nextField(nil)
	assert.Nil(t, f)
	assert.Nil(t, rest)
}
