// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package jvm

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HotSpot attach protocol v1 request: "1\0" <cmd> "\0" then exactly three
// NUL-terminated args; the reply starts with the decimal status and a newline.
const jattachTestPerfmapRequest = "1\x00jcmd\x00Compiler.perfmap\x00\x00\x00"

// jattachTestJVM runs a fake attach listener on one end of a pipe.
func jattachTestJVM(t *testing.T, reply func(req []byte, c net.Conn)) *JVM {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		buf := make([]byte, len(jattachTestPerfmapRequest))
		if _, err := io.ReadFull(server, buf); err != nil {
			return
		}
		reply(buf, server)
	}()
	return &JVM{conn: client}
}

func TestDumpPerfmapProtocol(t *testing.T) {
	t.Run("status 0 is success", func(t *testing.T) {
		got := make(chan []byte, 1)
		j := jattachTestJVM(t, func(req []byte, c net.Conn) {
			got <- req
			_, _ = c.Write([]byte("0\n"))
		})
		require.NoError(t, j.DumpPerfmap())
		assert.Equal(t, jattachTestPerfmapRequest, string(<-got))
		require.NoError(t, j.Close())
	})
	t.Run("non-zero status is an error", func(t *testing.T) {
		j := jattachTestJVM(t, func(req []byte, c net.Conn) {
			_, _ = c.Write([]byte("-1\nUnknown diagnostic command\n"))
		})
		err := j.DumpPerfmap()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status")
		_ = j.Close()
	})
	t.Run("connection closed before status", func(t *testing.T) {
		j := jattachTestJVM(t, func(req []byte, c net.Conn) {
			_ = c.Close()
		})
		assert.Error(t, j.DumpPerfmap())
		_ = j.Close()
	})
	t.Run("write on closed connection", func(t *testing.T) {
		client, server := net.Pipe()
		_ = server.Close()
		_ = client.Close()
		j := &JVM{conn: client}
		assert.Error(t, j.DumpPerfmap())
	})
}

func TestCheckSock(t *testing.T) {
	dir := t.TempDir()
	if len(dir) > 90 {
		var err error
		dir, err = os.MkdirTemp("/tmp", "jv")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer l.Close()
	regular := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(regular, nil, 0600))

	assert.True(t, checkSock(sock))
	assert.False(t, checkSock(regular), "regular attach file is not a socket")
	assert.False(t, checkSock(dir))
	assert.False(t, checkSock(filepath.Join(dir, "missing")))
}

func TestWaitForSock(t *testing.T) {
	dir := t.TempDir()
	if len(dir) > 90 {
		var err error
		dir, err = os.MkdirTemp("/tmp", "jv")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	sock := filepath.Join(dir, "late.sock")
	ls := make(chan net.Listener, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		l, _ := net.Listen("unix", sock)
		ls <- l
	}()
	start := time.Now()
	require.NoError(t, waitForSock(sock))
	assert.Less(t, time.Since(start), 2*time.Second)
	if l := <-ls; l != nil {
		_ = l.Close()
	}
}

func TestDumpPerfmapNonexistentPid(t *testing.T) {
	// Dial with a real pid needs a /proc tree (proc.root is unexported in
	// another package) and SIGQUIT to a live JVM; only the failure path of a
	// pid that cannot exist (> pid_max) is exercised, which must be an error,
	// never a panic.
	err := DumpPerfmap(0xFFFFFFF0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to attach to JVM")
}

func TestDumpPerfmapViaExistingAttachSocket(t *testing.T) {
	// End-to-end through Dial when the JVM attach listener is already up
	// (no attach file, no SIGQUIT): the test process plays the JVM by serving
	// /proc/self/root/tmp/.java_pid<nspid>. Skipped if /proc/self is unusable.
	pid := uint32(os.Getpid())
	nsPid, err := proc.GetNsPid(pid)
	if err != nil {
		t.Skip("no NSpid in /proc/self/status:", err)
	}
	sockPath := proc.Path(pid, fmt.Sprintf("root/tmp/.java_pid%d", nsPid))
	if _, err := os.Stat(sockPath); err == nil {
		t.Skip("attach socket path already exists:", sockPath)
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skip("cannot create attach socket:", err)
	}
	t.Cleanup(func() { _ = l.Close(); _ = os.Remove(sockPath) })
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, len(jattachTestPerfmapRequest))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		got <- string(buf)
		_, _ = c.Write([]byte("0\n"))
	}()
	require.NoError(t, DumpPerfmap(pid))
	assert.Equal(t, jattachTestPerfmapRequest, <-got)
}
