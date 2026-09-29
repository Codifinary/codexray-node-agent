package cpuonline

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCPURange(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected []uint
		wantErr  bool
	}{
		{name: "single range", input: "0-3", expected: []uint{0, 1, 2, 3}},
		{name: "single cpu", input: "0", expected: []uint{0}},
		{name: "list of singles", input: "0,2,3", expected: []uint{0, 2, 3}},
		{name: "mixed ranges and singles", input: "0-1,3,5-6", expected: []uint{0, 1, 3, 5, 6}},
		{name: "trailing newline", input: "0-3\n", expected: []uint{0, 1, 2, 3}},
		{name: "surrounded by spaces", input: "  0-1 \n", expected: []uint{0, 1}},
		{name: "invalid first", input: "x-3", wantErr: true},
		{name: "invalid last", input: "0-y", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cpus, err := ReadCPURange(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, cpus)
		})
	}
}

func TestGetRealSystem(t *testing.T) {
	if _, err := os.Stat("/sys/devices/system/cpu/online"); err != nil {
		t.Skipf("cannot access /sys/devices/system/cpu/online in this environment: %v", err)
	}
	cpus, err := Get()
	require.NoError(t, err)
	require.NotEmpty(t, cpus)
	t.Logf("online cpus: %v", cpus)
}
