package gosym

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemPCLNDataReadAt(t *testing.T) {
	m := MemPCLNData{Data: []byte("0123456789")}
	buf := make([]byte, 4)
	err := m.ReadAt(buf, 3)
	require.NoError(t, err)
	require.Equal(t, "3456", string(buf))
}

func TestFilePCLNDataReadAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	require.NoError(t, os.WriteFile(path, []byte("abcdefghijklmnop"), 0o644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	// offset is added to every ReadAt call, emulating reading a section that
	// starts partway through the file.
	pcln := NewFilePCLNData(f, 4)
	buf := make([]byte, 4)
	err = pcln.ReadAt(buf, 0)
	require.NoError(t, err)
	require.Equal(t, "efgh", string(buf))

	err = pcln.ReadAt(buf, 4)
	require.NoError(t, err)
	require.Equal(t, "ijkl", string(buf))
}

func TestFilePCLNDataReadAtShortRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	require.NoError(t, os.WriteFile(path, []byte("short"), 0o644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	pcln := NewFilePCLNData(f, 0)
	buf := make([]byte, 100)
	err = pcln.ReadAt(buf, 0)
	require.Error(t, err)
}
