package python

import (
	"os"
	"testing"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
)

func TestGetPyPerfPidDataProcMapsMissing(t *testing.T) {
	// A pid that (almost certainly) doesn't exist must fail while opening
	// /proc/<pid>/maps.
	_, err := GetPyPerfPidData(log.NewNopLogger(), 999999999, false)
	require.Error(t, err)
}

func TestGetPyPerfPidDataNoPythonFound(t *testing.T) {
	// Using our own test process's pid is a real, readable /proc/<pid>/maps
	// file that (as a go test binary) contains no python mappings, so this
	// exercises the GetProcInfo failure branch inside GetPyPerfPidData.
	_, err := GetPyPerfPidData(log.NewNopLogger(), uint32(os.Getpid()), false)
	require.Error(t, err)
}
