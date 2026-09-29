package symtab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSymbolTabMisc(t *testing.T) {
	tab := NewSymbolTab([]Symbol{{Start: 0x1000, Name: "foo"}})
	require.Equal(t, "SymbolTab{TODO}", tab.DebugString())

	// Refresh and Cleanup are no-ops today, but must not panic.
	tab.Refresh()
	tab.Cleanup()

	tab.Rebase(0x2000)
	require.Equal(t, uint64(0x2000), tab.base)

	// resolving below the rebased range returns the zero Symbol
	require.Equal(t, Symbol{}, tab.Resolve(0x2000))

	// an empty table always resolves to the zero Symbol
	empty := NewSymbolTab(nil)
	require.Equal(t, Symbol{}, empty.Resolve(0x1234))
}
