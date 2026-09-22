package dogstatsd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomSpoolPutReplayRemoveAndQuota(t *testing.T) {
	spool, err := OpenCustomSpool(t.TempDir(), 6)
	if err != nil {
		t.Fatal(err)
	}
	spool.now = func() time.Time { return time.Unix(1, 0) }
	first, err := spool.Put([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	spool.now = func() time.Time { return time.Unix(2, 0) }
	second, err := spool.Put([]byte("def"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Put([]byte("g")); !errors.Is(err, ErrCustomSpoolFull) {
		t.Fatalf("quota error=%v", err)
	}
	oldest, err := spool.Oldest()
	if err != nil || oldest != first {
		t.Fatalf("oldest=(%q,%v), want %q", oldest, err, first)
	}
	stats, err := spool.Stats()
	if err != nil || stats.Files != 2 || stats.Bytes != 6 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if err := spool.Remove(first); err != nil {
		t.Fatal(err)
	}
	oldest, err = spool.Oldest()
	if err != nil || oldest != second {
		t.Fatalf("oldest after remove=(%q,%v), want %q", oldest, err, second)
	}
}

func TestCustomSpoolPutBatchIsCapacityCheckedTogether(t *testing.T) {
	spool, err := OpenCustomSpool(t.TempDir(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.PutBatch([][]byte{[]byte("abc"), []byte("def")}); !errors.Is(err, ErrCustomSpoolFull) {
		t.Fatalf("batch quota error=%v", err)
	}
	stats, err := spool.Stats()
	if err != nil || stats.Files != 0 || stats.Bytes != 0 {
		t.Fatalf("partial batch persisted: stats=%+v err=%v", stats, err)
	}
	files, err := spool.PutBatch([][]byte{[]byte("ab"), []byte("cd")})
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}

func TestCustomSpoolQuarantineAndPathIsolation(t *testing.T) {
	root := t.TempDir()
	spool, err := OpenCustomSpool(filepath.Join(root, "custom"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	file, err := spool.Put([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	quarantined, err := spool.Quarantine(file, "HTTP 401")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(quarantined, string(filepath.Separator)+"quarantine"+string(filepath.Separator)) {
		t.Fatalf("quarantine path=%q", quarantined)
	}
	if _, err := os.Stat(quarantined); err != nil {
		t.Fatal(err)
	}
	nodeFile := filepath.Join(root, "node-spool.ready")
	if err := os.WriteFile(nodeFile, []byte("node"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := spool.Remove(nodeFile); err == nil {
		t.Fatal("expected cross-spool removal to be rejected")
	}
	if got, err := os.ReadFile(nodeFile); err != nil || string(got) != "node" {
		t.Fatalf("node file changed: %q %v", got, err)
	}
}

func TestCustomSpoolRejectsInvalidConfigurationAndPayload(t *testing.T) {
	if _, err := OpenCustomSpool("", 1); err == nil {
		t.Fatal("expected empty directory error")
	}
	if _, err := OpenCustomSpool(t.TempDir(), 0); err == nil {
		t.Fatal("expected quota error")
	}
	spool, err := OpenCustomSpool(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Put(nil); err == nil {
		t.Fatal("expected empty payload error")
	}
}

func TestCustomSpoolStartupRecovery(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, ".batch-orphan.tmp")
	if err := os.Mkdir(orphan, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "batch-00000000000000000001-corrupt.ready")
	if err := os.Mkdir(corrupt, 0750); err != nil {
		t.Fatal(err)
	}
	spool, err := OpenCustomSpool(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan temp batch remains: %v", err)
	}
	quarantined := filepath.Join(spool.quarantine, filepath.Base(corrupt)+".corrupt")
	if info, err := os.Stat(quarantined); err != nil || !info.IsDir() {
		t.Fatalf("corrupt batch was not quarantined: info=%v err=%v", info, err)
	}
	if file, err := spool.Oldest(); err != nil || file != "" {
		t.Fatalf("startup replay=(%q,%v), want empty", file, err)
	}
}

func TestCustomSpoolMaintenanceBoundsAgeAndQuarantine(t *testing.T) {
	spool, err := OpenCustomSpoolWithPolicy(t.TempDir(), 1024, time.Hour, 5)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	spool.now = func() time.Time { return now }
	ready, err := spool.Put([]byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(ready, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool.quarantine, "first.failed"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(spool.quarantine, "first.failed"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool.quarantine, "second.failed"), []byte("5678"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := spool.Maintain()
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpiredReadyFiles != 1 || result.RemovedQuarantine != 1 {
		t.Fatalf("maintenance=%+v", result)
	}
	if file, err := spool.Oldest(); err != nil || file != "" {
		t.Fatalf("expired replay file remains: %q %v", file, err)
	}
	if _, err := os.Stat(filepath.Join(spool.quarantine, "first.failed")); !os.IsNotExist(err) {
		t.Fatalf("oldest quarantine entry remains: %v", err)
	}
}
