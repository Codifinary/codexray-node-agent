// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package labels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFastRegexMatcherLiteral(t *testing.T) {
	m, err := NewFastRegexMatcher("foo")
	require.NoError(t, err)
	assert.True(t, m.MatchString("foo"))
	assert.False(t, m.MatchString("foobar"))
	assert.Equal(t, "foo", m.GetRegexString())
}

func TestNewFastRegexMatcherPattern(t *testing.T) {
	m, err := NewFastRegexMatcher("foo.*bar")
	require.NoError(t, err)
	assert.True(t, m.MatchString("fooXXXbar"))
	assert.False(t, m.MatchString("nope"))
	assert.Equal(t, "^(?:foo.*bar)$", m.GetRegexString())
}

func TestNewFastRegexMatcherInvalid(t *testing.T) {
	_, err := NewFastRegexMatcher("[")
	assert.Error(t, err)
}

func TestNewFastRegexMatcherPrefixSuffixContains(t *testing.T) {
	m, err := NewFastRegexMatcher("prefix.*middle.*suffix")
	require.NoError(t, err)
	assert.True(t, m.MatchString("prefixXXXmiddleXXXsuffix"))
	assert.False(t, m.MatchString("noprefixmiddlesuffix"))
	assert.False(t, m.MatchString("prefixXXXmiddleXXXnosuffi"))
	assert.False(t, m.MatchString("prefix_suffix_no_middle"))
}

func TestNewFastRegexMatcherAnchors(t *testing.T) {
	m, err := NewFastRegexMatcher("^foo$")
	require.NoError(t, err)
	assert.True(t, m.MatchString("foo"))
}

func TestIsLiteral(t *testing.T) {
	assert.True(t, isLiteral("foo"))
	assert.False(t, isLiteral("foo.*"))
	assert.False(t, isLiteral("[abc]"))
}

func TestFastRegexMatcherEmptyPattern(t *testing.T) {
	m, err := NewFastRegexMatcher("")
	require.NoError(t, err)
	assert.True(t, m.MatchString(""))
	assert.False(t, m.MatchString("x"))
}

func TestFastRegexMatcherCaseInsensitive(t *testing.T) {
	m, err := NewFastRegexMatcher("(?i)FOO")
	require.NoError(t, err)
	assert.True(t, m.MatchString("foo"))
	assert.True(t, m.MatchString("FOO"))
}

func TestFastRegexMatcherNonConcatOp(t *testing.T) {
	// A single alternation is not an OpConcat, so prefix/suffix/contains
	// optimization is skipped entirely and matching falls through to re.
	m, err := NewFastRegexMatcher("foo|bar")
	require.NoError(t, err)
	assert.True(t, m.MatchString("foo"))
	assert.True(t, m.MatchString("bar"))
	assert.False(t, m.MatchString("baz"))
}

func TestFastRegexMatcherPrefixOnly(t *testing.T) {
	m, err := NewFastRegexMatcher("foo.*")
	require.NoError(t, err)
	assert.True(t, m.MatchString("foobar"))
	assert.False(t, m.MatchString("xfoobar"))
}

func TestFastRegexMatcherSuffixOnly(t *testing.T) {
	m, err := NewFastRegexMatcher(".*bar")
	require.NoError(t, err)
	assert.True(t, m.MatchString("foobar"))
	assert.False(t, m.MatchString("barx"))
}

func TestFastRegexMatcherContainsMismatch(t *testing.T) {
	// prefix and suffix match, but the middle literal ("middle") is
	// missing, exercising the "contains" mismatch branch specifically.
	m, err := NewFastRegexMatcher("prefix.*middle.*suffix")
	require.NoError(t, err)
	assert.False(t, m.MatchString("prefixXXXsuffix"))
}

func TestFastRegexMatcherEmptySubAfterAnchors(t *testing.T) {
	// "^$" reduces to an empty sub slice after stripping begin/end text
	// matchers, exercising the early return in optimizeConcatRegex.
	m, err := NewFastRegexMatcher("^$")
	require.NoError(t, err)
	assert.True(t, m.MatchString(""))
	assert.False(t, m.MatchString("x"))
}

func TestFastRegexMatcherFoldCaseLiteralNotUsedAsPrefix(t *testing.T) {
	// Case-folded literal at the start must NOT be optimized into a
	// plain prefix check (it's excluded via the FoldCase flag check).
	m, err := NewFastRegexMatcher("(?i)foo.*bar")
	require.NoError(t, err)
	assert.True(t, m.MatchString("FOOxxxBAR"))
	assert.False(t, m.MatchString("nope"))
}
