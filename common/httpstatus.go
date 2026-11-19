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
func ClassifyHTTPStatus(code int) SendDisposition {
	if code >= 200 && code < 300 {
		return SendSuccess
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500 {
		return SendRetry
	}
	return SendPermanent
}
