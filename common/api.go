package common

import (
	"strings"

	"github.com/codifinary/codexray-node-agent/flags"
)

func AuthHeaders() map[string]string {
	res := map[string]string{}
	if apiKey := *flags.ApiKey; apiKey != "" {
		res["X-Api-Key"] = apiKey
	}
	return res
}

func AuthHeadersForContainer(containerId string) map[string]string {
	res := map[string]string{}
	if containerId != "" && strings.Contains(containerId, "codexray") {
		if defaultKey := *flags.DefaultApiKey; defaultKey != "" {
			res["X-Api-Key"] = defaultKey
			return res
		}
	}
	if apiKey := *flags.ApiKey; apiKey != "" {
		res["X-Api-Key"] = apiKey
	}
	return res
}

// AuthHeadersForServiceName checks the service name (used by traces, logs, profiling)
func AuthHeadersForServiceName(serviceName string) map[string]string {
	res := map[string]string{}
	if serviceName != "" && strings.Contains(serviceName, "codexray") {
		if defaultKey := *flags.DefaultApiKey; defaultKey != "" {
			res["X-Api-Key"] = defaultKey
			return res
		}
	}
	if apiKey := *flags.ApiKey; apiKey != "" {
		res["X-Api-Key"] = apiKey
	}
	return res
}

func ApiKeyTypeForContainer(containerId string) string {
	if containerId != "" && strings.Contains(containerId, "codexray") {
		if defaultKey := *flags.DefaultApiKey; defaultKey != "" {
			_ = defaultKey
			return "DEFAULT"
		}
	}
	return "REGULAR"
}

// ApiKeyTypeForServiceName checks the service name (used by traces, logs, profiling)
func ApiKeyTypeForServiceName(serviceName string) string {
	if serviceName != "" && strings.Contains(serviceName, "codexray") {
		if defaultKey := *flags.DefaultApiKey; defaultKey != "" {
			_ = defaultKey
			return "DEFAULT"
		}
	}
	return "REGULAR"
}
