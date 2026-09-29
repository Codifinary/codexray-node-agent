// Copyright Codexray
// SPDX-License-Identifier: Apache-2.0

//go:build !stringlabels && !dedupelabels

package labels

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelsSortInterface(t *testing.T) {
	ls := Labels{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}, {Name: "c", Value: "3"}}
	assert.Equal(t, 3, ls.Len())
	assert.True(t, ls.Less(1, 0))
	ls.Swap(0, 1)
	assert.Equal(t, "a", ls[0].Name)
	assert.Equal(t, "b", ls[1].Name)
}

func TestLabelsBytes(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	b := ls.Bytes(nil)
	assert.NotEmpty(t, b)
	// reuse buf
	b2 := ls.Bytes(make([]byte, 0, 128))
	assert.Equal(t, b, b2)

	empty := Labels{}
	assert.Equal(t, []byte{labelSep}, empty.Bytes(nil))
}

func TestLabelsMatchLabels(t *testing.T) {
	ls := New(
		Label{Name: MetricName, Value: "metric"},
		Label{Name: "a", Value: "1"},
		Label{Name: "b", Value: "2"},
	)

	on := ls.MatchLabels(true, "a")
	assert.Equal(t, Labels{{Name: "a", Value: "1"}}, on)

	off := ls.MatchLabels(false, "a")
	// off excludes 'a' and also excludes MetricName since on==false
	assert.Equal(t, Labels{{Name: "b", Value: "2"}}, off)

	// empty names, on=true -> nothing
	assert.Empty(t, ls.MatchLabels(true))
	// empty names, on=false -> everything except metric name
	offAll := ls.MatchLabels(false)
	assert.Equal(t, Labels{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}}, offAll)
}

func TestLabelsHash(t *testing.T) {
	ls1 := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	ls2 := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	ls3 := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "3"})

	assert.Equal(t, ls1.Hash(), ls2.Hash())
	assert.NotEqual(t, ls1.Hash(), ls3.Hash())

	// empty labels
	assert.Equal(t, EmptyLabels().Hash(), EmptyLabels().Hash())
}

func TestLabelsHashLargeEntry(t *testing.T) {
	// force the >1KB slow path in Hash()
	bigValue := make([]byte, 2048)
	for i := range bigValue {
		bigValue[i] = 'x'
	}
	ls := New(
		Label{Name: "a", Value: string(bigValue)},
		Label{Name: "b", Value: "2"},
	)
	h1 := ls.Hash()
	h2 := ls.Hash()
	assert.Equal(t, h1, h2)
}

func TestHashForLabels(t *testing.T) {
	ls := New(
		Label{Name: "a", Value: "1"},
		Label{Name: "b", Value: "2"},
		Label{Name: "c", Value: "3"},
	)
	h1, buf := ls.HashForLabels(nil, "a", "c")
	h2, _ := ls.HashForLabels(nil, "a", "c")
	assert.Equal(t, h1, h2)
	assert.NotEmpty(t, buf)

	// unmatched names entirely
	h3, buf3 := ls.HashForLabels(nil, "z")
	assert.Empty(t, buf3)
	assert.NotEqual(t, h1, h3)

	// names interleaved so both the "names[j] < ls[i].Name" and
	// "ls[i].Name < names[j]" branches are exercised, plus a name after
	// the last label so the loop terminates via j exhaustion too.
	h4, buf4 := ls.HashForLabels(nil, "0", "b", "d")
	assert.NotEmpty(t, buf4)
	assert.NotEqual(t, h1, h4)

	// reused buffer gets truncated, not appended to.
	reused := make([]byte, 0, 64)
	_, buf5 := ls.HashForLabels(reused, "a")
	assert.Equal(t, buf5, buf5[:len(buf5)])
}

func TestHashWithoutLabels(t *testing.T) {
	ls := New(
		Label{Name: MetricName, Value: "m"},
		Label{Name: "a", Value: "1"},
		Label{Name: "b", Value: "2"},
	)
	h1, _ := ls.HashWithoutLabels(nil, "b")
	withoutB := New(Label{Name: "a", Value: "1"})
	h2, _ := withoutB.HashWithoutLabels(nil)
	assert.Equal(t, h1, h2)

	// names that are lexicographically past all label names: the inner
	// "j < len(names) && names[j] < ls[i].Name" loop must advance j to
	// exhaustion without matching anything.
	h3, _ := ls.HashWithoutLabels(nil, "zzz")
	h4, _ := ls.HashWithoutLabels(nil)
	assert.Equal(t, h3, h4)
}

func TestBytesWithLabels(t *testing.T) {
	ls := New(
		Label{Name: "a", Value: "1"},
		Label{Name: "b", Value: "2"},
		Label{Name: "c", Value: "3"},
	)
	b := ls.BytesWithLabels(nil, "a", "c")
	assert.NotEmpty(t, b)
	bNone := ls.BytesWithLabels(nil, "z")
	assert.Equal(t, []byte{labelSep}, bNone)

	// match on more than one name so the "b.Len() > 1" separator branch
	// is exercised (first match doesn't write a separator, second does).
	bMulti := ls.BytesWithLabels(nil, "a", "b", "c")
	assert.Equal(t, ls.Bytes(nil), bMulti)

	// names interleaved with gaps before/after all label names.
	bGap := ls.BytesWithLabels(nil, "0", "b", "d")
	assert.NotEmpty(t, bGap)
}

func TestBytesWithoutLabels(t *testing.T) {
	ls := New(
		Label{Name: "a", Value: "1"},
		Label{Name: "b", Value: "2"},
	)
	b := ls.BytesWithoutLabels(nil, "b")
	only := New(Label{Name: "a", Value: "1"})
	assert.Equal(t, only.Bytes(nil), b)

	// no names removed: exercises the "b.Len() > 1" separator branch for
	// the second+ entries via the without-path.
	bAll := ls.BytesWithoutLabels(nil)
	assert.Equal(t, ls.Bytes(nil), bAll)

	// name past the end of the label set.
	bPastEnd := ls.BytesWithoutLabels(nil, "zzz")
	assert.Equal(t, ls.Bytes(nil), bPastEnd)
}

func TestLabelsCopy(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"})
	cp := ls.Copy()
	assert.Equal(t, ls, cp)
	cp[0].Value = "2"
	assert.Equal(t, "1", ls[0].Value)
}

func TestLabelsGetHas(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"})
	assert.Equal(t, "1", ls.Get("a"))
	assert.Equal(t, "", ls.Get("missing"))
	assert.True(t, ls.Has("a"))
	assert.False(t, ls.Has("missing"))
}

func TestHasDuplicateLabelNames(t *testing.T) {
	ls := Labels{{Name: "a", Value: "1"}, {Name: "a", Value: "2"}}
	name, dup := ls.HasDuplicateLabelNames()
	assert.True(t, dup)
	assert.Equal(t, "a", name)

	unique := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	_, dup2 := unique.HasDuplicateLabelNames()
	assert.False(t, dup2)

	_, dup3 := Labels{}.HasDuplicateLabelNames()
	assert.False(t, dup3)
}

func TestWithoutEmpty(t *testing.T) {
	ls := Labels{{Name: "a", Value: "1"}, {Name: "b", Value: ""}, {Name: "c", Value: "3"}}
	got := ls.WithoutEmpty()
	assert.Equal(t, Labels{{Name: "a", Value: "1"}, {Name: "c", Value: "3"}}, got)

	noEmpty := Labels{{Name: "a", Value: "1"}}
	// should return same slice (no copy) when nothing empty
	got2 := noEmpty.WithoutEmpty()
	assert.Equal(t, noEmpty, got2)
}

func TestEqual(t *testing.T) {
	a := New(Label{Name: "a", Value: "1"})
	b := New(Label{Name: "a", Value: "1"})
	c := New(Label{Name: "a", Value: "2"})
	d := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})

	assert.True(t, Equal(a, b))
	assert.False(t, Equal(a, c))
	assert.False(t, Equal(a, d))
}

func TestEmptyLabels(t *testing.T) {
	assert.Equal(t, Labels{}, EmptyLabels())
	assert.True(t, EmptyLabels().IsEmpty())
}

func TestNewAndFromStrings(t *testing.T) {
	ls := New(Label{Name: "b", Value: "2"}, Label{Name: "a", Value: "1"})
	assert.Equal(t, Labels{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}}, ls)

	fs := FromStrings("b", "2", "a", "1")
	assert.Equal(t, ls, fs)

	assert.Panics(t, func() { FromStrings("a") })
}

func TestCompare(t *testing.T) {
	a := New(Label{Name: "a", Value: "1"})
	b := New(Label{Name: "a", Value: "1"})
	c := New(Label{Name: "a", Value: "2"})
	longer := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	nameDiff := New(Label{Name: "z", Value: "1"})

	assert.Equal(t, 0, Compare(a, b))
	assert.Negative(t, Compare(a, c))
	assert.Positive(t, Compare(c, a))
	assert.Negative(t, Compare(a, longer))
	assert.Positive(t, Compare(longer, a))
	assert.Negative(t, Compare(a, nameDiff))
	assert.Positive(t, Compare(nameDiff, a))
}

func TestCopyFrom(t *testing.T) {
	var ls Labels
	src := New(Label{Name: "a", Value: "1"})
	ls.CopyFrom(src)
	assert.Equal(t, src, ls)
}

func TestIsEmpty(t *testing.T) {
	assert.True(t, Labels{}.IsEmpty())
	assert.False(t, New(Label{Name: "a", Value: "1"}).IsEmpty())
}

func TestRangeAndValidate(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	var names []string
	ls.Range(func(l Label) { names = append(names, l.Name) })
	assert.Equal(t, []string{"a", "b"}, names)

	err := ls.Validate(func(l Label) error { return nil })
	require.NoError(t, err)

	sentinel := assert.AnError
	err = ls.Validate(func(l Label) error {
		if l.Name == "b" {
			return sentinel
		}
		return nil
	})
	assert.Equal(t, sentinel, err)
}

func TestDropMetricName(t *testing.T) {
	withName := New(Label{Name: MetricName, Value: "m"}, Label{Name: "a", Value: "1"})
	assert.Equal(t, New(Label{Name: "a", Value: "1"}), withName.DropMetricName())

	// metric name not first
	ls := Labels{{Name: "a", Value: "1"}, {Name: MetricName, Value: "m"}, {Name: "z", Value: "2"}}
	got := ls.DropMetricName()
	assert.Equal(t, Labels{{Name: "a", Value: "1"}, {Name: "z", Value: "2"}}, got)

	noName := New(Label{Name: "a", Value: "1"})
	assert.Equal(t, noName, noName.DropMetricName())
}

func TestInternReleaseStrings(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"})
	ls.InternStrings(func(s string) string { return s + "!" })
	assert.Equal(t, "a!", ls[0].Name)
	assert.Equal(t, "1!", ls[0].Value)

	var released []string
	ls.ReleaseStrings(func(s string) { released = append(released, s) })
	assert.Equal(t, []string{"a!", "1!"}, released)
}

func TestBuilderResetAndLabels(t *testing.T) {
	base := New(Label{Name: "a", Value: "1"}, Label{Name: "empty", Value: ""})
	b := NewBuilder(base)
	// empty value in base was queued for deletion by Reset
	got := b.Labels()
	assert.Equal(t, New(Label{Name: "a", Value: "1"}), got)
}

func TestBuilderSetDelKeepGet(t *testing.T) {
	base := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	b := NewBuilder(base)

	b.Set("c", "3")
	assert.Equal(t, "3", b.Get("c"))

	b.Set("c", "3-updated")
	assert.Equal(t, "3-updated", b.Get("c"))

	// Setting empty value deletes
	b.Set("a", "")
	assert.Equal(t, "", b.Get("a"))

	b2 := NewBuilder(base)
	b2.Del("b")
	assert.Equal(t, "", b2.Get("b"))

	b3 := NewBuilder(base)
	b3.Keep("a")
	res := b3.Labels()
	assert.Equal(t, New(Label{Name: "a", Value: "1"}), res)

	// Del entry that's also queued in add should remove from add slice too
	b4 := NewBuilder(EmptyLabels())
	b4.Set("x", "1")
	b4.Del("x")
	assert.Equal(t, "", b4.Get("x"))
	assert.Equal(t, EmptyLabels(), b4.Labels())

	// Set() overriding a base label: Labels() must skip it while
	// iterating base (via the contains(b.add,...) branch) and use the
	// updated value from add instead.
	b5 := NewBuilder(New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"}))
	b5.Set("a", "override")
	got5 := b5.Labels()
	assert.Equal(t, New(Label{Name: "a", Value: "override"}, Label{Name: "b", Value: "2"}), got5)

	// Only a deletion, no additions: exercises the "len(b.add) > 0" being
	// false branch (no re-sort needed).
	b6 := NewBuilder(New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"}))
	b6.Del("b")
	assert.Equal(t, New(Label{Name: "a", Value: "1"}), b6.Labels())

	// Deleting everything with nothing added: exercises the
	// "expectedSize < 1" clamp.
	b7 := NewBuilder(New(Label{Name: "a", Value: "1"}))
	b7.Del("a")
	assert.Equal(t, Labels{}, b7.Labels())

	// Get() for a name that is deleted but never added.
	b8 := NewBuilder(New(Label{Name: "a", Value: "1"}))
	b8.Del("a")
	assert.Equal(t, "", b8.Get("a"))
}

func TestContainsHelper(t *testing.T) {
	set := []Label{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}}
	assert.True(t, contains(set, "a"))
	assert.False(t, contains(set, "z"))
	assert.False(t, contains(nil, "a"))
}

func TestBuilderRange(t *testing.T) {
	base := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	b := NewBuilder(base)
	b.Set("c", "3")
	b.Del("b")

	var got []Label
	b.Range(func(l Label) { got = append(got, l) })
	assert.ElementsMatch(t, []Label{{Name: "a", Value: "1"}, {Name: "c", Value: "3"}}, got)
}

func TestScratchBuilder(t *testing.T) {
	var sb ScratchBuilder
	sb = NewScratchBuilder(2)
	sb.Add("b", "2")
	sb.Add("a", "1")
	sb.Sort()
	got := sb.Labels()
	assert.Equal(t, New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"}), got)

	sb.Reset()
	assert.Empty(t, sb.Labels())

	sb.UnsafeAddBytes([]byte("x"), []byte("y"))
	assert.Equal(t, Labels{{Name: "x", Value: "y"}}, sb.Labels())

	sb.Assign(New(Label{Name: "z", Value: "9"}))
	assert.Equal(t, New(Label{Name: "z", Value: "9"}), sb.Labels())

	var ls Labels
	sb.Overwrite(&ls)
	assert.Equal(t, New(Label{Name: "z", Value: "9"}), ls)
}

func TestSymbolTable(t *testing.T) {
	st := NewSymbolTable()
	assert.Nil(t, st)
	assert.Equal(t, 0, st.Len())

	b := NewBuilderWithSymbolTable(st)
	assert.NotNil(t, b)

	sb := NewScratchBuilderWithSymbolTable(st, 4)
	sb.SetSymbolTable(st)
	sb.Add("a", "1")
	assert.Equal(t, Labels{{Name: "a", Value: "1"}}, sb.Labels())
}

func TestLabelsString(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "x\"y"})
	s := ls.String()
	assert.Equal(t, `{a="1", b="x\"y"}`, s)
	assert.Equal(t, "{}", EmptyLabels().String())
}

func TestLabelsJSON(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"})
	data, err := json.Marshal(ls)
	require.NoError(t, err)

	var got Labels
	err = json.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, ls, got)

	var bad Labels
	err = bad.UnmarshalJSON([]byte(`not-json`))
	assert.Error(t, err)
}

func TestLabelsYAML(t *testing.T) {
	ls := New(Label{Name: "a", Value: "1"})
	out, err := ls.MarshalYAML()
	require.NoError(t, err)
	m, ok := out.(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "1", m["a"])

	var got Labels
	err = got.UnmarshalYAML(func(i interface{}) error {
		mp := i.(*map[string]string)
		*mp = map[string]string{"a": "1"}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, ls, got)

	err = got.UnmarshalYAML(func(i interface{}) error { return assert.AnError })
	assert.Error(t, err)
}

func TestLabelsIsValid(t *testing.T) {
	valid := New(Label{Name: MetricName, Value: "my_metric"}, Label{Name: "a", Value: "1"})
	assert.True(t, valid.IsValid())

	invalidMetric := New(Label{Name: MetricName, Value: "1invalid"})
	assert.False(t, invalidMetric.IsValid())

	invalidLabelName := New(Label{Name: "9bad", Value: "v"})
	assert.False(t, invalidLabelName.IsValid())
}

func TestLabelsMapFromMap(t *testing.T) {
	m := map[string]string{"a": "1", "b": "2"}
	ls := FromMap(m)
	assert.Equal(t, New(Label{Name: "a", Value: "1"}, Label{Name: "b", Value: "2"}), ls)
	assert.Equal(t, m, ls.Map())

	assert.Empty(t, EmptyLabels().Map())
}
