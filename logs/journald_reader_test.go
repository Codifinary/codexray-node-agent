package logs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJournaldReaderNamesEveryPathThatFailed(t *testing.T) {
	missing := []string{filepath.Join(t.TempDir(), "run-journal"), filepath.Join(t.TempDir(), "var-journal")}

	r, err := NewJournaldReader(missing...)

	require.Error(t, err)
	assert.Nil(t, r)
	for _, path := range missing {
		assert.Contains(t, err.Error(), path, "the reason for each path reaches the caller")
	}
}
