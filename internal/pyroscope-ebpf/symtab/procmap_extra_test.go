package symtab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcMapPermissionsString(t *testing.T) {
	cases := []struct {
		perms    ProcMapPermissions
		expected string
	}{
		{ProcMapPermissions{}, "-----"},
		{ProcMapPermissions{Read: true, Write: true, Execute: true, Shared: true, Private: true}, "rwxsp"},
		{ProcMapPermissions{Read: true}, "r----"},
		{ProcMapPermissions{Execute: true, Private: true}, "--x-p"},
	}
	for _, c := range cases {
		require.Equal(t, c.expected, c.perms.String())
	}
}

func TestProcMapString(t *testing.T) {
	m := &ProcMap{
		StartAddr: 0x1000,
		EndAddr:   0x2000,
		Perms:     &ProcMapPermissions{Read: true, Execute: true, Private: true},
		Offset:    0x10,
		Dev:       mkdev(9, 0),
		Inode:     42,
		Pathname:  "/bin/true",
	}
	s := m.String()
	require.Contains(t, s, "1000-2000")
	require.Contains(t, s, "r-x-p")
	require.Contains(t, s, "/bin/true")
}

func TestProcMapsString(t *testing.T) {
	maps := ProcMaps{
		{
			StartAddr: 0x1000,
			EndAddr:   0x2000,
			Perms:     &ProcMapPermissions{Read: true},
			Pathname:  "/bin/a",
		},
		{
			StartAddr: 0x3000,
			EndAddr:   0x4000,
			Perms:     &ProcMapPermissions{Write: true},
			Pathname:  "/bin/b",
		},
	}
	s := maps.String()
	require.Contains(t, s, "/bin/a")
	require.Contains(t, s, "/bin/b")
	require.Equal(t, 2, len(splitLines(s)))
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			if i > start {
				lines = append(lines, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestFindLastRXMap(t *testing.T) {
	maps := []*ProcMap{
		{Perms: &ProcMapPermissions{Read: true, Execute: true}, Pathname: "a"},
		{Perms: &ProcMapPermissions{Read: true}, Pathname: "b"},
		{Perms: &ProcMapPermissions{Read: true, Execute: true}, Pathname: "c"},
	}
	m := FindLastRXMap(maps)
	require.NotNil(t, m)
	require.Equal(t, "c", m.Pathname)

	require.Nil(t, FindLastRXMap(nil))
	require.Nil(t, FindLastRXMap([]*ProcMap{{Perms: &ProcMapPermissions{Write: true}}}))
}

func TestFindReadableMap(t *testing.T) {
	maps := []*ProcMap{
		{Perms: &ProcMapPermissions{Read: true, Write: true}, Pathname: "rw"},
		{Perms: &ProcMapPermissions{Read: true}, Pathname: "ro"},
		{Perms: &ProcMapPermissions{Read: true}, Pathname: "ro2"},
	}
	m := FindReadableMap(maps)
	require.NotNil(t, m)
	require.Equal(t, "ro", m.Pathname)

	require.Nil(t, FindReadableMap(nil))
	require.Nil(t, FindReadableMap([]*ProcMap{{Perms: &ProcMapPermissions{Write: true}}}))
}
