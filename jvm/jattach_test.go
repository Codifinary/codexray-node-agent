// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package jvm

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
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

// jattachHelperEnv turns a re-exec of this test binary into a fake JVM: it
// traps SIGQUIT (instead of the Go runtime's dump-and-exit) and, like HotSpot,
// only starts the attach listener /tmp/.java_pid<pid> when an .attach_pid<pid>
// file exists at signal time. Then it answers one attach request with "0\n".
// It reports what it saw on stdout and exits when its stdin is closed.
const jattachHelperEnv = "JATTACH_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(jattachHelperEnv) == "1" {
		jattachHelperMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func jattachHelperMain() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGQUIT)
	pid := os.Getpid()
	sock := fmt.Sprintf("/tmp/.java_pid%d", pid)
	_ = os.Remove(sock) // stale socket from a dead process that had this pid
	var outMu sync.Mutex
	say := func(format string, a ...any) {
		outMu.Lock()
		defer outMu.Unlock()
		fmt.Printf(format+"\n", a...)
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = os.Remove(sock)
		os.Exit(0)
	}()
	say("ready")
	for range sigs {
		cwd, _ := os.Getwd()
		found := ""
		for _, p := range []string{
			filepath.Join(cwd, fmt.Sprintf(".attach_pid%d", pid)),
			fmt.Sprintf("/tmp/.attach_pid%d", pid),
		} {
			if _, err := os.Stat(p); err == nil {
				found = p
				break
			}
		}
		if found == "" {
			say("sigquit-without-attach-file")
			continue
		}
		say("attach-file %s", found)
		l, err := net.Listen("unix", sock)
		if err != nil {
			say("listen-error %v", err)
			continue
		}
		go func() {
			defer l.Close()
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			buf := make([]byte, len(jattachTestPerfmapRequest))
			if _, err := io.ReadFull(c, buf); err != nil {
				say("read-error %v", err)
				return
			}
			say("request %q", buf)
			_, _ = c.Write([]byte("0\n"))
		}()
	}
}

func TestDialSignalsJVMAndWaitsForAttachListener(t *testing.T) {
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe)
	cmd.Dir = t.TempDir() // the fake JVM's cwd: Dial drops .attach_pid<pid> here
	cmd.Env = append(os.Environ(), jattachHelperEnv+"=1", "GORACE=atexit_sleep_ms=0")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill() // our own child only
			<-done
		}
	})
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func() string {
		t.Helper()
		select {
		case l, ok := <-lines:
			require.True(t, ok, "helper exited early")
			return l
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for the fake JVM")
			return ""
		}
	}
	require.Equal(t, "ready", next())

	pid := uint32(cmd.Process.Pid)
	attachFile := filepath.Join(cmd.Dir, fmt.Sprintf(".attach_pid%d", pid))
	t.Cleanup(func() { _ = os.Remove(fmt.Sprintf("/tmp/.java_pid%d", pid)) })

	j, err := Dial(pid)
	require.NoError(t, err)
	defer j.Close()

	// The attach file must exist when SIGQUIT arrives (HotSpot ignores the
	// signal for attach purposes otherwise) and be removed afterwards.
	assert.Equal(t, "attach-file "+attachFile, next())
	_, statErr := os.Stat(attachFile)
	assert.True(t, os.IsNotExist(statErr), "Dial must remove the attach trigger file")

	require.NoError(t, j.DumpPerfmap())
	assert.Equal(t, fmt.Sprintf("request %q", jattachTestPerfmapRequest), next())
}

func TestDialStaleSocketNoListener(t *testing.T) {
	// A .java_pid socket file left behind by a dead JVM: checkSock says "up",
	// the connect is refused, and Dial must return that error (no signal is
	// sent because the socket file exists).
	pid := uint32(os.Getpid())
	nsPid, err := proc.GetNsPid(pid)
	if err != nil {
		t.Skip("no NSpid in /proc/self/status:", err)
	}
	sockPath := proc.Path(pid, fmt.Sprintf("root/tmp/.java_pid%d", nsPid))
	if _, err := os.Stat(sockPath); err == nil {
		t.Skip("attach socket path already exists:", sockPath)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Skip("cannot create attach socket:", err)
	}
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	l.SetUnlinkOnClose(false)
	require.NoError(t, l.Close())
	require.True(t, checkSock(sockPath))

	j, err := Dial(pid)
	require.Error(t, err)
	assert.Nil(t, j)
	assert.ErrorIs(t, err, syscall.ECONNREFUSED)
}

func TestDialAttachFileNotWritable(t *testing.T) {
	// Neither <pid>/cwd nor <pid>/root/tmp is writable for a foreign-uid
	// process (pid 1 as non-root): Dial must fail before signalling. Guarded so
	// that it only runs when that is provably the case — no signal can be sent.
	if os.Geteuid() == 0 {
		t.Skip("root can write into pid 1's cwd and /tmp; would signal init")
	}
	if _, err := proc.GetNsPid(1); err != nil {
		t.Skip("cannot read /proc/1/status:", err)
	}
	for _, p := range []string{proc.Path(1, "cwd") + "/", proc.Path(1, "root", "tmp")} {
		if _, err := os.Stat(p); err == nil {
			t.Skip("pid 1's", p, "is accessible to this user")
		}
	}
	j, err := Dial(1)
	require.Error(t, err)
	assert.Nil(t, j)
	assert.ErrorIs(t, err, fs.ErrPermission)
}
