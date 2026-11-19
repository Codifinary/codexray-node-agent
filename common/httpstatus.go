// Copyright Codexray
// SPDX-License-Identifier: AGPL-3.0

package common

import "net/http"

// SendDisposition is what a sender does with a payload after the collector answered.
type SendDisposition int

const (
	SendSuccess SendDisposition = iota
	SendRetry
	SendPermanent
)

// ClassifyHTTPStatus maps a collector status to success, retry later, or a permanent rejection.
// 404 is the collector's "project not found", a config mistake like 401/403, so it is retried.
func ClassifyHTTPStatus(code int) SendDisposition {
	if code >= 200 && code < 300 {
		return SendSuccess
	}
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return SendRetry
	}
	if code >= 500 {
		return SendRetry
	}
	return SendPermanent
}
