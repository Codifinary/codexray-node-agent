package demangle

import (
	"testing"

	"github.com/ianlancetaylor/demangle"
	"github.com/stretchr/testify/require"
)

func TestConvertDemangleOptions(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected []demangle.Option
	}{
		{"none", "none", DemangleNoneSpecified},
		{"simplified", "simplified", DemangleSimplified},
		{"templates", "templates", DemangleTemplates},
		{"full", "full", DemangleFull},
		{"unknown", "bogus", DemangleUnspecified},
		{"empty", "", DemangleUnspecified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConvertDemangleOptions(tc.input)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestDemangleUnspecifiedIsNil(t *testing.T) {
	require.Nil(t, DemangleUnspecified)
}

func TestDemangleNoneSpecifiedIsEmptyNotNil(t *testing.T) {
	require.NotNil(t, DemangleNoneSpecified)
	require.Empty(t, DemangleNoneSpecified)
}

// TestDemangleEndToEnd sanity-checks that the option sets returned by
// ConvertDemangleOptions actually work when fed into the underlying
// demangle library on a real mangled C++ symbol.
func TestDemangleEndToEnd(t *testing.T) {
	const mangled = "_Znwm"                      // operator new(unsigned long)
	const mangledFn = "_ZN3foo3barEiiRKSs"        // foo::bar(int, int, std::string const&)
	full, err := demangle.ToString(mangledFn, ConvertDemangleOptions("full")...)
	require.NoError(t, err)
	require.Contains(t, full, "foo::bar")

	simplified, err := demangle.ToString(mangledFn, ConvertDemangleOptions("simplified")...)
	require.NoError(t, err)
	require.Contains(t, simplified, "foo::bar")

	basic, err := demangle.ToString(mangled, ConvertDemangleOptions("none")...)
	require.NoError(t, err)
	require.Contains(t, basic, "operator new")
}
