// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package dogstatsd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrCustomSpoolFull = errors.New("custom metric spool capacity exceeded")

type SpoolStats struct {
	Files     int
	Bytes     int64
	OldestAge time.Duration
}

// CustomSpool is intentionally independent from the node-telemetry spool. It
// never scans, removes, or renames files outside dir and its quarantine child.
type CustomSpool struct {
	mu                 sync.Mutex
	dir                string
	quarantine         string
	maxBytes           int64
	maxAge             time.Duration
	maxQuarantineBytes int64
	now                func() time.Time
}

func OpenCustomSpool(dir string, maxBytes int64) (*CustomSpool, error) {
	quarantineLimit := maxBytes / 10
	if quarantineLimit < 1 {
		quarantineLimit = 1
	}
	return OpenCustomSpoolWithPolicy(dir, maxBytes, 24*time.Hour, quarantineLimit)
}

func OpenCustomSpoolWithPolicy(dir string, maxBytes int64, maxAge time.Duration, maxQuarantineBytes int64) (*CustomSpool, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("custom metric spool directory must not be empty")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("custom metric spool max bytes must be positive")
	}
	if maxAge <= 0 || maxQuarantineBytes <= 0 {
		return nil, fmt.Errorf("custom metric spool retention values must be positive")
	}
	clean := filepath.Clean(dir)
	quarantine := filepath.Join(clean, "quarantine")
	if err := os.MkdirAll(quarantine, 0750); err != nil {
		return nil, err
	}
	spool := &CustomSpool{dir: clean, quarantine: quarantine, maxBytes: maxBytes, maxAge: maxAge, maxQuarantineBytes: maxQuarantineBytes, now: time.Now}
	if err := spool.recoverStartup(); err != nil {
		return nil, err
	}
	return spool, nil
}

type MaintenanceResult struct {
	ExpiredReadyFiles int
	RemovedQuarantine int
}

// Maintain applies age retention to replayable data and a separate byte quota
// to quarantine. It only removes validated descendants of the custom spool.
func (s *CustomSpool) Maintain() (MaintenanceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result MaintenanceResult
	files, err := s.filesLocked()
	if err != nil {
		return result, err
	}
	cutoff := s.now().Add(-s.maxAge)
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return result, err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(file); err != nil {
				return result, err
			}
			result.ExpiredReadyFiles++
			parent := filepath.Dir(file)
			if parent != s.dir {
				if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
					_ = os.Remove(parent)
				}
			}
		}
	}
	entries, err := os.ReadDir(s.quarantine)
	if err != nil {
		return result, err
	}
	type quarantineEntry struct {
		path string
		size int64
		mod  time.Time
	}
	items := make([]quarantineEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		path := filepath.Join(s.quarantine, entry.Name())
		size, err := treeSize(path)
		if err != nil {
			return result, err
		}
		info, err := entry.Info()
		if err != nil {
			return result, err
		}
		items = append(items, quarantineEntry{path: path, size: size, mod: info.ModTime()})
		total += size
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, item := range items {
		if total <= s.maxQuarantineBytes {
			break
		}
		if err := os.RemoveAll(item.path); err != nil {
			return result, err
		}
		total -= item.size
		result.RemovedQuarantine++
	}
	return result, nil
}

func treeSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Put atomically persists one already-encoded remote-write payload. Existing
// files are retained when capacity is exhausted; the new payload is rejected.
func (s *CustomSpool) Put(payload []byte) (string, error) {
	files, err := s.PutBatch([][]byte{payload})
	if err != nil {
		return "", err
	}
	return files[0], nil
}

// PutBatch capacity-checks and persists all payloads under one lock. On a
// write/rename error it removes every file created by this call, so the caller
// can retry the reserved aggregation window without knowingly duplicating a
// partial batch.
func (s *CustomSpool) PutBatch(payloads [][]byte) ([]string, error) {
	if len(payloads) == 0 {
		return nil, fmt.Errorf("custom metric payload batch must not be empty")
	}
	for _, payload := range payloads {
		if len(payload) == 0 {
			return nil, fmt.Errorf("custom metric payload must not be empty")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stats, err := s.statsLocked()
	if err != nil {
		return nil, err
	}
	var required int64
	for _, payload := range payloads {
		required += int64(len(payload))
	}
	if required > s.maxBytes || stats.Bytes > s.maxBytes-required {
		return nil, ErrCustomSpoolFull
	}

	tempDir, err := os.MkdirTemp(s.dir, ".batch-*.tmp")
	if err != nil {
		return nil, err
	}
	rollback := func() { _ = os.RemoveAll(tempDir) }
	batchTime := s.now().UnixNano()
	for i, payload := range payloads {
		fileName := fmt.Sprintf("chunk-%06d.ready", i)
		filePath := filepath.Join(tempDir, fileName)
		tmp, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
		if err != nil {
			rollback()
			return nil, err
		}
		cleanupTemp := func() {
			_ = tmp.Close()
			_ = os.Remove(filePath)
		}
		if _, err := tmp.Write(payload); err != nil {
			cleanupTemp()
			rollback()
			return nil, err
		}
		if err := tmp.Sync(); err != nil {
			cleanupTemp()
			rollback()
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			cleanupTemp()
			rollback()
			return nil, err
		}
	}
	batchDirHandle, err := os.Open(tempDir)
	if err != nil {
		rollback()
		return nil, err
	}
	if err := batchDirHandle.Sync(); err != nil {
		_ = batchDirHandle.Close()
		rollback()
		return nil, err
	}
	_ = batchDirHandle.Close()
	batchDir := filepath.Join(s.dir, fmt.Sprintf("batch-%020d-%s.ready", batchTime, strings.TrimSuffix(filepath.Base(tempDir), ".tmp")))
	if err := os.Rename(tempDir, batchDir); err != nil {
		rollback()
		return nil, err
	}
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	created := make([]string, 0, len(payloads))
	for i := range payloads {
		created = append(created, filepath.Join(batchDir, fmt.Sprintf("chunk-%06d.ready", i)))
	}
	return created, nil
}

func (s *CustomSpool) Oldest() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.filesLocked()
	if err != nil || len(files) == 0 {
		return "", err
	}
	return files[0], nil
}

func (s *CustomSpool) Remove(file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.validReadyPath(file)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if parent != s.dir {
		entries, err := os.ReadDir(parent)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(parent)
		}
	}
	return nil
}

// Quarantine moves a permanently rejected or corrupt payload out of the replay
// queue while retaining evidence for bounded operator inspection.
func (s *CustomSpool) Quarantine(file, reason string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.validReadyPath(file)
	if err != nil {
		return "", err
	}
	reason = safeReason(reason)
	destination := filepath.Join(s.quarantine, strings.TrimSuffix(filepath.Base(path), ".ready")+"."+reason+".failed")
	if err := os.Rename(path, destination); err != nil {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent != s.dir {
		entries, err := os.ReadDir(parent)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(parent)
		}
	}
	return destination, nil
}

func (s *CustomSpool) Stats() (SpoolStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statsLocked()
}

func (s *CustomSpool) statsLocked() (SpoolStats, error) {
	files, err := s.filesLocked()
	if err != nil {
		return SpoolStats{}, err
	}
	stats := SpoolStats{Files: len(files)}
	var oldest time.Time
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return SpoolStats{}, err
		}
		stats.Bytes += info.Size()
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
	}
	if !oldest.IsZero() {
		stats.OldestAge = s.now().Sub(oldest)
		if stats.OldestAge < 0 {
			stats.OldestAge = 0
		}
	}
	return stats, nil
}

func (s *CustomSpool) filesLocked() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "custom-") && strings.HasSuffix(entry.Name(), ".ready") {
			files = append(files, filepath.Join(s.dir, entry.Name()))
			continue
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "batch-") && strings.HasSuffix(entry.Name(), ".ready") {
			batchDir := filepath.Join(s.dir, entry.Name())
			chunks, err := os.ReadDir(batchDir)
			if err != nil {
				return nil, err
			}
			if len(chunks) == 0 {
				return nil, fmt.Errorf("committed custom spool batch is empty: %s", batchDir)
			}
			for _, chunk := range chunks {
				if !chunk.Type().IsRegular() || !strings.HasPrefix(chunk.Name(), "chunk-") || !strings.HasSuffix(chunk.Name(), ".ready") {
					return nil, fmt.Errorf("invalid custom spool batch entry: %s", filepath.Join(batchDir, chunk.Name()))
				}
				files = append(files, filepath.Join(batchDir, chunk.Name()))
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func (s *CustomSpool) validReadyPath(file string) (string, error) {
	path := filepath.Clean(file)
	relative, err := filepath.Rel(s.dir, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path is not a custom spool file: %q", file)
	}
	base := filepath.Base(path)
	parent := filepath.Dir(path)
	legacy := parent == s.dir && strings.HasPrefix(base, "custom-") && strings.HasSuffix(base, ".ready")
	batched := filepath.Dir(parent) == s.dir && strings.HasPrefix(filepath.Base(parent), "batch-") && strings.HasSuffix(parent, ".ready") && strings.HasPrefix(base, "chunk-") && strings.HasSuffix(base, ".ready")
	if !legacy && !batched {
		return "", fmt.Errorf("path is not a custom spool file: %q", file)
	}
	return path, nil
}

func (s *CustomSpool) recoverStartup() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".batch-") && strings.HasSuffix(entry.Name(), ".tmp") {
			if err := os.RemoveAll(filepath.Join(s.dir, entry.Name())); err != nil {
				return fmt.Errorf("remove abandoned custom spool batch %q: %w", entry.Name(), err)
			}
			continue
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "batch-") && strings.HasSuffix(entry.Name(), ".ready") {
			batchDir := filepath.Join(s.dir, entry.Name())
			chunks, readErr := os.ReadDir(batchDir)
			valid := readErr == nil && len(chunks) > 0
			for _, chunk := range chunks {
				if !chunk.Type().IsRegular() || !strings.HasPrefix(chunk.Name(), "chunk-") || !strings.HasSuffix(chunk.Name(), ".ready") {
					valid = false
					break
				}
			}
			if !valid {
				destination := filepath.Join(s.quarantine, entry.Name()+".corrupt")
				if err := os.Rename(batchDir, destination); err != nil {
					return fmt.Errorf("quarantine corrupt custom spool batch %q: %w", entry.Name(), err)
				}
			}
		}
	}
	return nil
}

func safeReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	var b strings.Builder
	for _, r := range reason {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
