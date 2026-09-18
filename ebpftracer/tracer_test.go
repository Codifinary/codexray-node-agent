//go:build amd64

// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"bytes"
	"compress/gzip"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/codifinary/codexray-node-agent/common"

	"github.com/containerd/cgroups"
	cgroupsV2 "github.com/containerd/cgroups/v2"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func skipIfNotVM(t *testing.T) {
	if os.Getenv("VM") == "" {
		t.SkipNow()
	}
}

func TestProcessEvents(t *testing.T) {
	skipIfNotVM(t)
	src := `
		package main
		
		import (
			"bytes"
			"os"
			"strconv"
			"time"
		)
		
		func main() {
			mb, _ := strconv.Atoi(os.Args[1])
			sleep, _ := time.ParseDuration(os.Args[2])
			bytes.Repeat([]byte("x"), mb*1024*1024)
			time.Sleep(sleep)
		}
	`
	program := path.Join(t.TempDir(), "program")
	require.NoError(t, os.WriteFile(program+".go", []byte(src), 0644))
	require.NoError(t, exec.Command("go", "build", "-o", program, program+".go").Run())

	getEvent, stop := runTracer(t, false)
	defer stop()
	for {
		if e := getEvent(); e == nil {
			break
		}
	}

	p1 := exec.Command(program, "600", "10s")
	require.NoError(t, p1.Start())
	time.Sleep(time.Second)
	assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: uint32(p1.Process.Pid)}, *getEvent())

	// p1 should be killed by the OOM killer, because VM have only 1 GB of memory total
	p2 := exec.Command(program, "400", "1s")
	require.NoError(t, p2.Run())
	assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: uint32(p2.Process.Pid)}, *getEvent())

	require.Error(t, p1.Wait())
	assert.Equal(t, Event{Type: EventTypeProcessExit, Reason: EventReasonOOMKill, Pid: uint32(p1.Process.Pid)}, *getEvent())
	assert.Equal(t, Event{Type: EventTypeProcessExit, Pid: uint32(p2.Process.Pid)}, *getEvent())

	var limit int64 = 200 * 1024 * 1024
	// p3 should be killed by the OOM killer, because 300 MB > 200 MB cgroup limit
	p3 := exec.Command(program, "300", "3s")
	require.NoError(t, p3.Start())
	switch cgroups.Mode() {
	case cgroups.Legacy, cgroups.Hybrid:
		control, err := cgroups.New(cgroups.V1, cgroups.StaticPath("/program"), &specs.LinuxResources{
			Memory: &specs.LinuxMemory{Limit: &limit},
		})
		require.NoError(t, err)
		defer control.Delete()
		require.NoError(t, control.Add(cgroups.Process{Pid: p3.Process.Pid}))
	case cgroups.Unified:
		control, err := cgroupsV2.NewManager("/sys/fs/cgroup", "/program", &cgroupsV2.Resources{Memory: &cgroupsV2.Memory{Max: &limit}})
		require.NoError(t, err)
		defer control.Delete()
		require.NoError(t, control.AddProc(uint64(p3.Process.Pid)))
	}
	require.Error(t, p3.Wait())
	assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: uint32(p3.Process.Pid)}, *getEvent())
	assert.Equal(t, Event{Type: EventTypeProcessExit, Reason: EventReasonOOMKill, Pid: uint32(p3.Process.Pid)}, *getEvent())

	for {
		e := getEvent()
		if e == nil {
			break
		}
		t.Errorf("unexpected event %+v", e)
	}
}

func TestTcpEvents(t *testing.T) {
	skipIfNotVM(t)
	l, err := net.Listen("tcp", "127.0.0.1:8080")
	require.NoError(t, err)
	listenAddr := l.Addr().String()
	remoteAddr := "127.0.0.1:8080"
	c, err := net.DialTimeout("tcp", remoteAddr, 100*time.Millisecond)
	require.NoError(t, err)
	localAddr := c.LocalAddr().String()
	time.Sleep(100 * time.Millisecond)

	getEvent, stop := runTracer(t, false)
	defer stop()

	pid := uint32(os.Getpid())

	is := func(e *Event, typ EventType, sAddr string, dAddr string, pid uint32) bool {
		if e == nil {
			return false
		}
		sa := e.SrcAddr.String()
		if strings.HasSuffix(sAddr, ":") {
			sa = fmt.Sprintf("%s:", e.SrcAddr.IP())
		}
		da := e.DstAddr.String()
		return e.Type == typ && e.Pid == pid && sa == sAddr && da == dAddr
	}

	listenFound := false
	connectFound := false
	for {
		e := getEvent()
		if e == nil {
			break
		}
		if is(e, EventTypeListenOpen, listenAddr, "0.0.0.0:0", pid) {
			listenFound = true
		}
		if is(e, EventTypeConnectionOpen, localAddr, remoteAddr, pid) {
			connectFound = true
		}
	}
	if !listenFound {
		t.Errorf("expected %s on %s", EventTypeListenOpen, l.Addr())
	}
	if !connectFound {
		t.Errorf("expected %s to %s", EventTypeConnectionOpen, l.Addr())
	}

	nextIs := func(typ EventType, sAddr string, dAddr string, pid uint32) {
		e := getEvent()
		if !is(e, typ, sAddr, dAddr, pid) {
			expected := fmt.Sprintf("%-20s %6d: %s -> %s", typ, pid, sAddr, dAddr)
			actual := "nil"
			if e != nil {
				actual = fmt.Sprintf("%-20s %6d: %s -> %s", e.Type, e.Pid, e.SrcAddr, e.DstAddr)
			}
			assert.Equal(t, expected, actual)
		}
	}

	require.NoError(t, c.Close())
	nextIs(EventTypeConnectionClose, localAddr, listenAddr, 0)
	nextIs(EventTypeConnectionClose, listenAddr, localAddr, 0)

	require.NoError(t, l.Close())
	nextIs(EventTypeListenClose, listenAddr, "0.0.0.0:0", pid)

	c, err = net.DialTimeout("tcp", listenAddr, 100*time.Millisecond)
	require.Error(t, err)
	nextIs(EventTypeConnectionError, "127.0.0.1:", listenAddr, pid)

	l, err = net.Listen("tcp4", ":8080")
	require.NoError(t, err)
	listenAddr = l.Addr().String()
	nextIs(EventTypeListenOpen, listenAddr, "0.0.0.0:0", pid)

	c, err = net.DialTimeout("tcp", remoteAddr, 100*time.Millisecond)
	require.NoError(t, err)
	localAddr = c.LocalAddr().String()
	nextIs(EventTypeConnectionOpen, localAddr, remoteAddr, pid)

	require.NoError(t, exec.Command("tc", "qdisc", "add", "dev", "lo", "root", "netem", "loss", "100%").Run())
	getEvent()
	getEvent()
	c.Write([]byte("hello"))
	nextIs(EventTypeTCPRetransmit, localAddr, remoteAddr, 0)
	require.NoError(t, exec.Command("tc", "qdisc", "del", "dev", "lo", "root", "netem").Run())
	getEvent()
	getEvent()
	func() {
		timer := time.NewTimer(time.Second)
		for {
			select {
			case <-timer.C:
				return
			default:
				e := getEvent()
				require.True(t, e == nil || e.Type == EventTypeTCPRetransmit)
			}
		}
	}()

	require.NoError(t, c.Close())
	nextIs(EventTypeConnectionClose, localAddr, remoteAddr, 0)
	nextIs(EventTypeConnectionClose, remoteAddr, localAddr, 0)

	require.NoError(t, l.Close())
	nextIs(EventTypeListenClose, listenAddr, "0.0.0.0:0", pid)

	for {
		e := getEvent()
		if e == nil {
			break
		}
		t.Errorf("unexpected event %+v", e)
	}
}

func TestFileEvents(t *testing.T) {
	skipIfNotVM(t)
	src := `
		package main
		
		import (
			"os"
			"strconv"
			"syscall"
			"unsafe"
			"time"
		)
		
		func main() {
			call, _ := strconv.Atoi(os.Args[1])
			path := os.Args[2]
			flags, _ := strconv.Atoi(os.Args[3])
			filename, _ := syscall.BytePtrFromString(path)
			var err syscall.Errno
			switch call {
			case syscall.SYS_OPEN:
				_, _, err = syscall.Syscall6(syscall.SYS_OPEN, uintptr(unsafe.Pointer(filename)), uintptr(flags), 0, 0, 0, 0)
			case syscall.SYS_OPENAT:
				AT_FDCWD := -100
				_, _, err = syscall.Syscall6(syscall.SYS_OPENAT, uintptr(AT_FDCWD), uintptr(unsafe.Pointer(filename)), uintptr(flags), 0, 0, 0)
			}
			time.Sleep(100 * time.Millisecond)
			os.Exit(int(err))
		}
	`
	require.NoError(t, os.Chdir(t.TempDir()))
	require.NoError(t, os.WriteFile("program.go", []byte(src), 0644))
	out, err := exec.Command("go", "build", "-o", "program", "program.go").CombinedOutput()
	require.Equal(t, "", string(out))
	require.NoError(t, err)

	getEvent, stop := runTracer(t, false)
	defer stop()
	for {
		if e := getEvent(); e == nil {
			break
		}
	}

	for _, call := range []int{syscall.SYS_OPEN, syscall.SYS_OPENAT} {
		run := func(file string, flag int) (uint32, error) {
			p := exec.Command("./program", strconv.Itoa(call), file, strconv.Itoa(flag))
			err := p.Run()
			return uint32(p.Process.Pid), err
		}

		pid, err := run("program.go", os.O_RDONLY)
		assert.NoError(t, err)
		assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: pid}, *getEvent())
		assert.Equal(t, Event{Type: EventTypeProcessExit, Pid: pid}, *getEvent())

		pid, err = run("program.go", os.O_WRONLY)
		assert.NoError(t, err)
		assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: pid}, *getEvent())
		assert.Equal(t, Event{Type: EventTypeFileOpen, Pid: pid, Fd: 3}, *getEvent())
		assert.Equal(t, Event{Type: EventTypeProcessExit, Pid: pid}, *getEvent())

		pid, err = run("program.go", os.O_RDWR)
		assert.NoError(t, err)
		assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: pid}, *getEvent())
		assert.Equal(t, Event{Type: EventTypeFileOpen, Pid: pid, Fd: 3}, *getEvent())
		assert.Equal(t, Event{Type: EventTypeProcessExit, Pid: pid}, *getEvent())

		// open error: text file busy
		pid, err = run("program", os.O_RDWR)
		assert.Error(t, err)
		assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: pid}, *getEvent())
		assert.Equal(t, Event{Type: EventTypeProcessExit, Pid: pid}, *getEvent())

		// ignoring /proc/*, /dev/*, /sys/*
		for _, f := range []string{"/proc/sys/fs/file-max", "/dev/null", "/sys/kernel/profiling"} {
			pid, err = run(f, os.O_RDWR)
			assert.NoError(t, err)
			assert.Equal(t, Event{Type: EventTypeProcessStart, Pid: pid}, *getEvent())
			assert.Equal(t, Event{Type: EventTypeProcessExit, Pid: pid}, *getEvent())
		}

		for {
			e := getEvent()
			if e == nil {
				break
			}
			t.Errorf("unexpected event %+v", e)
		}
	}
}

func runTracer(t *testing.T, verbose bool) (func() *Event, func()) {
	events := make(chan Event, 1000)
	done := make(chan bool, 1)

	var uname unix.Utsname
	assert.NoError(t, unix.Uname(&uname))
	assert.NoError(t, common.SetKernelVersion(string(bytes.Split(uname.Release[:], []byte{0})[0])))

	go func() {
		tt := NewTracer(0, 0, false)
		err := tt.Run(events)
		require.NoError(t, err)
		<-done
		tt.Close()
	}()

	stop := func() {
		done <- true
	}

	get := func() *Event {
		select {
		case e := <-events:
			if verbose {
				fmt.Printf("%+v\n", e)
			}
			return &e
		case <-time.NewTimer(time.Second).C:
			return nil
		}
	}

	return get, stop
}

// --- pure (non-VM) tests: event record layouts must match ebpftracer/ebpf/*.c byte-for-byte ---

func TestEventStructSizesMatchC(t *testing.T) {
	// struct proc_event { __u32 type; __u32 pid; __u32 reason; }                          sizeof = 12
	assert.Equal(t, 12, binary.Size(procEvent{}))
	// struct file_event { __u32 type; __u32 pid; __u64 fd; __u64 mnt; __u64 log; }        sizeof = 32
	assert.Equal(t, 32, binary.Size(fileEvent{}))
	// struct tcp_event: ...; __u16 sport,dport,aport; __u8 saddr[16],daddr[16],aaddr[16]; ends at 102 (+2 tail pad)
	assert.Equal(t, 102, binary.Size(tcpEvent{}))
	// struct l7_event: header is offsetof(payload) = 48, payload[MAX_PAYLOAD_SIZE] follows
	assert.Equal(t, 48, binary.Size(l7Event{}))
	assert.Equal(t, 1024, MaxPayloadSize)
	// struct connection_id { __u64 fd; __u32 pid; } sizeof = 16 (padded); struct connection = 3 x __u64
	assert.Equal(t, 16, binary.Size(ConnectionId{}))
	assert.Equal(t, 24, binary.Size(Connection{}))
}

func TestDecodeProcAndFileEvents(t *testing.T) {
	le := binary.LittleEndian
	raw := make([]byte, 12)
	le.PutUint32(raw[0:], uint32(EventTypeProcessExit))
	le.PutUint32(raw[4:], 4242)
	le.PutUint32(raw[8:], uint32(EventReasonOOMKill))
	pe := procEvent{}
	require.NoError(t, binary.Read(bytes.NewBuffer(raw), binary.LittleEndian, &pe))
	assert.Equal(t, procEvent{Type: EventTypeProcessExit, Pid: 4242, Reason: 1}, pe)
	assert.Equal(t, "oom-kill", EventReason(pe.Reason).String())

	raw = make([]byte, 32)
	le.PutUint32(raw[0:], uint32(EventTypeFileOpen))
	le.PutUint32(raw[4:], 7)
	le.PutUint64(raw[8:], 3)
	le.PutUint64(raw[16:], 0xdeadbeef)
	le.PutUint64(raw[24:], 1)
	fe := fileEvent{}
	require.NoError(t, binary.Read(bytes.NewBuffer(raw), binary.LittleEndian, &fe))
	assert.Equal(t, fileEvent{Type: EventTypeFileOpen, Pid: 7, Fd: 3, Mnt: 0xdeadbeef, Log: 1}, fe)

	// a short record is an error, never a panic
	require.Error(t, binary.Read(bytes.NewBuffer(raw[:20]), binary.LittleEndian, &fileEvent{}))
	require.Error(t, binary.Read(bytes.NewBuffer(nil), binary.LittleEndian, &procEvent{}))
}

func TestDecodeTcpEvent(t *testing.T) {
	le := binary.LittleEndian
	raw := make([]byte, 104)                                 // sizeof(struct tcp_event) incl. tail padding
	le.PutUint64(raw[0:], 11)                                // fd
	le.PutUint64(raw[8:], 1_000_000)                         // timestamp
	le.PutUint64(raw[16:], 2_500_000)                        // duration (ns)
	le.PutUint32(raw[24:], uint32(EventTypeConnectionClose)) // type
	le.PutUint32(raw[28:], 99)                               // pid
	le.PutUint64(raw[32:], 1234)                             // bytes_sent
	le.PutUint64(raw[40:], 5678)                             // bytes_received
	le.PutUint16(raw[48:], 50000)                            // sport
	le.PutUint16(raw[50:], 443)                              // dport
	le.PutUint16(raw[52:], 8443)                             // aport
	copy(raw[54:70], net.ParseIP("10.0.0.1").To16())         // saddr (v4-mapped, as the tracepoint reports it)
	copy(raw[70:86], net.ParseIP("2001:db8::1").To16())      // daddr
	copy(raw[86:102], net.ParseIP("10.96.0.10").To16())      // aaddr

	v := tcpEvent{}
	require.NoError(t, binary.Read(bytes.NewBuffer(raw), binary.LittleEndian, &v))
	assert.Equal(t, uint64(11), v.Fd)
	assert.Equal(t, uint64(1_000_000), v.Timestamp)
	assert.Equal(t, uint64(2_500_000), v.Duration)
	assert.Equal(t, EventTypeConnectionClose, v.Type)
	assert.Equal(t, uint32(99), v.Pid)
	assert.Equal(t, uint64(1234), v.BytesSent)
	assert.Equal(t, uint64(5678), v.BytesReceived)
	assert.Equal(t, "10.0.0.1:50000", ipPort(v.SAddr, v.SPort).String())
	assert.Equal(t, "[2001:db8::1]:443", ipPort(v.DAddr, v.DPort).String())
	assert.Equal(t, "10.96.0.10:8443", ipPort(v.AAddr, v.Aport).String())
	// zero actual destination (no conntrack entry) stays zero -> ActualDestinationIfKnown falls back
	assert.Equal(t, uint16(0), ipPort([16]byte{}, 0).Port())
}

func TestDecodeL7Event(t *testing.T) {
	le := binary.LittleEndian
	payload := []byte("GET /health HTTP/1.1\r\nHost: x\r\n\r\n")
	raw := make([]byte, 48+MaxPayloadSize)
	le.PutUint64(raw[0:], 5)           // fd
	le.PutUint64(raw[8:], 777)         // connection_timestamp
	le.PutUint32(raw[16:], 1001)       // pid
	le.PutUint32(raw[20:], 0xffffffff) // status (signed): -1
	le.PutUint64(raw[24:], 3_000_000)  // duration
	raw[32] = 1                        // protocol
	raw[33] = 2                        // method
	le.PutUint32(raw[36:], 42)         // statement_id
	le.PutUint64(raw[40:], uint64(len(payload)))
	copy(raw[48:], payload)

	r := bytes.NewBuffer(raw)
	v := l7Event{}
	require.NoError(t, binary.Read(r, binary.LittleEndian, &v))
	assert.Equal(t, l7Event{Fd: 5, ConnectionTimestamp: 777, Pid: 1001, Status: -1, Duration: 3_000_000,
		Protocol: 1, Method: 2, StatementId: 42, PayloadSize: uint64(len(payload))}, v)
	rest := r.Bytes()
	assert.Len(t, rest, MaxPayloadSize, "the whole fixed-size payload buffer follows the header")
	assert.Equal(t, payload, rest[:v.PayloadSize])
}

func TestEventTypeAndReasonString(t *testing.T) {
	names := map[EventType]string{
		EventTypeProcessStart: "process-start", EventTypeProcessExit: "process-exit",
		EventTypeConnectionOpen: "connection-open", EventTypeConnectionClose: "connection-close",
		EventTypeConnectionError: "connection-error", EventTypeListenOpen: "listen-open",
		EventTypeListenClose: "listen-close", EventTypeFileOpen: "file-open",
		EventTypeTCPRetransmit: "tcp-retransmit", EventTypeL7Request: "l7-request",
	}
	for typ, name := range names {
		assert.Equal(t, name, typ.String())
	}
	assert.Equal(t, "unknown: 99", EventType(99).String())
	assert.Equal(t, "none", EventReasonNone.String())
	assert.Equal(t, "oom-kill", EventReasonOOMKill.String())
	assert.Equal(t, "unknown: 7", EventReason(7).String())
}

func TestIsCtxExtraPaddingRequired(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, isCtxExtraPaddingRequired(dir), "missing format file")

	format := filepath.Join(dir, "events/task/task_newtask/format")
	require.NoError(t, os.MkdirAll(filepath.Dir(format), 0o755))
	require.NoError(t, os.WriteFile(format, []byte(
		"name: task_newtask\nID: 1\nformat:\n\tfield:unsigned short common_type;\toffset:0;\tsize:2;\tsigned:0;\n"+
			"\tfield:unsigned char common_flags;\toffset:2;\tsize:1;\tsigned:0;\n"), 0o644))
	assert.False(t, isCtxExtraPaddingRequired(dir))

	require.NoError(t, os.WriteFile(format, []byte(
		"format:\n\tfield:unsigned short common_type;\toffset:0;\tsize:2;\tsigned:0;\n"+
			"\tfield:unsigned char common_preempt_lazy_count;\toffset:4;\tsize:1;\tsigned:0;\n"), 0o644))
	assert.True(t, isCtxExtraPaddingRequired(dir))
}

func TestEbpfProgVariants(t *testing.T) {
	// Tracer.ebpf picks the FIRST variant whose version <= kernel and whose flags match,
	// so variants must be ordered newest-first within each flag set.
	for _, arch := range []string{"amd64", "arm64"} {
		progs, ok := ebpfProgs[arch]
		require.True(t, ok, arch)
		var plain []string
		var padded []string
		var prev *common.Version
		for _, p := range progs {
			v, err := common.VersionFromString(p.version)
			require.NoError(t, err)
			switch p.flags {
			case "":
				plain = append(plain, p.version)
				if prev != nil {
					assert.True(t, prev.GreaterOrEqual(v) && *prev != v, "%s: %s listed after %s", arch, p.version, prev)
				}
				vv := v
				prev = &vv
			case "ctx-extra-padding":
				padded = append(padded, p.version)
			default:
				t.Errorf("%s: unexpected flags %q", arch, p.flags)
			}

			// every blob must decode to a little-endian BPF ELF object
			gz, err := gzip.NewReader(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(p.prog)))
			require.NoError(t, err)
			obj, err := io.ReadAll(gz)
			require.NoError(t, err)
			ef, err := elf.NewFile(bytes.NewReader(obj))
			require.NoError(t, err, "%s %s %s", arch, p.version, p.flags)
			assert.Equal(t, elf.EM_BPF, ef.Machine)
			assert.Equal(t, binary.LittleEndian, ef.ByteOrder)
		}
		assert.Equal(t, []string{"5.12", "5.6", "4.20", "4.16"}, plain, arch)
		assert.Equal(t, []string{"5.12"}, padded, arch)
	}
}

func TestNewTracer(t *testing.T) {
	tr := NewTracer(0, 0, true)
	assert.True(t, tr.disableL7Tracing)
	assert.NotNil(t, tr.readers)
	assert.NotNil(t, tr.uprobes)
	// L7 disabled -> TLS uprobes are never attached
	assert.Nil(t, tr.AttachOpenSslUprobes(uint32(os.Getpid())))
	links, isGo := tr.AttachGoTlsUprobes(uint32(os.Getpid()))
	assert.Nil(t, links)
	assert.False(t, isGo)
}
