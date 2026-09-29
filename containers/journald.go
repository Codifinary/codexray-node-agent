// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"fmt"

	"github.com/codifinary/codexray-node-agent/cgroup"
	"github.com/codifinary/codexray-node-agent/logs"
	"github.com/codifinary/logparser"
)

type journaldSubscriber interface {
	Subscribe(cgroup string, ch chan<- logparser.LogEntry) error
	Unsubscribe(cgroup string)
}

var (
	journaldReader    journaldSubscriber
	newJournaldReader = logs.NewJournaldReader
)

func JournaldInit() error {
	r, err := newJournaldReader(
		hostPath("/run/log/journal"),
		hostPath("/var/log/journal"),
	)
	if err != nil {
		return err
	}
	journaldReader = r
	return nil
}

func JournaldSubscribe(cg *cgroup.Cgroup, ch chan<- logparser.LogEntry) error {
	if journaldReader == nil {
		return fmt.Errorf("journald reader not initialized")
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
