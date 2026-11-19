package dogstatsd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/codifinary/codexray-node-agent/common"
)

func TestCustomSenderClassifiesResponsesAndSetsHeaders(t *testing.T) {
	tests := []struct {
		status int
		want   common.SendDisposition
	}{
		{http.StatusNoContent, common.SendSuccess},
		{http.StatusBadRequest, common.SendPermanent},
		{http.StatusUnauthorized, common.SendRetry},
		{http.StatusForbidden, common.SendRetry},
		{http.StatusNotFound, common.SendRetry},
		{http.StatusRequestEntityTooLarge, common.SendPermanent},
		{http.StatusRequestTimeout, common.SendRetry},
		{http.StatusTooManyRequests, common.SendRetry},
		{http.StatusInternalServerError, common.SendRetry},
		{http.StatusServiceUnavailable, common.SendRetry},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != "secret" || r.Header.Get("Content-Encoding") != "snappy" {
					t.Errorf("missing sender headers: %#v", r.Header)
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != "payload" {
					t.Errorf("body=%q", body)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			file := filepath.Join(t.TempDir(), "payload.ready")
			if err := os.WriteFile(file, []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			sender, err := NewCustomSender(server.Client(), server.URL, map[string]string{"X-Api-Key": "secret"})
			if err != nil {
				t.Fatal(err)
			}
			got, code, err := sender.SendFile(file)
			if got != tc.want || code != tc.status {
				t.Fatalf("result=(%v,%d), want (%v,%d), err=%v", got, code, tc.want, tc.status, err)
			}
			if tc.want == common.SendSuccess && err != nil {
				t.Fatal(err)
			}
			if tc.want != common.SendSuccess && err == nil {
				t.Fatal("expected status error")
			}
		})
	}
}
