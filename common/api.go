package common

import (
	"github.com/codifinary/codexray-node-agent/flags"
)

func AuthHeaders() map[string]string {
	res := map[string]string{}
	if apiKey := *flags.ApiKey; apiKey != "" {
		res["X-Api-Key"] = apiKey
	}
	return res
}
