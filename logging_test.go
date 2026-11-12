package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/klog/v2"
)

func TestConfigureLoggingWritesEachMessageOnce(t *testing.T) {
	tests := []struct {
		name string
		log  func(string)
	}{
		{name: "info", log: func(msg string) { klog.Infoln(msg) }},
		{name: "warning", log: func(msg string) { klog.Warningln(msg) }},
		{name: "error", log: func(msg string) { klog.Errorln(msg) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			configureLogging(&out)
			msg := "spool size exceeded " + test.name
			test.log(msg)
			klog.Flush()
			assert.Equal(t, 1, strings.Count(out.String(), msg), out.String())
		})
	}
}
