package containers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNodejsDetectionMatchesTheFileNameOnly(t *testing.T) {
	tests := []struct {
		exe  string
		want string
	}{
		{exe: "/usr/bin/node", want: "nodejs"},
		{exe: "/usr/local/bin/node18", want: "nodejs"},
		{exe: "/usr/local/bin/node18.1", want: "nodejs"},
		{exe: "/usr/bin/nodejs", want: "nodejs"},
		{exe: "node", want: "nodejs"},
		{exe: "/usr/bin/calico-node", want: ""},
		{exe: "/opt/app/my-node", want: ""},
		{exe: "/usr/bin/python3.11", want: "python"},
	}
	for _, tt := range tests {
		t.Run(tt.exe, func(t *testing.T) {
			assert.Equal(t, tt.want, guessApplicationTypeByExe(tt.exe))
		})
	}
}
