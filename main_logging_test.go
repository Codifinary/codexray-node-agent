// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
)

type lineCounter struct {
	mu    sync.Mutex
	lines []string
}

func (c *lineCounter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, string(p))
	return len(p), nil
}

func (c *lineCounter) count(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, l := range c.lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// Each message must be written once, whatever its severity (klog by default fans an error out
// to the error, warning and info outputs, which all point at the same writer here).
func TestSetupLoggingWritesEachMessageOnce(t *testing.T) {
	w := &lineCounter{}
	setupLogging(w)
	klog.Errorln("flagtest-error")
	klog.Warningln("flagtest-warning")
	klog.Infoln("flagtest-info")
	klog.Flush()
	require.Equal(t, 1, w.count("flagtest-error"))
	require.Equal(t, 1, w.count("flagtest-warning"))
	require.Equal(t, 1, w.count("flagtest-info"))
}
