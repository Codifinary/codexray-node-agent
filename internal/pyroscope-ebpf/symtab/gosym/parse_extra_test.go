package gosym

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRuntimeTextFromPclntab18(t *testing.T) {
	// too short
	require.Equal(t, uint64(0), ParseRuntimeTextFromPclntab18(make([]byte, 10)))

	// wrong magic
	buf := make([]byte, 64)
	binary.LittleEndian.PutUint32(buf[0:4], 0x12345678)
	require.Equal(t, uint64(0), ParseRuntimeTextFromPclntab18(buf))

	// go 1.18 magic
	buf18 := make([]byte, 64)
	binary.LittleEndian.PutUint32(buf18[0:4], 0xFFFFFFF0)
	binary.LittleEndian.PutUint64(buf18[24:32], 0xdeadbeef)
	require.Equal(t, uint64(0xdeadbeef), ParseRuntimeTextFromPclntab18(buf18))

	// go 1.20 magic
	buf20 := make([]byte, 64)
	binary.LittleEndian.PutUint32(buf20[0:4], 0xFFFFFFF1)
	binary.LittleEndian.PutUint64(buf20[24:32], 0xcafef00d)
	require.Equal(t, uint64(0xcafef00d), ParseRuntimeTextFromPclntab18(buf20))
}
