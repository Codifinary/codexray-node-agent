// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

package labels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchTypeString(t *testing.T) {
	assert.Equal(t, "=", MatchEqual.String())
	assert.Equal(t, "!=", MatchNotEqual.String())
	assert.Equal(t, "=~", MatchRegexp.String())
	assert.Equal(t, "!~", MatchNotRegexp.String())
	assert.Panics(t, func() { _ = MatchType(99).String() })
}

func TestNewMatcher(t *testing.T) {
	m, err := NewMatcher(MatchEqual, "a", "1")
	require.NoError(t, err)
	assert.Equal(t, "a", m.Name)
	assert.Equal(t, "1", m.Value)
	assert.Equal(t, "", m.GetRegexString())

	mre, err := NewMatcher(MatchRegexp, "a", "1.*")
	require.NoError(t, err)
	assert.Equal(t, "^(?:1.*)$", mre.GetRegexString())

	_, err = NewMatcher(MatchRegexp, "a", "[")
	assert.Error(t, err)
}

func TestMustNewMatcher(t *testing.T) {
	m := MustNewMatcher(MatchEqual, "a", "1")
	assert.Equal(t, "a", m.Name)
	assert.Panics(t, func() { MustNewMatcher(MatchRegexp, "a", "[") })
}

func TestMatcherString(t *testing.T) {
	m := MustNewMatcher(MatchEqual, "a", "1")
	assert.Equal(t, `a="1"`, m.String())
	mre := MustNewMatcher(MatchRegexp, "a", "1.*")
	assert.Equal(t, `a=~"1.*"`, mre.String())
}

func TestMatcherMatches(t *testing.T) {
	eq := MustNewMatcher(MatchEqual, "a", "1")
	assert.True(t, eq.Matches("1"))
	assert.False(t, eq.Matches("2"))

	neq := MustNewMatcher(MatchNotEqual, "a", "1")
	assert.False(t, neq.Matches("1"))
	assert.True(t, neq.Matches("2"))

	re := MustNewMatcher(MatchRegexp, "a", "1.*")
	assert.True(t, re.Matches("123"))
	assert.False(t, re.Matches("223"))

	nre := MustNewMatcher(MatchNotRegexp, "a", "1.*")
	assert.False(t, nre.Matches("123"))
	assert.True(t, nre.Matches("223"))
}

func TestMatcherMatchesInvalidType(t *testing.T) {
	m := &Matcher{Type: MatchType(99), Name: "a", Value: "1"}
	assert.Panics(t, func() { m.Matches("1") })
}

func TestMatcherInverse(t *testing.T) {
	cases := []struct {
		in   MatchType
		want MatchType
	}{
		{MatchEqual, MatchNotEqual},
		{MatchNotEqual, MatchEqual},
		{MatchRegexp, MatchNotRegexp},
		{MatchNotRegexp, MatchRegexp},
	}
	for _, c := range cases {
		m := MustNewMatcher(c.in, "a", "1")
		inv, err := m.Inverse()
		require.NoError(t, err)
		assert.Equal(t, c.want, inv.Type)
	}

	bad := &Matcher{Type: MatchType(99), Name: "a", Value: "1"}
	assert.Panics(t, func() { _, _ = bad.Inverse() })
}

func TestMatcherInverseInvalidRegex(t *testing.T) {
	// Bypass NewMatcher to construct a MatchRegexp matcher with an invalid
	// pattern; Inverse() must propagate the compile error from NewMatcher.
	m := &Matcher{Type: MatchRegexp, Name: "a", Value: "["}
	inv, err := m.Inverse()
	assert.Error(t, err)
	assert.Nil(t, inv)
}

func TestMatcherGetRegexStringNilRe(t *testing.T) {
	m := &Matcher{Type: MatchEqual, Name: "a", Value: "1"}
	assert.Equal(t, "", m.GetRegexString())
}
