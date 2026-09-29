package python

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTempMem(t *testing.T, size int) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mem")
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	require.NoError(t, f.Truncate(int64(size)))
	return f
}

func TestGetAutoTLSKeyMissingSymbol(t *testing.T) {
	f := openTempMem(t, 16)
	_, err := getAutoTLSKey(1, Version{2, 7, 0}, 0, f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing symbols")
}

func TestGetAutoTLSKeySuccess(t *testing.T) {
	f := openTempMem(t, 64)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], 7)
	_, err := f.WriteAt(buf[:], 32)
	require.NoError(t, err)

	key, err := getAutoTLSKey(1, Version{2, 7, 0}, 32, f)
	require.NoError(t, err)
	assert.EqualValues(t, 7, key)
}

func TestGetAutoTLSKeyNotInitialized(t *testing.T) {
	f := openTempMem(t, 64)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], 0xFFFFFFFF) // -1
	_, err := f.WriteAt(buf[:], 8)
	require.NoError(t, err)

	_, err = getAutoTLSKey(1, Version{2, 7, 0}, 8, f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestGetAutoTLSKeyReadPastEOF(t *testing.T) {
	f := openTempMem(t, 4)
	_, err := getAutoTLSKey(1, Version{2, 7, 0}, 2, f)
	require.Error(t, err)
}

func validTssOffsets() *UserOffsets {
	return &UserOffsets{
		PyTssT_is_initialized:             0,
		PyTssT_key:                        4,
		PyTssTSize:                        8,
		PyRuntimeState_autoTSSkey:         16,
		PyRuntimeState_gilstate:           20,
		Gilstate_runtime_state_autoTSSkey: 8,
	}
}

func TestGetPyTssKeyBadOffsets(t *testing.T) {
	f := openTempMem(t, 64)
	bad := validTssOffsets()
	bad.PyTssT_key = 0
	_, err := getPyTssKey(1, Version{3, 11, 0}, bad, 100, f, &PerfLibc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected _Py_tss_t offsets")
}

func TestGetPyTssKeyMissingPyRuntime(t *testing.T) {
	f := openTempMem(t, 64)
	_, err := getPyTssKey(1, Version{3, 11, 0}, validTssOffsets(), 0, f, &PerfLibc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing symbols pyRuntime")
}

func TestGetPyTssKeyPy312MissingAutoTSSKey(t *testing.T) {
	f := openTempMem(t, 64)
	offsets := validTssOffsets()
	offsets.PyRuntimeState_autoTSSkey = -1
	_, err := getPyTssKey(1, Version{3, 12, 0}, offsets, 100, f, &PerfLibc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PyRuntimeState_autoTSSkey")
}

func TestGetPyTssKeyPy312Success(t *testing.T) {
	f := openTempMem(t, 256)
	offsets := validTssOffsets()
	var pyRuntime uint64 = 100
	pkey := int64(pyRuntime) + int64(offsets.PyRuntimeState_autoTSSkey)

	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], 1) // is_initialized
	binary.LittleEndian.PutUint32(buf[4:8], 5) // key
	_, err := f.WriteAt(buf[:], pkey)
	require.NoError(t, err)

	key, err := getPyTssKey(1, Version{3, 12, 0}, offsets, pyRuntime, f, &PerfLibc{})
	require.NoError(t, err)
	assert.EqualValues(t, 5, key)
}

func TestGetPyTssKeyPy312NotInitialized(t *testing.T) {
	f := openTempMem(t, 256)
	offsets := validTssOffsets()
	var pyRuntime uint64 = 100
	pkey := int64(pyRuntime) + int64(offsets.PyRuntimeState_autoTSSkey)

	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], 0) // is_initialized = 0
	binary.LittleEndian.PutUint32(buf[4:8], 5)
	_, err := f.WriteAt(buf[:], pkey)
	require.NoError(t, err)

	_, err = getPyTssKey(1, Version{3, 12, 0}, offsets, pyRuntime, f, &PerfLibc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestGetPyTssKeyPreGilstateMissingOffsets(t *testing.T) {
	f := openTempMem(t, 64)
	offsets := validTssOffsets()
	offsets.PyRuntimeState_gilstate = -1
	_, err := getPyTssKey(1, Version{3, 10, 0}, offsets, 100, f, &PerfLibc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GilstateRuntimeStateAutoTSSkey")
}

func TestGetPyTssKeyPreGilstateSuccess(t *testing.T) {
	f := openTempMem(t, 256)
	offsets := validTssOffsets()
	var pyRuntime uint64 = 100
	pkey := int64(pyRuntime) + int64(offsets.PyRuntimeState_gilstate+offsets.Gilstate_runtime_state_autoTSSkey)

	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	binary.LittleEndian.PutUint32(buf[4:8], 9)
	_, err := f.WriteAt(buf[:], pkey)
	require.NoError(t, err)

	key, err := getPyTssKey(1, Version{3, 10, 0}, offsets, pyRuntime, f, &PerfLibc{Musl: false})
	require.NoError(t, err)
	assert.EqualValues(t, 9, key)
}

func TestGetPyTssKeyPreGilstateMuslAdjustment(t *testing.T) {
	// On amd64, mutexSizeGlibc == mutexSizeMusl == 40, so the musl adjustment
	// is a no-op here; this still exercises the musl code branch.
	f := openTempMem(t, 256)
	offsets := validTssOffsets()
	var pyRuntime uint64 = 100
	pkey := int64(pyRuntime) + int64(offsets.PyRuntimeState_gilstate+offsets.Gilstate_runtime_state_autoTSSkey)
	pkey -= 2 * (mutexSizeGlibc - mutexSizeMusl)

	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	binary.LittleEndian.PutUint32(buf[4:8], 3)
	_, err := f.WriteAt(buf[:], pkey)
	require.NoError(t, err)

	key, err := getPyTssKey(1, Version{3, 10, 0}, offsets, pyRuntime, f, &PerfLibc{Musl: true})
	require.NoError(t, err)
	assert.EqualValues(t, 3, key)
}

func TestGetPyTssKeyReadPastEOF(t *testing.T) {
	f := openTempMem(t, 4)
	offsets := validTssOffsets()
	_, err := getPyTssKey(1, Version{3, 12, 0}, offsets, 100, f, &PerfLibc{})
	require.Error(t, err)
}

func TestGetTSSKeyDispatch(t *testing.T) {
	// GetTSSKey opens /proc/<pid>/mem itself; using a pid that (almost
	// certainly) doesn't exist exercises the os.Open error path for both
	// the pre-3.7 and post-3.7 dispatch branches.
	_, err := GetTSSKey(999999999, Version{2, 7, 0}, validTssOffsets(), 1, 1, &PerfLibc{})
	require.Error(t, err)

	_, err = GetTSSKey(999999999, Version{3, 11, 0}, validTssOffsets(), 1, 1, &PerfLibc{})
	require.Error(t, err)
}
