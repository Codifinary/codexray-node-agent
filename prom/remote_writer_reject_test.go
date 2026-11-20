package prom

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSpoolFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestSendOnceQuarantinesPermanentlyRejectedFileAndMovesOn(t *testing.T) {
	c := &collector{status: http.StatusBadRequest, reply: "out of bounds\n"}
	spoolDir := t.TempDir()
	a := newFallbackTestAgent(t, spoolDir, c)
	rejected := writeSpoolFile(t, spoolDir, "spool-1.done", "one")
	writeSpoolFile(t, spoolDir, "spool-2.done", "two")

	if sent, err := a.sendOnce(); !sent || err != nil {
		t.Fatalf("sent=%v err=%v, want the rejected file handled without backoff", sent, err)
	}
	if _, err := os.Stat(rejected); !os.IsNotExist(err) {
		t.Fatalf("rejected file still queued: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spoolDir, quarantineDirName, "spool-1.done.400.failed")); err != nil {
		t.Fatalf("rejected file not quarantined: %v", err)
	}
	if got := metricValue(t, a.metrics.rejectedPayloads.WithLabelValues("400")); got != 1 {
		t.Fatalf("rejected_payloads_total{code=400}=%v, want 1", got)
	}
	files, err := a.listSpoolFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "spool-2.done" {
		t.Fatalf("queued files=%v, want only spool-2.done (quarantine is not queued)", files)
	}

	c.status = http.StatusNoContent
	if _, err := a.sendOnce(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.bodies, ","); got != "one,two" {
		t.Fatalf("collector received %q, want one,two", got)
	}
}

func TestSendOnceKeepsFileOnRetryableStatus(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusRequestTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			spoolDir := t.TempDir()
			a := newFallbackTestAgent(t, spoolDir, &collector{status: status})
			file := writeSpoolFile(t, spoolDir, "spool-1.done", "one")

			if sent, err := a.sendOnce(); !sent || err == nil {
				t.Fatalf("sent=%v err=%v, want a retryable failure", sent, err)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("file not kept for the retry: %v", err)
			}
			if _, err := os.Stat(filepath.Join(spoolDir, quarantineDirName)); !os.IsNotExist(err) {
				t.Fatalf("quarantine created for a retryable status: %v", err)
			}
		})
	}
}

func TestSendOnceDropsPermanentlyRejectedMemoryPayload(t *testing.T) {
	c := &collector{status: http.StatusUnprocessableEntity}
	a := newFallbackTestAgent(t, t.TempDir(), c)
	a.bufferInMemory(1, []byte("from-memory"))

	if sent, err := a.sendOnce(); !sent || err != nil {
		t.Fatalf("sent=%v err=%v, want the rejected payload dropped", sent, err)
	}
	if len(a.memBuf) != 0 || a.memBufBytes != 0 {
		t.Fatalf("rejected payload kept: payloads=%d bytes=%d", len(a.memBuf), a.memBufBytes)
	}
	if got := metricValue(t, a.metrics.rejectedPayloads.WithLabelValues("422")); got != 1 {
		t.Fatalf("rejected_payloads_total{code=422}=%v, want 1", got)
	}
}

func TestSendOnceTreatsFileEvictedMidSendAsSent(t *testing.T) {
	spoolDir := t.TempDir()
	a := newFallbackTestAgent(t, spoolDir, nil)
	file := writeSpoolFile(t, spoolDir, "spool-1.done", "one")
	a.httpClient = http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
	})}

	if sent, err := a.sendOnce(); !sent || err != nil {
		t.Fatalf("sent=%v err=%v, want a file the spool evicted mid-send to count as sent", sent, err)
	}
}

func TestQuarantineSpoolFileDropsOldestOverQuota(t *testing.T) {
	spoolDir := t.TempDir()
	quarantine := filepath.Join(spoolDir, quarantineDirName)
	if err := os.MkdirAll(quarantine, 0750); err != nil {
		t.Fatal(err)
	}
	oldest := writeSpoolFile(t, quarantine, "spool-1.done.400.failed", "0123456789")
	src := writeSpoolFile(t, spoolDir, "spool-2.done", "0123456789")

	dest, err := quarantineSpoolFile(src, quarantine, http.StatusBadRequest, 15)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("oldest quarantined file kept over the quota: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("newly quarantined file missing: %v", err)
	}
}

func TestRemoteWriteErrorCarriesTheCollectorReason(t *testing.T) {
	c := &collector{status: http.StatusBadRequest, reply: "  out of bounds\n"}
	a := newFallbackTestAgent(t, t.TempDir(), c)

	err := a.post(strings.NewReader("payload"))

	rejected, ok := permanentRejection(err)
	if !ok {
		t.Fatalf("err=%v, want a permanent rejection", err)
	}
	if rejected.Error() != "Bad Request: out of bounds" {
		t.Fatalf("error=%q, want the status and the trimmed body", rejected.Error())
	}
	if _, ok := permanentRejection(&remoteWriteError{status: "Service Unavailable", code: http.StatusServiceUnavailable}); ok {
		t.Fatal("503 must not be a permanent rejection")
	}
}
