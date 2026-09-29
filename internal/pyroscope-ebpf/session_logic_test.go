//go:build linux

package ebpfspy

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/pyroscope/ebpf/cpp/demangle"
	"github.com/grafana/pyroscope/ebpf/pyrobpf"
	"github.com/grafana/pyroscope/ebpf/sd"
	"github.com/grafana/pyroscope/ebpf/symtab"
	"github.com/stretchr/testify/require"
)

// fakeSymbolTable is a minimal symtab.SymbolTable used to exercise WalkStack
// without needing a real process or elf binary.
type fakeSymbolTable struct {
	syms map[uint64]symtab.Symbol
}

func (f *fakeSymbolTable) Refresh() {}
func (f *fakeSymbolTable) Cleanup() {}
func (f *fakeSymbolTable) Resolve(addr uint64) symtab.Symbol {
	if sym, ok := f.syms[addr]; ok {
		return sym
	}
	return symtab.Symbol{}
}

func newSessionForTest() *session {
	return &session{
		logger: log.NewNopLogger(),
		pids: pids{
			unknown: make(map[uint32]struct{}),
			dead:    make(map[uint32]struct{}),
			all:     make(map[uint32]procInfoLite),
		},
	}
}

func stackBytesFor(ips ...uint64) []byte {
	buf := make([]byte, 127*8)
	for i, ip := range ips {
		binary.LittleEndian.PutUint64(buf[i*8:i*8+8], ip)
	}
	return buf
}

func TestUint8FromBool(t *testing.T) {
	require.Equal(t, uint8(1), uint8FromBool(true))
	require.Equal(t, uint8(0), uint8FromBool(false))
}

func TestBoolToU8(t *testing.T) {
	require.Equal(t, uint8(1), boolToU8(true))
	require.Equal(t, uint8(0), boolToU8(false))
}

func TestStackBuilder(t *testing.T) {
	sb := &stackBuilder{}
	sb.append("a")
	sb.append("b")
	require.Equal(t, []string{"a", "b"}, sb.stack)
	sb.reset()
	require.Empty(t, sb.stack)
	// reset should retain capacity but reuse the backing array
	sb.append("c")
	require.Equal(t, []string{"c"}, sb.stack)
}

func TestStackResolveStatsAdd(t *testing.T) {
	s := StackResolveStats{known: 1, unknownSymbols: 2, unknownModules: 3}
	s.add(StackResolveStats{known: 10, unknownSymbols: 20, unknownModules: 30})
	require.Equal(t, StackResolveStats{known: 11, unknownSymbols: 22, unknownModules: 33}, s)
}

func TestSessionComm(t *testing.T) {
	s := newSessionForTest()
	require.Equal(t, "pid_unknown", s.comm(42))

	s.pids.all[42] = procInfoLite{pid: 42, comm: "myproc"}
	require.Equal(t, "myproc", s.comm(42))

	// explicit empty comm falls back to pid_unknown too
	s.pids.all[43] = procInfoLite{pid: 43, comm: ""}
	require.Equal(t, "pid_unknown", s.comm(43))
}

func TestSaveUnknownPIDLocked(t *testing.T) {
	s := newSessionForTest()
	s.saveUnknownPIDLocked(7)
	_, ok := s.pids.unknown[7]
	require.True(t, ok)
}

func TestSessionGetStackNegativeID(t *testing.T) {
	s := newSessionForTest()
	require.Nil(t, s.GetStack(-1))
}

func TestSessionGetPythonStackNilMap(t *testing.T) {
	s := newSessionForTest()
	// PythonStacks map is nil (zero value), function must not panic
	require.Nil(t, s.GetPythonStack(5))
}

func TestWalkStack_KnownAndUnknownSymbols(t *testing.T) {
	resolver := &fakeSymbolTable{syms: map[uint64]symtab.Symbol{
		0x1000: {Name: "known_func", Module: "libfoo.so", Start: 0x1000},
		0x2000: {Module: "libbar.so", Start: 0x2000}, // known module, unknown symbol
	}}

	t.Run("known symbol resolves to its name", func(t *testing.T) {
		s := newSessionForTest()
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		s.WalkStack(sb, stackBytesFor(0x1000), resolver, stats)
		require.Equal(t, []string{"known_func"}, sb.stack)
		require.Equal(t, uint32(1), stats.known)
	})

	t.Run("unknown symbol with known module falls back to module name", func(t *testing.T) {
		s := newSessionForTest()
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		s.WalkStack(sb, stackBytesFor(0x2000), resolver, stats)
		require.Equal(t, []string{"libbar.so"}, sb.stack)
		require.Equal(t, uint32(1), stats.unknownSymbols)
	})

	t.Run("unknown module uses [unknown] placeholder by default", func(t *testing.T) {
		s := newSessionForTest()
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		s.WalkStack(sb, stackBytesFor(0x3000), resolver, stats)
		require.Equal(t, []string{"[unknown]"}, sb.stack)
		require.Equal(t, uint32(1), stats.unknownModules)
	})

	t.Run("UnknownSymbolAddress option prints hex address", func(t *testing.T) {
		s := newSessionForTest()
		s.options.UnknownSymbolAddress = true
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		s.WalkStack(sb, stackBytesFor(0x3000), resolver, stats)
		require.Equal(t, []string{"3000"}, sb.stack)
	})

	t.Run("UnknownSymbolModuleOffset option prints module+offset", func(t *testing.T) {
		s := newSessionForTest()
		s.options.UnknownSymbolModuleOffset = true
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		s.WalkStack(sb, stackBytesFor(0x2000), resolver, stats)
		require.Equal(t, []string{"libbar.so+2000"}, sb.stack)
	})

	t.Run("empty stack does nothing", func(t *testing.T) {
		s := newSessionForTest()
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		s.WalkStack(sb, nil, resolver, stats)
		require.Empty(t, sb.stack)
	})

	t.Run("multiple frames get reversed so root is first", func(t *testing.T) {
		s := newSessionForTest()
		sb := &stackBuilder{}
		stats := &StackResolveStats{}
		// stack order (leaf first, as delivered by ebpf) is 0x1000 then 0x2000
		s.WalkStack(sb, stackBytesFor(0x1000, 0x2000), resolver, stats)
		// after reversal within this call, root (0x2000) should come before leaf (0x1000)
		require.Equal(t, []string{"libbar.so", "known_func"}, sb.stack)
	})
}

func TestProcErrLogger(t *testing.T) {
	s := newSessionForTest()
	// should not panic for either branch; behavior differs based on error type
	l1 := s.procErrLogger(os.ErrNotExist)
	require.NotNil(t, l1)
	l2 := s.procErrLogger(errors.New("boom"))
	require.NotNil(t, l2)
}

func TestProcAliveLogger(t *testing.T) {
	s := newSessionForTest()
	require.NotNil(t, s.procAliveLogger(true))
	require.NotNil(t, s.procAliveLogger(false))
}

func TestCollectKernelEnabled(t *testing.T) {
	s := newSessionForTest()
	s.options.CollectKernel = false
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{"service_name": "svc"})
	require.False(t, s.collectKernelEnabled(target))

	s.options.CollectKernel = true
	require.True(t, s.collectKernelEnabled(target))

	targetOverride := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{
		"service_name":         "svc",
		sd.OptionCollectKernel: "false",
	})
	require.False(t, s.collectKernelEnabled(targetOverride))
}

func TestPythonEnabled(t *testing.T) {
	s := newSessionForTest()
	s.options.PythonEnabled = false
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{"service_name": "svc"})
	require.False(t, s.pythonEnabled(target))

	targetOverride := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{
		"service_name":         "svc",
		sd.OptionPythonEnabled: "true",
	})
	require.True(t, s.pythonEnabled(targetOverride))
}

func TestPythonBPFDebugLogEnabled(t *testing.T) {
	s := newSessionForTest()
	s.options.PythonBPFDebugLogEnabled = false
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{
		"service_name":                    "svc",
		sd.OptionPythonBPFDebugLogEnabled: "true",
	})
	require.True(t, s.pythonBPFDebugLogEnabled(target))
}

func TestPythonBPFErrorLogEnabled(t *testing.T) {
	s := newSessionForTest()
	s.options.PythonBPFErrorLogEnabled = true
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{
		"service_name":                    "svc",
		sd.OptionPythonBPFErrorLogEnabled: "false",
	})
	require.False(t, s.pythonBPFErrorLogEnabled(target))
}

func TestOverrideSymbolOptions(t *testing.T) {
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{
		"service_name":              "svc",
		sd.OptionGoTableFallback:    "true",
		sd.OptionPythonFullFilePath: "true",
		sd.OptionDemangle:           "none",
	})
	opt := &symtab.SymbolOptions{}
	overrideSymbolOptions(target, opt)
	require.True(t, opt.GoTableFallback)
	require.True(t, opt.PythonFullFilePath)
	require.Equal(t, demangle.ConvertDemangleOptions("none"), opt.DemangleOptions)
}

func TestOverrideSymbolOptions_NoFlags(t *testing.T) {
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{"service_name": "svc"})
	opt := &symtab.SymbolOptions{GoTableFallback: true, PythonFullFilePath: true}
	overrideSymbolOptions(target, opt)
	// unset flags leave existing values untouched
	require.True(t, opt.GoTableFallback)
	require.True(t, opt.PythonFullFilePath)
}

func TestTargetSymbolOptions(t *testing.T) {
	s := newSessionForTest()
	s.options.SymbolOptions = symtab.SymbolOptions{GoTableFallback: true}
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{"service_name": "svc"})
	opt := s.targetSymbolOptions(target)
	require.True(t, opt.GoTableFallback)
	// returned pointer must be a copy, not aliasing session options
	opt.GoTableFallback = false
	require.True(t, s.options.SymbolOptions.GoTableFallback)
}

func TestProgOptions(t *testing.T) {
	s := newSessionForTest()
	s.options.VerifierLogSize = 0
	opts := s.progOptions()
	require.True(t, opts.LogDisabled)

	s.options.VerifierLogSize = 1024
	opts = s.progOptions()
	require.False(t, opts.LogDisabled)
	require.Equal(t, uint32(1024), opts.LogSizeStart)
}

func TestSelectProfilingType_ProcessNotFound(t *testing.T) {
	s := newSessionForTest()
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{"service_name": "svc"})
	// pid 999999999 should not exist
	pi := s.selectProfilingType(999999999, target)
	require.Equal(t, pyrobpf.ProfilingTypeError, pi.typ)
}

func TestSelectProfilingType_CurrentProcess(t *testing.T) {
	s := newSessionForTest()
	s.options.PythonEnabled = false
	target := sd.NewTargetForTesting("cid", 1, sd.DiscoveryTarget{"service_name": "svc"})
	pid := uint32(os.Getpid())
	pi := s.selectProfilingType(pid, target)
	// go test binary isn't "python*" nor "uwsgi", should be framepointers (2)
	require.Equal(t, uint8(2), uint8(pi.typ))
	require.NotEmpty(t, pi.comm)
}

func TestGetPIDNamespace(t *testing.T) {
	dev, ino, err := getPIDNamespace()
	require.NoError(t, err)
	require.NotZero(t, ino)
	_ = dev
}

func TestProcessAlive(t *testing.T) {
	require.True(t, processAlive(uint32(os.Getpid())))
	require.False(t, processAlive(999999999))
}

func TestSkipPythonFrame(t *testing.T) {
	require.True(t, skipPythonFrame("", "__init__", "__init__"))
	require.False(t, skipPythonFrame("SomeClass", "__init__", "__init__"))
	require.False(t, skipPythonFrame("", "other.py", "__init__"))
	require.False(t, skipPythonFrame("", "__init__", "other"))
}

func TestLogVerifierErrorDisabledWhenNoLogSize(t *testing.T) {
	s := newSessionForTest()
	s.options.VerifierLogSize = 0
	// should be a no-op and not panic even with a plain error
	s.logVerifierError(errors.New("some error"))
}
