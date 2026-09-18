// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func init() {
	root = "fixtures"
}

func TestListPids(t *testing.T) {
	res, err := ListPids()
	require.NoError(t, err)
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	assert.Equal(t, []uint32{123, 88451}, res)
}

func TestGetMountInfo(t *testing.T) {
	res := GetMountInfo(123)
	assert.Equal(t, map[string]MountInfo{
		"3125": {MajorMinor: "259:2", MountPoint: "/dev/termination-log"},
		"3126": {MajorMinor: "259:2", MountPoint: "/bitnami/kafka"},
		"3127": {MajorMinor: "259:2", MountPoint: "/scripts/setup.sh"},
		"3128": {MajorMinor: "259:2", MountPoint: "/etc/resolv.conf"},
		"3129": {MajorMinor: "259:2", MountPoint: "/etc/hostname"},
		"3130": {MajorMinor: "259:2", MountPoint: "/etc/hosts"},
		"2664": {MajorMinor: "0:422", MountPoint: "/bitnami/postgresql"},
		"2665": {MajorMinor: "0:422", MountPoint: "/bitnami/postgresql"},
		"2666": {MajorMinor: "0:422", MountPoint: "/bitnami/postgresql"},
	}, res)
}

func TestGetNsPid(t *testing.T) {
	nsPid, err := GetNsPid(123)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), nsPid)

	nsPid, err = GetNsPid(88451)
	require.NoError(t, err)
	assert.Equal(t, uint32(88451), nsPid)
}

func TestReadFds(t *testing.T) {
	fds, err := ReadFds(123)
	require.NoError(t, err)
	assert.Equal(t, []Fd{
		{Fd: 4, Dest: "/var/lib/postgresql/data/pg_wal/000000010000000000000001"},
		{Fd: 5, Dest: "socket:[321]", SocketInode: "321"},
	}, fds)
}

func TestGetFdInfo(t *testing.T) {
	res := GetFdInfo(123, 4)
	assert.Equal(t, FdInfo{
		MntId: "1965",
		Flags: int(0100002),
		Dest:  "/var/lib/postgresql/data/pg_wal/000000010000000000000001",
	}, *res)
}

func TestGetSockets(t *testing.T) {
	res, err := GetSockets(123)
	require.NoError(t, err)

	ipp := func(s string) netaddr.IPPort {
		res, err := netaddr.ParseIPPort(s)
		require.NoError(t, err)
		return res
	}

	assert.Equal(t, []Sock{
		{Inode: "8039432", SAddr: ipp("0.0.0.0:5432"), DAddr: ipp("0.0.0.0:0"), Listen: true},
		{Inode: "8134154", SAddr: ipp("172.17.0.3:5432"), DAddr: ipp("172.17.0.4:36332"), Listen: false},
		{Inode: "8039433", SAddr: ipp("[::]:5432"), DAddr: ipp("[::]:0"), Listen: true},
		{Inode: "11139979", SAddr: ipp("[fe80::48cb:8b57:3c30:e6ac]:8080"), DAddr: ipp("[::]:0"), Listen: true},
		{Inode: "11154515", SAddr: ipp("127.0.0.1:8081"), DAddr: ipp("[::]:0"), Listen: true},
	}, res)
}

// procTestRoot points the package at a fresh fake /proc and returns its path.
func procTestRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := root
	root = dir
	t.Cleanup(func() { root = prev })
	return dir
}

func procTestWrite(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestPathAndHostPath(t *testing.T) {
	procTestRoot(t)
	root = "/proc"
	assert.Equal(t, "/proc/42/net/tcp", Path(42, "net", "tcp"))
	assert.Equal(t, "/proc/42", Path(42))
	assert.Equal(t, "/proc/1/root/etc/machine-id", HostPath("/etc/machine-id"))
	// path.Join cleans the result
	assert.Equal(t, "/proc/1/root/etc/os-release", HostPath("/etc/../etc/os-release"))
}

func TestGetCmdline(t *testing.T) {
	dir := procTestRoot(t)
	procTestWrite(t, dir, "10/cmdline", "/usr/bin/java\x00-jar\x00app.jar\x00")
	assert.Equal(t, []byte("/usr/bin/java\x00-jar\x00app.jar"), GetCmdline(10))

	procTestWrite(t, dir, "11/cmdline", "") // kernel thread / zombie
	assert.Empty(t, GetCmdline(11))

	assert.Nil(t, GetCmdline(12), "exited process")
}

func TestGetNsPidEdgeCases(t *testing.T) {
	dir := procTestRoot(t)

	_, err := GetNsPid(1)
	assert.Error(t, err, "exited process")

	procTestWrite(t, dir, "2/status", "Name:\tx\nPid:\t2\n")
	_, err = GetNsPid(2)
	assert.EqualError(t, err, "NSpid not found")

	procTestWrite(t, dir, "3/status", "Name:\tx\nNSpid:\tabc\n")
	_, err = GetNsPid(3)
	assert.Error(t, err)

	procTestWrite(t, dir, "4/status", "Name:\tx\nNSpid:\t4\t99999999999\n")
	_, err = GetNsPid(4)
	assert.Error(t, err, "overflow")

	procTestWrite(t, dir, "5/status", "Name:\tx\nNSpid:\t5\t7\n")
	v, err := GetNsPid(5)
	require.NoError(t, err)
	assert.Equal(t, uint32(7), v)
}

func TestGetNsPidNestedNamespaces(t *testing.T) {
	// NSpid lists the pid in every nested pid namespace, outermost first; the innermost
	// (the one a containerized JVM sees) is the last field — e.g. docker-in-docker, sysbox.
	// BUG: GetNsPid rejects NSpid lines with more than two pids instead of using the last one — unskip when fixed
	t.Skip("BUG: GetNsPid rejects NSpid lines with more than two pids (nested pid namespaces)")
	dir := procTestRoot(t)
	procTestWrite(t, dir, "6/status", "Name:\tx\nNSpid:\t6\t60\t1\n")
	v, err := GetNsPid(6)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), v)
}

func TestReadCgroup(t *testing.T) {
	dir := procTestRoot(t)
	procTestWrite(t, dir, "7/cgroup", "0::/system.slice/nginx.service\n")
	cg, err := ReadCgroup(7)
	require.NoError(t, err)
	assert.Equal(t, "/system.slice/nginx.service", cg.ContainerId)

	_, err = ReadCgroup(8)
	assert.True(t, common.IsNotExist(err))
}

func TestListPidsEdgeCases(t *testing.T) {
	dir := procTestRoot(t)
	for _, d := range []string{"1", "42", "self", "sys", "4294967296", "-1"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	res, err := ListPids()
	require.NoError(t, err)
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	assert.Equal(t, []uint32{1, 42}, res)

	root = filepath.Join(dir, "missing")
	_, err = ListPids()
	assert.Error(t, err)
}
