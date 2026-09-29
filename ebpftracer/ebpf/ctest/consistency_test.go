// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

package ctest

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/codifinary/codexray-node-agent/ebpftracer"
	"github.com/codifinary/codexray-node-agent/ebpftracer/l7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func defines(t *testing.T, src string) map[string]int {
	t.Helper()
	res := map[string]int{}
	for _, m := range regexp.MustCompile(`(?m)^#define\s+(\w+)\s+(-?\d+)\b`).FindAllStringSubmatch(src, -1) {
		v, err := strconv.Atoi(m[2])
		require.NoError(t, err)
		res[m[1]] = v
	}
	return res
}

// The shim copies constants from the kernel sources; this fails if they drift.
func TestShimConstantsMatchKernelSources(t *testing.T) {
	l7c := defines(t, readSource(t, "../l7/l7.c"))
	state := defines(t, readSource(t, "../tcp/state.c"))

	assert.Equal(t, map[string]int{
		"STATUS_UNKNOWN":             int(StatusUnknown),
		"STATUS_OK":                  int(StatusOK),
		"STATUS_FAILED":              int(StatusFailed),
		"METHOD_UNKNOWN":             MethodUnknown,
		"METHOD_PRODUCE":             MethodProduce,
		"METHOD_CONSUME":             MethodConsume,
		"METHOD_STATEMENT_PREPARE":   MethodStatementPrepare,
		"METHOD_STATEMENT_CLOSE":     MethodStatementClose,
		"METHOD_HTTP2_CLIENT_FRAMES": MethodHTTP2ClientFrames,
		"METHOD_HTTP2_SERVER_FRAMES": MethodHTTP2ServerFrames,
	}, map[string]int{
		"STATUS_UNKNOWN":             l7c["STATUS_UNKNOWN"],
		"STATUS_OK":                  l7c["STATUS_OK"],
		"STATUS_FAILED":              l7c["STATUS_FAILED"],
		"METHOD_UNKNOWN":             l7c["METHOD_UNKNOWN"],
		"METHOD_PRODUCE":             l7c["METHOD_PRODUCE"],
		"METHOD_CONSUME":             l7c["METHOD_CONSUME"],
		"METHOD_STATEMENT_PREPARE":   l7c["METHOD_STATEMENT_PREPARE"],
		"METHOD_STATEMENT_CLOSE":     l7c["METHOD_STATEMENT_CLOSE"],
		"METHOD_HTTP2_CLIENT_FRAMES": l7c["METHOD_HTTP2_CLIENT_FRAMES"],
		"METHOD_HTTP2_SERVER_FRAMES": l7c["METHOD_HTTP2_SERVER_FRAMES"],
	})
	assert.Equal(t, MaxPayloadSize, state["MAX_PAYLOAD_SIZE"])
	assert.Equal(t, MaxPayloadSize, ebpftracer.MaxPayloadSize, "Go and C payload limits differ")
}

// The shim re-implements a few macros; if the kernel versions change, the
// shim in host_shim.h must be updated to match.
func TestShimMacrosMatchKernelSources(t *testing.T) {
	l7c := readSource(t, "../l7/l7.c")
	assert.Contains(t, l7c, "size = MIN(size, MAX_PAYLOAD_SIZE-1);")
	assert.Contains(t, l7c, `asm volatile ("%0 &= %1" : "+r"(size) : "i"(MAX_PAYLOAD_SIZE-1));`)

	ebpf := readSource(t, "../ebpf.c")
	assert.Contains(t, ebpf, "if (bpf_probe_read(&dst, sizeof(dst), src) < 0) {")
	assert.Contains(t, ebpf, "#define MIN(a,b) (((a)<(b))?(a):(b))")
}

// Every protocol file l7.c pulls into the kernel program must also be
// compiled by this harness, so a new protocol cannot land untested.
func TestEveryClassifierFileIsCompiled(t *testing.T) {
	includes := func(src, prefix string) []string {
		var res []string
		for _, m := range regexp.MustCompile(`(?m)^#include "` + prefix + `(\w+\.c)"`).FindAllStringSubmatch(src, -1) {
			res = append(res, m[1])
		}
		sort.Strings(res)
		return res
	}
	kernel := includes(readSource(t, "../l7/l7.c"), "")
	harness := includes(readSource(t, "classifiers.go"), "l7/")
	require.NotEmpty(t, kernel)
	assert.Equal(t, kernel, harness)
}

// The kernel classifier decides what reaches the Go parser, so methods the Go
// parser supports but the classifier drops are never traced.
func TestHTTPClassifierAgreesWithGoParser(t *testing.T) {
	for _, m := range []string{"GET", "POST", "HEAD", "PUT", "DELETE", "CONNECT", "OPTIONS", "PATCH"} {
		payload := []byte(m + " /x HTTP/1.1\r\nHost: a\r\n\r\n")
		goMethod, _ := l7.ParseHttp(payload)
		assert.Equal(t, m, goMethod, m)
		assert.Equal(t, 1, IsHTTPRequest(payload), m)
	}
	for _, p := range []string{"HTTP/1.1 200 OK\r\n\r\n", "BREW /pot HTTP/1.1\r\n\r\n"} {
		goMethod, _ := l7.ParseHttp([]byte(p))
		assert.Equal(t, "", goMethod, p)
		assert.Equal(t, 0, IsHTTPRequest([]byte(p)), p)
	}
}

func TestHTTPClassifierAcceptsTrace(t *testing.T) {
	// BUG: is_http_request (http.c:3-33) has no TRACE case, while the Go parser
	// (ebpftracer/l7/http.go) parses TRACE, so TRACE requests never reach it — unskip when fixed
	t.Skip("BUG: kernel HTTP classifier drops TRACE requests that the Go parser supports")
	payload := []byte("TRACE /x HTTP/1.1\r\nHost: a\r\n\r\n")
	goMethod, _ := l7.ParseHttp(payload)
	assert.Equal(t, "TRACE", goMethod)
	assert.Equal(t, 1, IsHTTPRequest(payload))
}

func TestRedisClassifierAgreesWithGoParser(t *testing.T) {
	for _, p := range []string{
		"*1\r\n$4\r\nPING\r\n",
		"*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n",
		"*2\r\n$3\r\nGET\r\n$3\r\nfoo\r\n",
	} {
		cmd, _ := l7.ParseRedis([]byte(p))
		assert.NotEmpty(t, cmd, p)
		assert.Equal(t, 1, IsRedisQuery([]byte(p)), p)
	}
}
