package python

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStackStatusString(t *testing.T) {
	testdata := []struct {
		status StackStatus
		want   string
	}{
		{StackStatusComplete, "StackStatusComplete"},
		{StackStatusError, "StackStatusError"},
		{StackStatusTruncated, "StackStatusTruncated"},
		{StackStatus(42), "StackStatus(42)"},
	}
	for _, td := range testdata {
		t.Run(td.want, func(t *testing.T) {
			assert.Equal(t, td.want, td.status.String())
		})
	}
}

func TestPyErrorString(t *testing.T) {
	testdata := []struct {
		err  PyError
		want string
	}{
		{PyErrorGeneric, "PyErrorGeneric"},
		{PyErrorThreadState, "PyErrorThreadState"},
		{PyErrorThreadStateNull, "PyErrorThreadStateNull"},
		{PyErrorTopFrame, "PyErrorTopFrame"},
		{PyErrorFrameCode, "PyErrorFrameCode"},
		{PyErrorFramePrev, "PyErrorFramePrev"},
		{PyErrorSymbol, "PyErrorSymbol"},
		{PyErrorTlsbase, "PyErrorTlsbase"},
		{PyErrorFirstArg, "PyErrorFirstArg"},
		{PyErrorClassName, "PyErrorClassName"},
		{PyErrorFileName, "PyErrorFileName"},
		{PyErrorName, "PyErrorName"},
		{PyError(99), "PyError(99)"},
	}
	for _, td := range testdata {
		t.Run(td.want, func(t *testing.T) {
			assert.Equal(t, td.want, td.err.String())
		})
	}
}
