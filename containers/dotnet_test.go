package containers

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jpillora/backoff"
	"github.com/stretchr/testify/assert"
)

func TestNextDotNetAttemptSlowsDownWhenThereIsNoSocket(t *testing.T) {
	b := &backoff.Backoff{Factor: 2, Min: time.Second, Max: time.Minute}
	for streak := 1; streak < dotNetNoSocketAttempts; streak++ {
		d, warn := nextDotNetAttempt(errNoDiagnosticSocket, streak, b)
		assert.Equal(t, time.Second<<(streak-1), d, "attempt %d follows the backoff", streak)
		assert.False(t, warn, "attempt %d is not warned about", streak)
	}

	d, warn := nextDotNetAttempt(fmt.Errorf("connect: %w", errNoDiagnosticSocket), dotNetNoSocketAttempts, b)
	assert.Equal(t, dotNetNoSocketRetryInterval, d)
	assert.True(t, warn, "one warning when retries slow down")

	d, warn = nextDotNetAttempt(errNoDiagnosticSocket, dotNetNoSocketAttempts+1, b)
	assert.Equal(t, dotNetNoSocketRetryInterval, d)
	assert.False(t, warn, "no warning after the first slow retry")
}

func TestNextDotNetAttemptWarnsOnOtherErrors(t *testing.T) {
	b := &backoff.Backoff{Factor: 2, Min: time.Second, Max: time.Minute}

	d, warn := nextDotNetAttempt(errors.New("connection reset"), 0, b)

	assert.Equal(t, time.Second, d)
	assert.True(t, warn)
}
