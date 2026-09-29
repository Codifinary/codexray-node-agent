package symtab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoopSymbolNameResolver(t *testing.T) {
	var r SymbolNameResolver = &noopSymbolNameResolver{}
	require.False(t, r.IsDead())
	require.Equal(t, "", r.Resolve(0x1234))
	require.Zero(t, r.DebugInfo())
	// Refresh/Cleanup are no-ops, just make sure they don't panic.
	r.Refresh()
	r.Cleanup()
}
