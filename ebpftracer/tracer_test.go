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
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/florianl/go-conntrack"
	"inet.af/netaddr"

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

	require.NoError(t, os.Remove(format))
	require.NoError(t, os.Mkdir(format, 0o755))
	assert.False(t, isCtxExtraPaddingRequired(dir), "unreadable format file")
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

// --- runEventsReader / ebpf / Run / Close with the kernel faked out ---

type tracerTestRead struct {
	rec perf.Record
	err error
}

type tracerTestReader struct {
	reads chan tracerTestRead
	done  chan struct{}
	once  sync.Once

	mu        sync.Mutex
	closed    int
	deadlines []time.Duration
	size      int
	opts      perf.ReaderOptions
}

func newTracerTestReader(reads ...tracerTestRead) *tracerTestReader {
	r := &tracerTestReader{reads: make(chan tracerTestRead, 100), done: make(chan struct{})}
	for _, rd := range reads {
		r.reads <- rd
	}
	return r
}

// Read returns the queued reads first and perf.ErrClosed once the queue is drained and the reader is closed.
func (r *tracerTestReader) Read() (perf.Record, error) {
	select {
	case rd := <-r.reads:
		return rd.rec, rd.err
	default:
	}
	select {
	case rd := <-r.reads:
		return rd.rec, rd.err
	case <-r.done:
		return perf.Record{}, fmt.Errorf("read: %w", perf.ErrClosed)
	}
}

func (r *tracerTestReader) SetDeadline(t time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines = append(r.deadlines, time.Until(t))
}

func (r *tracerTestReader) Close() error {
	r.mu.Lock()
	r.closed++
	r.mu.Unlock()
	r.once.Do(func() { close(r.done) })
	return nil
}

func (r *tracerTestReader) closedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *tracerTestReader) maxDeadline() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	var res time.Duration
	for _, d := range r.deadlines {
		if d > res {
			res = d
		}
	}
	return res
}

func tracerTestSample(raw []byte) tracerTestRead {
	return tracerTestRead{rec: perf.Record{RawSample: raw}}
}

func tracerTestProcRecord(typ EventType, pid uint32, reason EventReason) []byte {
	raw := make([]byte, 12)
	binary.LittleEndian.PutUint32(raw[0:], uint32(typ))
	binary.LittleEndian.PutUint32(raw[4:], pid)
	binary.LittleEndian.PutUint32(raw[8:], uint32(reason))
	return raw
}

func tracerTestFileRecord(pid uint32, fd, mnt, log uint64) []byte {
	raw := make([]byte, 32)
	binary.LittleEndian.PutUint32(raw[0:], uint32(EventTypeFileOpen))
	binary.LittleEndian.PutUint32(raw[4:], pid)
	binary.LittleEndian.PutUint64(raw[8:], fd)
	binary.LittleEndian.PutUint64(raw[16:], mnt)
	binary.LittleEndian.PutUint64(raw[24:], log)
	return raw
}

func tracerTestTcpRecord(typ EventType, pid uint32, src, dst, actual string) []byte {
	le := binary.LittleEndian
	raw := make([]byte, 104)
	le.PutUint64(raw[0:], 11)
	le.PutUint64(raw[8:], 1_000_000)
	le.PutUint64(raw[16:], 2_500_000)
	le.PutUint32(raw[24:], uint32(typ))
	le.PutUint32(raw[28:], pid)
	le.PutUint64(raw[32:], 1234)
	le.PutUint64(raw[40:], 5678)
	for i, a := range []string{src, dst, actual} {
		ipp := netaddr.MustParseIPPort(a)
		le.PutUint16(raw[48+2*i:], ipp.Port())
		ip := ipp.IP().As16()
		copy(raw[54+16*i:], ip[:])
	}
	return raw
}

func tracerTestL7Record(pid uint32, payloadSize uint64, payload []byte) []byte {
	le := binary.LittleEndian
	raw := make([]byte, 48+MaxPayloadSize)
	le.PutUint64(raw[0:], 5)
	le.PutUint64(raw[8:], 777)
	le.PutUint32(raw[16:], pid)
	le.PutUint32(raw[20:], 200)
	le.PutUint64(raw[24:], 3_000_000)
	raw[32] = uint8(l7.ProtocolHTTP)
	raw[33] = uint8(l7.MethodStatementPrepare)
	le.PutUint32(raw[36:], 42)
	le.PutUint64(raw[40:], payloadSize)
	copy(raw[48:], payload)
	return raw
}

func tracerTestRunReader(typ perfMapType, readTimeout time.Duration, reads ...tracerTestRead) ([]Event, *tracerTestReader) {
	r := newTracerTestReader(reads...)
	_ = r.Close()
	ch := make(chan Event, 100)
	runEventsReader("test", r, ch, typ, readTimeout)
	close(ch)
	var res []Event
	for e := range ch {
		res = append(res, e)
	}
	return res, r
}

func TestRunEventsReaderProcAndFileEvents(t *testing.T) {
	events, r := tracerTestRunReader(perfMapTypeProcEvents, 0,
		tracerTestSample(tracerTestProcRecord(EventTypeProcessStart, 1, EventReasonNone)),
		tracerTestRead{rec: perf.Record{LostSamples: 10}},
		tracerTestRead{err: os.ErrDeadlineExceeded},
		tracerTestSample([]byte{1, 2, 3}),
		tracerTestSample(tracerTestProcRecord(EventTypeProcessExit, 2, EventReasonOOMKill)),
	)
	assert.Equal(t, []Event{
		{Type: EventTypeProcessStart, Pid: 1},
		{Type: EventTypeProcessExit, Reason: EventReasonOOMKill, Pid: 2},
	}, events)
	// a deadline is set before every read: 5 queued + the final closed read
	assert.Len(t, r.deadlines, 6)
	for _, d := range r.deadlines {
		assert.True(t, d > 50*time.Millisecond && d <= 100*time.Millisecond, "default read timeout, got %s", d)
	}

	events, r = tracerTestRunReader(perfMapTypeFileEvents, 10*time.Millisecond,
		tracerTestSample(tracerTestFileRecord(7, 3, 0xdead, 0)),
		tracerTestSample(tracerTestFileRecord(8, 4, 0xbeef, 1)),
		tracerTestSample(make([]byte, 20)),
	)
	assert.Equal(t, []Event{
		{Type: EventTypeFileOpen, Pid: 7, Fd: 3, Mnt: 0xdead},
		{Type: EventTypeFileOpen, Pid: 8, Fd: 4, Mnt: 0xbeef, Log: true},
	}, events)
	assert.LessOrEqual(t, r.maxDeadline(), 10*time.Millisecond)
}

func TestRunEventsReaderTcpEvents(t *testing.T) {
	events, _ := tracerTestRunReader(perfMapTypeTCPEvents, 0,
		tracerTestSample(tracerTestTcpRecord(EventTypeConnectionOpen, 99, "10.0.0.1:50000", "10.96.0.10:443", "10.244.0.5:8443")),
		tracerTestSample(tracerTestTcpRecord(EventTypeConnectionClose, 99, "[2001:db8::1]:50000", "[2001:db8::2]:443", "0.0.0.0:0")),
		tracerTestSample(make([]byte, 101)),
	)
	src, dst, actual := netaddr.MustParseIPPort("10.0.0.1:50000"), netaddr.MustParseIPPort("10.96.0.10:443"), netaddr.MustParseIPPort("10.244.0.5:8443")
	assert.Equal(t, []Event{
		{Type: EventTypeConnectionOpen, Pid: 99, SrcAddr: src, DstAddr: dst, ActualDstAddr: actual, Fd: 11, Timestamp: 1_000_000, Duration: 2500 * time.Microsecond},
		{Type: EventTypeConnectionClose, Pid: 99, SrcAddr: netaddr.MustParseIPPort("[2001:db8::1]:50000"), DstAddr: netaddr.MustParseIPPort("[2001:db8::2]:443"),
			ActualDstAddr: netaddr.IPPortFrom(netaddr.IPv4(0, 0, 0, 0), 0), Fd: 11, Timestamp: 1_000_000, Duration: 2500 * time.Microsecond,
			TrafficStats: &TrafficStats{BytesSent: 1234, BytesReceived: 5678}},
	}, events)
}

func TestRunEventsReaderL7Events(t *testing.T) {
	payload := []byte("GET /health HTTP/1.1\r\n")
	full := bytes.Repeat([]byte("x"), MaxPayloadSize)
	events, _ := tracerTestRunReader(perfMapTypeL7Events, 0,
		tracerTestSample(tracerTestL7Record(1, 0, payload)),
		tracerTestSample(tracerTestL7Record(2, uint64(len(payload)), payload)),
		tracerTestSample(tracerTestL7Record(3, 5000, full)),
		tracerTestSample(make([]byte, 47)),
	)
	require.Len(t, events, 3)
	req := func(payload []byte) *l7.RequestData {
		return &l7.RequestData{Protocol: l7.ProtocolHTTP, Status: 200, Duration: 3 * time.Millisecond,
			Method: l7.MethodStatementPrepare, StatementId: 42, Payload: payload}
	}
	assert.Equal(t, Event{Type: EventTypeL7Request, Pid: 1, Fd: 5, Timestamp: 777, L7Request: req(nil)}, events[0])
	assert.Equal(t, Event{Type: EventTypeL7Request, Pid: 2, Fd: 5, Timestamp: 777, L7Request: req(payload)}, events[1])
	assert.Equal(t, Event{Type: EventTypeL7Request, Pid: 3, Fd: 5, Timestamp: 777, L7Request: req(full)}, events[2], "payload is capped at MaxPayloadSize")
}

func TestRunEventsReaderUnknownMapType(t *testing.T) {
	events, r := tracerTestRunReader(perfMapType(99), 0, tracerTestSample(tracerTestProcRecord(EventTypeProcessStart, 1, 0)))
	assert.Empty(t, events)
	assert.Len(t, r.deadlines, 2)
}

type tracerTestEbpfEnv struct {
	specs       []*ebpf.CollectionSpec
	collErr     error
	readers     []*tracerTestReader
	readerErr   func(n int) error
	tracepoints []string
	kprobes     []string
	links       []*elfTestLink
	linkErr     func(name string) error
}

func tracerTestKernel(t *testing.T, version string) {
	prev := common.GetKernelVersion()
	t.Cleanup(func() {
		if prev.Major > 0 {
			_ = common.SetKernelVersion(prev.String())
		}
	})
	require.NoError(t, common.SetKernelVersion(version))
}

func tracerTestTraceFs(t *testing.T, paths ...string) {
	prev := traceFsPaths
	t.Cleanup(func() { traceFsPaths = prev })
	traceFsPaths = paths
}

// tracerTestFakeEbpf fakes collection loading, perf readers and kprobe/tracepoint links,
// with a tracefs that does (padding) or does not require the ctx-extra-padding variant.
func tracerTestFakeEbpf(t *testing.T, kernel string, padding bool) *tracerTestEbpfEnv {
	tracerTestKernel(t, kernel)
	traceFs := t.TempDir()
	format := filepath.Join(traceFs, "events/task/task_newtask/format")
	require.NoError(t, os.MkdirAll(filepath.Dir(format), 0o755))
	field := "common_pid"
	if padding {
		field = "common_preempt_lazy_count"
	}
	require.NoError(t, os.WriteFile(format, []byte("format:\n\tfield:unsigned char "+field+";\toffset:4;\tsize:1;\tsigned:0;\n"), 0o644))
	tracerTestTraceFs(t, filepath.Join(t.TempDir(), "missing"), traceFs)

	env := &tracerTestEbpfEnv{}
	prevColl, prevReader, prevTp, prevKp := newCollection, newPerfReader, linkTracepoint, linkKprobe
	t.Cleanup(func() {
		newCollection, newPerfReader, linkTracepoint, linkKprobe = prevColl, prevReader, prevTp, prevKp
	})
	newCollection = func(spec *ebpf.CollectionSpec, opts ebpf.CollectionOptions) (*ebpf.Collection, error) {
		env.specs = append(env.specs, spec)
		if env.collErr != nil {
			return nil, env.collErr
		}
		c := &ebpf.Collection{Programs: map[string]*ebpf.Program{}, Maps: map[string]*ebpf.Map{}}
		for name := range spec.Programs {
			c.Programs[name] = nil
		}
		for name := range spec.Maps {
			c.Maps[name] = nil
		}
		return c, nil
	}
	newPerfReader = func(array *ebpf.Map, perCPUBuffer int, opts perf.ReaderOptions) (perfReader, error) {
		if env.readerErr != nil {
			if err := env.readerErr(len(env.readers) + 1); err != nil {
				return nil, err
			}
		}
		r := newTracerTestReader()
		r.size, r.opts = perCPUBuffer, opts
		env.readers = append(env.readers, r)
		return r, nil
	}
	attach := func(name string) (link.Link, error) {
		if env.linkErr != nil {
			if err := env.linkErr(name); err != nil {
				return nil, err
			}
		}
		l := &elfTestLink{}
		env.links = append(env.links, l)
		return l, nil
	}
	linkTracepoint = func(group, name string, prog *ebpf.Program, opts *link.TracepointOptions) (link.Link, error) {
		assert.Nil(t, opts)
		env.tracepoints = append(env.tracepoints, group+"/"+name)
		return attach(name)
	}
	linkKprobe = func(symbol string, prog *ebpf.Program, opts *link.KprobeOptions) (link.Link, error) {
		assert.Nil(t, opts)
		env.kprobes = append(env.kprobes, symbol)
		return attach(symbol)
	}
	return env
}

// programs returns the sorted AttachTo of the loaded spec's programs of the given type
// (uprobe=true: the names of the uprobe programs).
func (env *tracerTestEbpfEnv) programs(typ ebpf.ProgramType, uprobes bool, except ...string) []string {
	var res []string
	for name, p := range env.specs[0].Programs {
		if p.Type != typ || strings.HasPrefix(p.SectionName, "uprobe/") != uprobes || slices.Contains(except, name) {
			continue
		}
		if uprobes {
			res = append(res, name)
		} else {
			res = append(res, p.AttachTo)
		}
	}
	sort.Strings(res)
	return res
}

func (env *tracerTestEbpfEnv) linksClosed() []int {
	var res []int
	for _, l := range env.links {
		res = append(res, l.closed)
	}
	return res
}

func (env *tracerTestEbpfEnv) readersClosed() []int {
	var res []int
	for _, r := range env.readers {
		res = append(res, r.closedCount())
	}
	return res
}

func tracerTestRepeat(v, n int) []int {
	res := make([]int, n)
	for i := range res {
		res[i] = v
	}
	return res
}

var tracerTestL7Syscalls = []string{
	"sys_enter_writev", "sys_enter_write", "sys_enter_sendto", "sys_enter_sendmsg", "sys_enter_sendmmsg",
	"sys_enter_read", "sys_enter_readv", "sys_enter_recvfrom", "sys_enter_recvmsg",
	"sys_exit_read", "sys_exit_readv", "sys_exit_recvfrom", "sys_exit_recvmsg",
}

func tracerTestWaitEvents(t *testing.T, ch <-chan Event, n int) map[uint32]Event {
	res := map[uint32]Event{}
	timeout := time.After(5 * time.Second)
	for len(res) < n {
		select {
		case e := <-ch:
			res[e.Pid] = e
		case <-timeout:
			t.Fatalf("got %d of %d events", len(res), n)
		}
	}
	return res
}

func TestTracerEbpf(t *testing.T) {
	env := tracerTestFakeEbpf(t, "6.8.0", false)
	tr := NewTracer(0, 0, false)
	ch := make(chan Event, 100)
	require.NoError(t, tr.ebpf(ch))
	require.Len(t, env.specs, 1)
	require.NotNil(t, tr.collection)

	// perf readers: one per perf map, each decoding its own record type
	pageSize := os.Getpagesize()
	expected := map[string]int{"proc_events": 4, "tcp_listen_events": 4, "tcp_connect_events": 8, "tcp_retransmit_events": 4, "file_events": 4, "l7_events": 32}
	require.Len(t, tr.readers, len(expected))
	for name, pages := range expected {
		r := tr.readers[name].(*tracerTestReader)
		assert.Equal(t, pages*pageSize, r.size, name)
		assert.Equal(t, perf.ReaderOptions{WakeupEvents: 100}, r.opts, name)
		assert.Contains(t, env.specs[0].Maps, name)
	}
	reader := func(name string) *tracerTestReader { return tr.readers[name].(*tracerTestReader) }
	reader("proc_events").reads <- tracerTestSample(tracerTestProcRecord(EventTypeProcessStart, 1, 0))
	reader("tcp_listen_events").reads <- tracerTestSample(tracerTestTcpRecord(EventTypeListenOpen, 2, "0.0.0.0:80", "0.0.0.0:0", "0.0.0.0:0"))
	reader("tcp_connect_events").reads <- tracerTestSample(tracerTestTcpRecord(EventTypeConnectionOpen, 3, "10.0.0.1:5000", "10.0.0.2:80", "0.0.0.0:0"))
	reader("tcp_retransmit_events").reads <- tracerTestSample(tracerTestTcpRecord(EventTypeTCPRetransmit, 4, "10.0.0.1:5000", "10.0.0.2:80", "0.0.0.0:0"))
	reader("file_events").reads <- tracerTestSample(tracerTestFileRecord(5, 3, 1, 1))
	reader("l7_events").reads <- tracerTestSample(tracerTestL7Record(6, 0, nil))
	events := tracerTestWaitEvents(t, ch, 6)
	for pid, typ := range map[uint32]EventType{1: EventTypeProcessStart, 2: EventTypeListenOpen, 3: EventTypeConnectionOpen,
		4: EventTypeTCPRetransmit, 5: EventTypeFileOpen, 6: EventTypeL7Request} {
		assert.Equal(t, typ, events[pid].Type, "pid %d", pid)
	}
	assert.LessOrEqual(t, reader("tcp_connect_events").maxDeadline(), 10*time.Millisecond)
	assert.Greater(t, reader("proc_events").maxDeadline(), 10*time.Millisecond)

	// every tracepoint and kprobe is linked; uprobes are only kept for later
	sort.Strings(env.tracepoints)
	sort.Strings(env.kprobes)
	assert.Equal(t, env.programs(ebpf.TracePoint, false), env.tracepoints)
	assert.Contains(t, env.tracepoints, "sched/sched_process_exit")
	assert.Equal(t, []string{"nf_ct_deliver_cached_events", "path_get"}, env.kprobes)
	assert.Equal(t, env.programs(ebpf.Kprobe, true), slices.Sorted(maps.Keys(tr.uprobes)))
	assert.Contains(t, tr.uprobes, "openssl_SSL_read_exit")
	assert.Len(t, tr.links, len(env.tracepoints)+len(env.kprobes))
	for i, l := range tr.links {
		assert.Same(t, env.links[i], l)
	}

	tr.Close()
	assert.Equal(t, tracerTestRepeat(1, len(env.links)), env.linksClosed())
	assert.Equal(t, tracerTestRepeat(1, len(env.readers)), env.readersClosed())
}

func TestTracerEbpfL7Disabled(t *testing.T) {
	env := tracerTestFakeEbpf(t, "6.8.0", false)
	tr := NewTracer(0, 0, true)
	require.NoError(t, tr.ebpf(make(chan Event)))
	assert.NotContains(t, tr.readers, "l7_events")
	assert.Len(t, tr.readers, 5)
	sort.Strings(env.tracepoints)
	assert.Equal(t, env.programs(ebpf.TracePoint, false, tracerTestL7Syscalls...), env.tracepoints)
	for _, name := range tracerTestL7Syscalls {
		assert.NotContains(t, env.tracepoints, "syscalls/"+name)
	}
	assert.Contains(t, env.tracepoints, "syscalls/sys_enter_connect")
	assert.Len(t, env.kprobes, 2)
	// TLS uprobe programs are still registered; attaching them is gated separately
	assert.Contains(t, tr.uprobes, "go_crypto_tls_write_enter")
	tr.Close()
}

func TestTracerEbpfVariantSelection(t *testing.T) {
	sentinel := errors.New("variant loaded")
	real := ebpfProgs[runtime.GOARCH]
	t.Cleanup(func() { ebpfProgs[runtime.GOARCH] = real })
	for _, c := range []struct {
		kernel  string
		padding bool
		version string
		flags   string
	}{
		{"6.8.0", true, "5.12", "ctx-extra-padding"},
		{"6.8.0", false, "5.12", ""},
		{"5.12.0", false, "5.12", ""},
		{"5.11.0", false, "5.6", ""},
		{"5.4.0", false, "4.20", ""},
		{"4.19.0", false, "4.16", ""},
		{"4.16.0", false, "4.16", ""},
	} {
		t.Run(fmt.Sprintf("%s %v", c.kernel, c.padding), func(t *testing.T) {
			// only the expected variant decodes; picking any other one fails with an encoding error
			var variants []struct {
				version string
				flags   string
				prog    []byte
			}
			for _, p := range real {
				if p.version != c.version || p.flags != c.flags {
					p.prog = []byte("!")
				}
				variants = append(variants, p)
			}
			ebpfProgs[runtime.GOARCH] = variants
			env := tracerTestFakeEbpf(t, c.kernel, c.padding)
			env.collErr = sentinel
			err := NewTracer(0, 0, false).ebpf(make(chan Event))
			assert.ErrorIs(t, err, sentinel)
			assert.Len(t, env.specs, 1)
		})
	}

	ebpfProgs[runtime.GOARCH] = real
	env := tracerTestFakeEbpf(t, "4.15.0", false)
	assert.EqualError(t, NewTracer(0, 0, false).ebpf(nil), "unsupported kernel version: 4.15.0 ")
	env = tracerTestFakeEbpf(t, "5.11.0", true)
	assert.EqualError(t, NewTracer(0, 0, false).ebpf(nil), "unsupported kernel version: 5.11.0 ctx-extra-padding")
	assert.Empty(t, env.specs)
}

func TestTracerEbpfLoadErrors(t *testing.T) {
	real := ebpfProgs[runtime.GOARCH]
	t.Cleanup(func() { ebpfProgs[runtime.GOARCH] = real })
	gz := func(data []byte) []byte {
		b := &bytes.Buffer{}
		w := gzip.NewWriter(b)
		_, _ = w.Write(data)
		require.NoError(t, w.Close())
		return b.Bytes()
	}
	b64 := func(data []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(data)) }
	for blob, msg := range map[string]string{
		"!":                                   "invalid program encoding",
		string(b64(gz([]byte("hello"))[:15])): "failed to ungzip program",
		string(b64(gz([]byte("not an ELF")))): "failed to load collection spec",
	} {
		ebpfProgs[runtime.GOARCH] = []struct {
			version string
			flags   string
			prog    []byte
		}{{"4.16", "", []byte(blob)}}
		env := tracerTestFakeEbpf(t, "6.8.0", false)
		err := NewTracer(0, 0, false).ebpf(nil)
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), msg+": "), err.Error())
		assert.Empty(t, env.specs)
	}

	ebpfProgs[runtime.GOARCH] = real
	env := tracerTestFakeEbpf(t, "6.8.0", false)
	env.collErr = fmt.Errorf("load program: %w", &ebpf.VerifierError{Cause: syscall.EACCES, Log: []string{"0: (b7) r0 = 0", "R1 invalid mem access"}})
	err := NewTracer(0, 0, false).ebpf(nil)
	assert.ErrorContains(t, err, "failed to load collection: load program: ")
	var vErr *ebpf.VerifierError
	assert.ErrorAs(t, err, &vErr)

	tracerTestFakeEbpf(t, "6.8.0", false)
	tracerTestTraceFs(t, filepath.Join(t.TempDir(), "missing"))
	assert.EqualError(t, NewTracer(0, 0, false).ebpf(nil), "kernel tracing is not available: debugfs or tracefs must be mounted")
}

func TestTracerEbpfAttachErrors(t *testing.T) {
	t.Run("perf reader", func(t *testing.T) {
		env := tracerTestFakeEbpf(t, "6.8.0", false)
		env.readerErr = func(n int) error {
			if n == 3 {
				return errors.New("boom")
			}
			return nil
		}
		assert.EqualError(t, NewTracer(0, 0, false).ebpf(make(chan Event)), "failed to create ebpf reader: boom")
		assert.Equal(t, []int{1, 1}, env.readersClosed())
		assert.Empty(t, env.links)
	})

	for _, name := range []string{"sys_enter_connect", "path_get"} {
		t.Run(name, func(t *testing.T) {
			env := tracerTestFakeEbpf(t, "6.8.0", false)
			env.linkErr = func(n string) error {
				if n == name {
					return errors.New("boom")
				}
				return nil
			}
			tr := NewTracer(0, 0, false)
			assert.EqualError(t, tr.ebpf(make(chan Event)), "failed to link program '"+name+"': boom")
			assert.Equal(t, tracerTestRepeat(1, len(env.links)), env.linksClosed(), "links attached before the failure are released")
			assert.Equal(t, tracerTestRepeat(1, 6), env.readersClosed())
		})
	}

	t.Run("nf_conntrack not in use", func(t *testing.T) {
		env := tracerTestFakeEbpf(t, "6.8.0", false)
		env.linkErr = func(n string) error {
			if n == "nf_ct_deliver_cached_events" {
				return errors.New("symbol not found")
			}
			return nil
		}
		tr := NewTracer(0, 0, false)
		require.NoError(t, tr.ebpf(make(chan Event)))
		assert.Len(t, tr.links, len(env.programs(ebpf.TracePoint, false))+1)
		tr.Close()
	})
}

func TestTracerDefaultSeams(t *testing.T) {
	// the defaults call straight into cilium/ebpf and go-conntrack
	_, err := newPerfReader(nil, 0, perf.ReaderOptions{})
	assert.EqualError(t, err, "perCPUBuffer must be larger than 0")
	if c, err := openConntrack(&conntrack.Config{}); err == nil {
		assert.NoError(t, c.Close())
	}
}

func TestTracerMapIterators(t *testing.T) {
	tr := NewTracer(0, 0, false)
	tr.collection = &ebpf.Collection{Maps: map[string]*ebpf.Map{"active_connections": {}, "nodejs_stats": {}, "python_stats": {}}}
	assert.NotNil(t, tr.ActiveConnectionsIterator())
	assert.NotNil(t, tr.NodejsStatsIterator())
	assert.NotNil(t, tr.PythonStatsIterator())
}

func tracerTestConntrackParam(t *testing.T, value string) string {
	p := filepath.Join(t.TempDir(), "nf_conntrack_events")
	if value != "" {
		require.NoError(t, os.WriteFile(p, []byte(value), 0o644))
	}
	prev := nfConntrackEventsParameterPath
	t.Cleanup(func() { nfConntrackEventsParameterPath = prev })
	nfConntrackEventsParameterPath = p
	return p
}

func TestEnsureConntrackEventsAreEnabled(t *testing.T) {
	p := tracerTestConntrackParam(t, "1\n")
	require.NoError(t, ensureConntrackEventsAreEnabled())
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "1\n", string(data), "already enabled: not rewritten")

	p = tracerTestConntrackParam(t, "0\n")
	require.NoError(t, ensureConntrackEventsAreEnabled())
	data, err = os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "1", string(data))

	tracerTestConntrackParam(t, "")
	assert.NoError(t, ensureConntrackEventsAreEnabled(), "nf_conntrack is not loaded")

	tracerTestConntrackParam(t, "yes")
	assert.Error(t, ensureConntrackEventsAreEnabled())

	if os.Geteuid() != 0 {
		p = tracerTestConntrackParam(t, "2")
		require.NoError(t, os.Chmod(p, 0o444))
		assert.ErrorIs(t, ensureConntrackEventsAreEnabled(), os.ErrPermission)
	}
}

func TestTracerRun(t *testing.T) {
	pid := uint32(os.Getpid())

	t.Run("conntrack events check fails", func(t *testing.T) {
		tracerTestConntrackParam(t, "garbage")
		env := tracerTestFakeEbpf(t, "6.8.0", false)
		assert.Error(t, NewTracer(0, 0, false).Run(make(chan Event)))
		assert.Empty(t, env.specs)
	})

	t.Run("ebpf fails", func(t *testing.T) {
		tracerTestConntrackParam(t, "1")
		tracerTestFakeEbpf(t, "4.15.0", false)
		assert.EqualError(t, NewTracer(0, 0, false).Run(make(chan Event)), "unsupported kernel version: 4.15.0 ")
	})

	t.Run("init fails", func(t *testing.T) {
		tracerTestConntrackParam(t, "1")
		env := tracerTestFakeEbpf(t, "6.8.0", false)
		initTestFakeConntrack(t, func(int) (*initTestConntrack, error) { return nil, errors.New("netlink: boom") })
		tr := NewTracer(0, 0, false)
		assert.EqualError(t, tr.Run(make(chan Event, 1<<16)), "netlink: boom")
		tr.Close()
		assert.Equal(t, tracerTestRepeat(1, len(env.readers)), env.readersClosed())
	})

	t.Run("ok", func(t *testing.T) {
		p := tracerTestConntrackParam(t, "0")
		env := tracerTestFakeEbpf(t, "6.8.0", false)
		initTestFakeConntrack(t, func(int) (*initTestConntrack, error) { return &initTestConntrack{}, nil })
		initTestFakeUpdateMap(t, nil)
		ch := make(chan Event, 1<<16)
		tr := NewTracer(0, 0, false)
		require.NoError(t, tr.Run(ch))
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Equal(t, "1", string(data))
		assert.Len(t, env.specs, 1)
		tr.Close()
		assert.Equal(t, tracerTestRepeat(1, len(env.links)), env.linksClosed())
		assert.Contains(t, initTestEvents(ch, pid), Event{Type: EventTypeProcessStart, Pid: pid})
	})
}
