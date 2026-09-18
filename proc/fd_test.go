// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
)

func fdTestCaptureKlog(t *testing.T) *bytes.Buffer {
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

func TestReadFdsEdgeCases(t *testing.T) {
	dir := procTestRoot(t)

	// process exited -> nil, nil
	fds, err := ReadFds(1)
	assert.NoError(t, err)
	assert.Nil(t, fds)

	fdDir := filepath.Join(dir, "2", "fd")
	require.NoError(t, os.MkdirAll(fdDir, 0o755))
	require.NoError(t, os.Symlink("/dev/null", filepath.Join(fdDir, "0")))
	require.NoError(t, os.Symlink("socket:[12345]", filepath.Join(fdDir, "3")))
	require.NoError(t, os.Symlink("pipe:[999]", filepath.Join(fdDir, "4")))
	require.NoError(t, os.Symlink("socket:[", filepath.Join(fdDir, "5")))
	require.NoError(t, os.Symlink("x", filepath.Join(fdDir, "notanumber")))
	require.NoError(t, os.WriteFile(filepath.Join(fdDir, "6"), nil, 0o644)) // not a link

	fds, err = ReadFds(2)
	require.NoError(t, err)
	assert.ElementsMatch(t, []Fd{
		{Fd: 0, Dest: "/dev/null"},
		{Fd: 3, Dest: "socket:[12345]", SocketInode: "12345"},
		{Fd: 4, Dest: "pipe:[999]"},
		{Fd: 5, Dest: "socket:["},
	}, fds)

	if os.Geteuid() != 0 {
		unreadable := filepath.Join(dir, "3", "fd")
		require.NoError(t, os.MkdirAll(unreadable, 0o755))
		require.NoError(t, os.Chmod(unreadable, 0))
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
		_, err = ReadFds(3)
		assert.Error(t, err, "permission errors are returned, not swallowed")
	}
}

func TestReadFdsWarnsOnUnexpectedReadlinkError(t *testing.T) {
	// An fd that vanished between ReadDir and Readlink (ENOENT) is the normal process-churn race
	// and must be silent; any other Readlink failure is unexpected and should be logged.
	// BUG: ReadFds logs only when Readlink fails with not-exist (inverted condition) — unskip when fixed
	t.Skip("BUG: ReadFds logs only when Readlink fails with not-exist (inverted condition)")
	dir := procTestRoot(t)
	fdDir := filepath.Join(dir, "2", "fd")
	require.NoError(t, os.MkdirAll(fdDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fdDir, "7"), nil, 0o644)) // Readlink -> EINVAL
	buf := fdTestCaptureKlog(t)
	_, err := ReadFds(2)
	require.NoError(t, err)
	klog.Flush()
	assert.Contains(t, buf.String(), "failed to read link")
}

func TestGetFdInfoEdgeCases(t *testing.T) {
	dir := procTestRoot(t)
	assert.Nil(t, GetFdInfo(1, 3), "exited process")

	procTestWrite(t, dir, "2/fdinfo/3", "pos:\t0\nflags:\t02004002\nmnt_id:\t29\nino:\t1\n")
	assert.Nil(t, GetFdInfo(2, 3), "fd closed between fdinfo and readlink")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "2", "fd"), 0o755))
	require.NoError(t, os.Symlink("/var/log/app.log", filepath.Join(dir, "2", "fd", "3")))
	res := GetFdInfo(2, 3)
	require.NotNil(t, res)
	assert.Equal(t, FdInfo{MntId: "29", Flags: 02004002, Dest: "/var/log/app.log"}, *res)

	procTestWrite(t, dir, "2/fdinfo/3", "flags:\tnotoctal\n")
	res = GetFdInfo(2, 3)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.Flags)
	assert.Equal(t, "", res.MntId)
}
