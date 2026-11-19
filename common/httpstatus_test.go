package common

import (
	"net/http"
	"testing"
)

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		code int
		want SendDisposition
	}{
		{http.StatusOK, SendSuccess},
		{http.StatusNoContent, SendSuccess},
		{http.StatusBadRequest, SendPermanent},
		{http.StatusNotFound, SendPermanent},
		{http.StatusRequestEntityTooLarge, SendPermanent},
		{http.StatusUnprocessableEntity, SendPermanent},
		{http.StatusMovedPermanently, SendPermanent},
		{http.StatusUnauthorized, SendRetry},
		{http.StatusForbidden, SendRetry},
		{http.StatusRequestTimeout, SendRetry},
		{http.StatusTooManyRequests, SendRetry},
		{http.StatusInternalServerError, SendRetry},
		{http.StatusServiceUnavailable, SendRetry},
	}
	for _, tt := range tests {
		if got := ClassifyHTTPStatus(tt.code); got != tt.want {
			t.Errorf("ClassifyHTTPStatus(%d)=%v, want %v", tt.code, got, tt.want)
		}
	}
}
