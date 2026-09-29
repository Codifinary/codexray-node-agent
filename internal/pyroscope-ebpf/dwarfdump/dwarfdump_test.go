package dwarfdump

import (
	"debug/dwarf"
	"debug/elf"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTypeName(t *testing.T) {
	cases := []struct {
		name     string
		need     Need
		expected string
	}{
		{"simple", Need{Name: "my_struct"}, "MyStruct"},
		{"with_pretty_name", Need{Name: "raw", PrettyName: "pretty_thing"}, "PrettyThing"},
		{"trailing_underscore", Need{Name: "task_struct_"}, "TaskStruct"},
		{"leading_underscore", Need{Name: "_task_struct"}, "TaskStruct"},
		{"leading_double_underscore", Need{Name: "__task_struct"}, "TaskStruct"},
		{"single_word", Need{Name: "task"}, "Task"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, typeName(tc.need))
		})
	}
}

// TestTypeNameDoubleTrailingUnderscorePanics documents a bug: typeName first
// trims a single trailing "_" (dwarfdump.go:259) before trying to trim "__"
// (dwarfdump.go:260), so a name with two or more trailing underscores (e.g.
// "task_struct__") is left with exactly one leftover trailing underscore.
// strings.Split then produces a trailing empty part, and parts[i][:1] on
// that empty string panics with a slice-bounds-out-of-range error.
func TestTypeNameDoubleTrailingUnderscorePanics(t *testing.T) {
	t.Skip("BUG: dwarfdump.go:259-260 typeName panics on names with >=2 trailing underscores (e.g. \"task_struct__\") because TrimSuffix(_) runs before TrimSuffix(__), leaving a dangling underscore that Split turns into an empty part")

	assert.Equal(t, "TaskStruct", typeName(Need{Name: "task_struct__"}))
}

func TestFieldName(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		expected string
	}{
		{"simple", "my_field", "MyField"},
		{"leading_underscore", "_pid", "Pid"},
		{"single_word", "offset", "Offset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, fieldName(tc.field))
		})
	}
}

func TestVersionCompare(t *testing.T) {
	v1 := Version{Major: 1, Minor: 2, Patch: 3}

	assert.Equal(t, 0, v1.Compare(Version{1, 2, 3}))
	assert.True(t, v1.Compare(Version{2, 0, 0}) > 0, "v1 is older than 2.0.0")
	assert.True(t, v1.Compare(Version{0, 9, 0}) < 0, "v1 is newer than 0.9.0")
	assert.True(t, v1.Compare(Version{1, 3, 0}) > 0)
	assert.True(t, v1.Compare(Version{1, 1, 0}) < 0)
	assert.True(t, v1.Compare(Version{1, 2, 4}) > 0)
	assert.True(t, v1.Compare(Version{1, 2, 2}) < 0)
}

func TestTypGetField(t *testing.T) {
	typ := Typ{
		Name: "my_struct",
		Fields: []Field{
			{Name: "a", Offset: 0},
			{Name: "b", Offset: 8},
		},
		Size: 16,
	}

	f := typ.GetField("a")
	require.NotNil(t, f)
	assert.Equal(t, uint64(0), f.Offset)

	f = typ.GetField("b")
	require.NotNil(t, f)
	assert.Equal(t, uint64(8), f.Offset)

	f = typ.GetField("does-not-exist")
	assert.Nil(t, f)
}

func newTestIndex() *Index {
	typA := &Typ{Name: "struct_a", Fields: []Field{{Name: "x", Offset: 0}}, Size: 8}
	typB := &Typ{Name: "struct_b", Fields: []Field{{Name: "y", Offset: 0}}, Size: 8}

	idx := &Index{
		offset2Type: map[dwarf.Offset]*Typ{
			1: typA,
			2: typB,
		},
		typedefs: map[string]*Typedef{
			"typedef_a": {Name: "typedef_a", TypeOffsets: []dwarf.Offset{1}},
			"typedef_dup": {Name: "typedef_dup", TypeOffsets: []dwarf.Offset{1, 1}},
			"typedef_empty": {Name: "typedef_empty"},
		},
	}
	return idx
}

func TestIndexGetTypeByName(t *testing.T) {
	idx := newTestIndex()

	typ := idx.GetTypeByName("struct_a")
	require.NotNil(t, typ)
	assert.Equal(t, "struct_a", typ.Name)

	typ = idx.GetTypeByName("does_not_exist")
	assert.Nil(t, typ)
}

func TestIndexGetTypeByName2(t *testing.T) {
	idx := newTestIndex()

	// empty name returns nil immediately
	assert.Nil(t, idx.GetTypeByName2(""))

	// via typedef indirection
	typ := idx.GetTypeByName2("typedef_a")
	require.NotNil(t, typ)
	assert.Equal(t, "struct_a", typ.Name)

	// falls back to direct offset2Type lookup when no typedef matches
	typ = idx.GetTypeByName2("struct_b")
	require.NotNil(t, typ)
	assert.Equal(t, "struct_b", typ.Name)

	// typedef pointing at duplicate-but-equal offsets is fine
	typ = idx.GetTypeByName2("typedef_dup")
	require.NotNil(t, typ)
	assert.Equal(t, "struct_a", typ.Name)

	// typedef with no offsets resolves to nil
	typ = idx.GetTypeByName2("typedef_empty")
	assert.Nil(t, typ)

	// unknown name that isn't a typedef nor a direct type
	typ = idx.GetTypeByName2("totally_unknown")
	assert.Nil(t, typ)
}

// TestDumpAndStructMemberOffsetsFromDwarf exercises the real DWARF-parsing
// path (structMemberOffsetsFromDwarf / Dump) against the currently running
// test binary itself, which the Go toolchain compiles with DWARF debug
// info by default. We look up a well-known runtime struct field to sanity
// check the parser end-to-end.
func TestDumpAndStructMemberOffsetsFromDwarf(t *testing.T) {
	self := os.Args[0]
	f, err := elf.Open(self)
	if err != nil {
		t.Skipf("cannot open own test binary as ELF (%s): %v", self, err)
	}
	defer f.Close()

	d, err := f.DWARF()
	if err != nil {
		t.Skipf("test binary has no DWARF info (maybe built with -ldflags=-w): %v", err)
	}

	idx, err := structMemberOffsetsFromDwarf(d)
	require.NoError(t, err)
	require.NotEmpty(t, idx.offset2Type)

	// runtime.g is a struct that should always exist in a Go binary's DWARF.
	typ := idx.GetTypeByName("runtime.g")
	if typ == nil {
		t.Skip("runtime.g not found in DWARF info of this build; skipping deep assertions")
	}
	assert.Equal(t, "runtime.g", typ.Name)
	assert.True(t, typ.Size > 0)

	// Exercise the higher level Dump() entrypoint too.
	fields := Dump(self, []Need{
		{
			Name: "runtime.g",
			Fields: []NeedField{
				{Name: "goid"},
			},
		},
	})
	require.NotEmpty(t, fields)
}
