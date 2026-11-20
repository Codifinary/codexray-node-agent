package containers

import (
	"errors"
	"testing"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/logparser"
	"github.com/stretchr/testify/assert"
)

func TestJournaldSubscribeCarriesTheInitError(t *testing.T) {
	t.Cleanup(func() { journaldReader, journaldInitErr = nil, nil })
	cg := &cgroup.Cgroup{Id: "/system.slice/app.service"}
	journaldReader, journaldInitErr = nil, errors.New("systemd journal not found in /run/log/journal")

	err := JournaldSubscribe(cg, make(chan logparser.LogEntry))

	assert.ErrorIs(t, err, errJournaldNotInitialized)
	assert.Contains(t, err.Error(), "systemd journal not found in /run/log/journal")
	assert.Equal(t, journaldInitErr, JournaldInitError())

	journaldInitErr = nil
	assert.Equal(t, errJournaldNotInitialized, JournaldSubscribe(cg, make(chan logparser.LogEntry)), "never initialized has no reason to add")
}
