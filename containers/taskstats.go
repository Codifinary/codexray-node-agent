// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"fmt"
	"sync"

	"github.com/mdlayher/taskstats"
)

type taskstatsSource interface {
	TGID(pid int) (*taskstats.Stats, error)
	PID(pid int) (*taskstats.Stats, error)
}

var (
	taskstatsClient    taskstatsSource
	taskstatsLock      sync.Mutex
	newTaskstatsClient = taskstats.New
)

func TaskstatsInit() error {
	c, err := newTaskstatsClient()
	if err != nil {
		return err
	}
	taskstatsClient = c
	return nil
}

func TaskstatsTGID(pid uint32) (*taskstats.Stats, error) {
	if taskstatsClient == nil {
		return nil, fmt.Errorf("taskstats client not initialized")
	}
	taskstatsLock.Lock()
	defer taskstatsLock.Unlock()
	s, err := taskstatsClient.TGID(int(pid))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func TaskstatsPID(pid uint32) (*taskstats.Stats, error) {
	if taskstatsClient == nil {
		return nil, fmt.Errorf("taskstats client not initialized")
	}
	taskstatsLock.Lock()
	defer taskstatsLock.Unlock()
	s, err := taskstatsClient.PID(int(pid))
	if err != nil {
		return nil, err
	}
	return s, nil
}
