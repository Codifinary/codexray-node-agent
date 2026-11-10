// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package diag

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestWatchdogThresholdRateLimitAndRetention(t *testing.T) {
	dir := t.TempDir()
	w := NewWatchdog(WatchdogConfig{Dir: dir, ThresholdRatio: 0.8, MinInterval: time.Minute, MaxDumps: 2})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	usage := uint64(700)
	w.readUsage = func() (uint64, uint64, bool) { return usage, 1000, true }

	require.False(t, w.check(), "below 80% of the limit")
	usage = 850
	require.True(t, w.check())
	require.False(t, w.check(), "rate limited")

	for i := 0; i < 3; i++ {
		now = now.Add(2 * time.Minute)
		require.True(t, w.check())
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "only MaxDumps bundles are kept")
	for _, f := range []string{"heap.pb.gz", "goroutine.txt", "memory.json"} {
		_, err := os.Stat(filepath.Join(dir, entries[1].Name(), f))
		require.NoError(t, err, f)
	}
}

func TestWatchdogAbsoluteThresholdWithoutLimit(t *testing.T) {
	w := NewWatchdog(WatchdogConfig{Dir: t.TempDir(), ThresholdBytes: 100, ThresholdRatio: 0.5})
	require.Equal(t, uint64(100), w.threshold(0), "no limit: absolute threshold only")
	require.Equal(t, uint64(100), w.threshold(1000), "the lower of the two applies")
	require.Equal(t, uint64(50), w.threshold(100))
	require.False(t, WatchdogConfig{Dir: "x"}.Enabled())
}

func TestPprofExposurePolicy(t *testing.T) {
	require.NoError(t, ValidatePprofListen("", ""))
	require.NoError(t, ValidatePprofListen("127.0.0.1:10302", ""))
	require.NoError(t, ValidatePprofListen("localhost:10302", ""))
	require.NoError(t, ValidatePprofListen("[::1]:10302", ""))
	require.Error(t, ValidatePprofListen("0.0.0.0:10302", ""))
	require.Error(t, ValidatePprofListen(":10302", ""))
	require.NoError(t, ValidatePprofListen(":10302", "secret"))
}

func TestPprofGuard(t *testing.T) {
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}), "s3cret", rate.NewLimiter(rate.Every(time.Hour), 1))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req := httptest.NewRequest("GET", "/debug/pprof/", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestCgroupMemoryV2(t *testing.T) {
	dir := t.TempDir()
	prev, prevProc := selfCgroupDir, procSelfCgroup
	selfCgroupDir, procSelfCgroup = dir, filepath.Join(dir, "no-such-file")
	defer func() { selfCgroupDir, procSelfCgroup = prev, prevProc }()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory.current"), []byte("1234\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory.max"), []byte("max\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("anon 1000\nfile 200\nsock 34\n"), 0o600))
	u, l, ok := CgroupMemory()
	require.True(t, ok)
	require.Equal(t, uint64(1234), u)
	require.Equal(t, uint64(0), l)
	require.Equal(t, uint64(34), CgroupMemoryStat()["sock"])
}

// Privileged/hostPID pods (the node-agent DaemonSet) have no private cgroup namespace:
// /sys/fs/cgroup is the host hierarchy, whose root has no memory.current, and the agent's
// cgroup is the path in /proc/self/cgroup. Observed on staging (containerd 2.2, k8s 1.30).
func TestCgroupMemoryV2HostHierarchy(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "kubepods.slice", "pod1.slice", "cri-containerd-abc.scope")
	require.NoError(t, os.MkdirAll(own, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "memory.stat"), []byte("anon 999999\n"), 0o600)) // host-wide, must be ignored
	require.NoError(t, os.WriteFile(filepath.Join(own, "memory.current"), []byte("4096\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(own, "memory.max"), []byte("5368709120\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(own, "memory.stat"), []byte("anon 3000\n"), 0o600))
	proc := filepath.Join(root, "self-cgroup")
	require.NoError(t, os.WriteFile(proc, []byte("0::/kubepods.slice/pod1.slice/cri-containerd-abc.scope\n"), 0o600))

	prev, prevProc := selfCgroupDir, procSelfCgroup
	selfCgroupDir, procSelfCgroup = root, proc
	defer func() { selfCgroupDir, procSelfCgroup = prev, prevProc }()

	u, l, ok := CgroupMemory()
	require.True(t, ok)
	require.Equal(t, uint64(4096), u)
	require.Equal(t, uint64(5368709120), l)
	require.Equal(t, uint64(3000), CgroupMemoryStat()["anon"])
}

func TestPprofDoesNotExposeCmdline(t *testing.T) {
	rec := httptest.NewRecorder()
	PprofMux().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/cmdline", nil))
	require.NotContains(t, rec.Body.String(), os.Args[0])
	rec = httptest.NewRecorder()
	PprofMux().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/heap?debug=1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}
