// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"errors"
	"fmt"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/logs"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/codifinary/logparser"
)

var (
	journaldReader  *logs.JournaldReader
	journaldInitErr error

	errJournaldNotInitialized = errors.New("journald reader not initialized")
)

func JournaldInit() error {
	r, err := logs.NewJournaldReader(
		proc.HostPath("/run/log/journal"),
		proc.HostPath("/var/log/journal"),
	)
	if err != nil {
		journaldInitErr = err
		return err
	}
	journaldReader = r
	return nil
}

// JournaldInitError is why logs of systemd services are not collected, or nil when the journal is read.
func JournaldInitError() error {
	return journaldInitErr
}

func JournaldSubscribe(cg *cgroup.Cgroup, ch chan<- logparser.LogEntry) error {
	if journaldReader == nil {
		if journaldInitErr != nil {
			return fmt.Errorf("%w: %w", errJournaldNotInitialized, journaldInitErr)
		}
		return errJournaldNotInitialized
	}
	err := journaldReader.Subscribe(cg.Id, ch)
	if err != nil {
		return err
	}
	return nil
}

func JournaldUnsubscribe(cg *cgroup.Cgroup) {
	if journaldReader == nil {
		return
	}
	journaldReader.Unsubscribe(cg.Id)
}
