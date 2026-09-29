// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cilium/cilium/pkg/bpf"
	cmtypes "github.com/cilium/cilium/pkg/clustermesh/types"
	"github.com/cilium/cilium/pkg/defaults"
	"github.com/cilium/cilium/pkg/loadbalancer"
	"github.com/cilium/cilium/pkg/maps/ctmap"
	"github.com/cilium/cilium/pkg/maps/lbmap"
	"github.com/cilium/cilium/pkg/u8proto"
	"github.com/codifinary/codexray-node-agent/proc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func ciliumTestWithoutMaps(t *testing.T) {
	ct4, ct6, b4, b6 := ciliumCt4, ciliumCt6, backends4Map, backends6Map
	ciliumCt4, ciliumCt6, backends4Map, backends6Map = nil, nil, nil, nil
	t.Cleanup(func() { ciliumCt4, ciliumCt6, backends4Map, backends6Map = ct4, ct6, b4, b6 })
}

func TestLookupCiliumConntrackTableWithoutCilium(t *testing.T) {
	// On nodes without Cilium (maps not pinned) every lookup is a miss.
	ciliumTestWithoutMaps(t)
	cases := []struct{ src, dst netaddr.IPPort }{
		{netaddr.MustParseIPPort("10.0.0.5:40000"), netaddr.MustParseIPPort("10.96.0.10:53")},
		{netaddr.MustParseIPPort("[fd00::5]:40000"), netaddr.MustParseIPPort("[fd00:96::a]:443")},
		{netaddr.IPPort{}, netaddr.IPPort{}}, // zero value: neither v4 nor v6
	}
	for _, c := range cases {
		assert.Nil(t, lookupCiliumConntrackTable(c.src, c.dst), "%s -> %s", c.src, c.dst)
	}
	assert.Nil(t, lookupCilium4(cases[0].src, cases[0].dst))
	assert.Nil(t, lookupCilium6(cases[1].src, cases[1].dst))
}

func TestLookupCiliumPartialMaps(t *testing.T) {
	// Having only one of conntrack/backends maps must also be a miss (no nil deref).
	ciliumTestWithoutMaps(t)
	assert.Nil(t, lookupCilium4(netaddr.MustParseIPPort("10.0.0.5:40000"), netaddr.MustParseIPPort("10.96.0.10:53")))
	assert.Nil(t, lookupCilium6(netaddr.MustParseIPPort("[fd00::5]:40000"), netaddr.MustParseIPPort("[fd00:96::a]:443")))
}

func TestCiliumBackendMapDefinitions(t *testing.T) {
	// V2 backend maps carry the legacy value layout, V3 maps the V3 layout;
	// both are keyed by the V3 (32-bit backend id) key.
	cases := []struct {
		name  string
		key   any
		value any
	}{
		{lbmap.Backend4MapV2Name, &lbmap.Backend4KeyV3{}, &lbmap.Backend4Value{}},
		{lbmap.Backend4MapV3Name, &lbmap.Backend4KeyV3{}, &lbmap.Backend4ValueV3{}},
		{lbmap.Backend6MapV2Name, &lbmap.Backend6KeyV3{}, &lbmap.Backend6Value{}},
		{lbmap.Backend6MapV3Name, &lbmap.Backend6KeyV3{}, &lbmap.Backend6ValueV3{}},
	}
	assert.Len(t, ciliumMaps, len(cases))
	for _, c := range cases {
		def, ok := ciliumMaps[c.name]
		if assert.True(t, ok, c.name) {
			assert.IsType(t, c.key, def.key, c.name)
			assert.IsType(t, c.value, def.value, c.name)
		}
	}
}

func ciliumTestSaveSeams(t *testing.T) {
	ciliumTestWithoutMaps(t)
	open, lookup := ciliumOpenMap, ciliumLookup
	t.Cleanup(func() { ciliumOpenMap, ciliumLookup = open, lookup })
}

func TestCiliumInit(t *testing.T) {
	t.Run("no cilium", func(t *testing.T) {
		ciliumTestSaveSeams(t)
		var opened []string
		ciliumOpenMap = func(pinPath string, _ bpf.MapKey, _ bpf.MapValue) (*bpf.Map, error) {
			opened = append(opened, pinPath)
			return nil, errors.New("no such file or directory")
		}
		ciliumInit()
		assert.Nil(t, ciliumCt4)
		assert.Nil(t, ciliumCt6)
		assert.Nil(t, backends4Map)
		assert.Nil(t, backends6Map)
		// both backend map generations are probed, all pinned maps are looked up on the host
		require.Len(t, opened, 6)
		for _, p := range opened {
			assert.True(t, strings.HasPrefix(p, proc.HostPath(defaults.BPFFSRoot)), p)
		}
		assert.Equal(t, filepath.Base(opened[0]), ctmap.MapNameTCP4Global)
		assert.Equal(t, filepath.Base(opened[1]), ctmap.MapNameTCP6Global)
		assert.Equal(t, []string{lbmap.Backend4MapV2Name, lbmap.Backend4MapV3Name, lbmap.Backend6MapV2Name, lbmap.Backend6MapV3Name},
			[]string{filepath.Base(opened[2]), filepath.Base(opened[3]), filepath.Base(opened[4]), filepath.Base(opened[5])})
	})

	t.Run("cilium with V3 backend maps", func(t *testing.T) {
		ciliumTestSaveSeams(t)
		maps := map[string]*bpf.Map{}
		ciliumOpenMap = func(pinPath string, key bpf.MapKey, value bpf.MapValue) (*bpf.Map, error) {
			name := filepath.Base(pinPath)
			if name == lbmap.Backend4MapV2Name || name == lbmap.Backend6MapV2Name {
				return nil, errors.New("no such file or directory")
			}
			if def, ok := ciliumMaps[name]; ok {
				assert.IsType(t, def.key, key, name)
				assert.IsType(t, def.value, value, name)
			}
			m := &bpf.Map{}
			maps[name] = m
			return m, nil
		}
		ciliumInit()
		assert.Same(t, maps[ctmap.MapNameTCP4Global], ciliumCt4)
		assert.Same(t, maps[ctmap.MapNameTCP6Global], ciliumCt6)
		assert.Same(t, maps[lbmap.Backend4MapV3Name], backends4Map)
		assert.Same(t, maps[lbmap.Backend6MapV3Name], backends6Map)
	})

	t.Run("V2 backend maps win and stop probing", func(t *testing.T) {
		ciliumTestSaveSeams(t)
		var opened []string
		ciliumOpenMap = func(pinPath string, _ bpf.MapKey, _ bpf.MapValue) (*bpf.Map, error) {
			opened = append(opened, filepath.Base(pinPath))
			return &bpf.Map{}, nil
		}
		ciliumInit()
		assert.Equal(t, []string{ctmap.MapNameTCP4Global, ctmap.MapNameTCP6Global, lbmap.Backend4MapV2Name, lbmap.Backend6MapV2Name}, opened)
	})
}

// ciliumTestMaps installs fake conntrack/backend maps: ct maps to the given backend
// entry, the backend map holds backend (nil = miss).
func ciliumTestMaps(t *testing.T, ctErr error, ct bpf.MapValue, backend bpf.MapValue) *[]bpf.MapKey {
	t.Helper()
	ciliumTestSaveSeams(t)
	ciliumCt4, ciliumCt6, backends4Map, backends6Map = &bpf.Map{}, &bpf.Map{}, &bpf.Map{}, &bpf.Map{}
	var keys []bpf.MapKey
	ciliumLookup = func(m *bpf.Map, key bpf.MapKey) (bpf.MapValue, error) {
		keys = append(keys, key)
		switch m {
		case ciliumCt4, ciliumCt6:
			return ct, ctErr
		case backends4Map, backends6Map:
			return backend, nil
		}
		t.Fatalf("lookup in an unknown map")
		return nil, nil
	}
	return &keys
}

func TestLookupCilium4(t *testing.T) {
	src, dst := netaddr.MustParseIPPort("10.0.0.5:40000"), netaddr.MustParseIPPort("10.96.0.10:53")
	want := netaddr.MustParseIPPort("10.0.1.7:5353")
	v2, err := lbmap.NewBackend4Value(net.ParseIP("10.0.1.7"), 5353, u8proto.UDP, loadbalancer.BackendStateActive)
	require.NoError(t, err)
	v3, err := lbmap.NewBackend4ValueV3(cmtypes.MustParseAddrCluster("10.0.1.7"), 5353, u8proto.UDP, loadbalancer.BackendStateActive, 0)
	require.NoError(t, err)

	// the maps hold network byte order values
	for name, backend := range map[string]bpf.MapValue{"v2 value": v2.ToNetwork(), "v3 value": v3.ToNetwork()} {
		t.Run(name, func(t *testing.T) {
			keys := ciliumTestMaps(t, nil, &ctmap.CtEntry{BackendID: 17}, backend)
			got := lookupCiliumConntrackTable(src, dst)
			require.NotNil(t, got)
			assert.Equal(t, want, *got)

			require.Len(t, *keys, 2)
			// the service conntrack entry is keyed in reverse (cilium's tuple notation), network byte order
			ctKey, ok := (*keys)[0].(*ctmap.CtKey4Global)
			require.True(t, ok)
			host := ctKey.ToHost().(*ctmap.CtKey4Global)
			assert.Equal(t, src.IP().As4(), [4]byte(host.SourceAddr))
			assert.Equal(t, dst.IP().As4(), [4]byte(host.DestAddr))
			assert.Equal(t, dst.Port(), host.SourcePort)
			assert.Equal(t, src.Port(), host.DestPort)
			assert.Equal(t, u8proto.TCP, host.NextHeader)
			assert.Equal(t, uint8(ctmap.TUPLE_F_SERVICE), uint8(host.Flags))
			assert.Equal(t, lbmap.NewBackend4KeyV3(17), (*keys)[1])
		})
	}

	t.Run("misses", func(t *testing.T) {
		ciliumTestMaps(t, errors.New("key does not exist"), nil, nil)
		assert.Nil(t, lookupCilium4(src, dst), "no conntrack entry")
		ciliumTestMaps(t, nil, nil, nil)
		assert.Nil(t, lookupCilium4(src, dst), "nil conntrack entry")
		ciliumTestMaps(t, nil, &ctmap.CtEntry{BackendID: 17}, nil)
		assert.Nil(t, lookupCilium4(src, dst), "backend is gone")
		ciliumTestMaps(t, nil, &ctmap.CtEntry{BackendID: 17}, &lbmap.Backend6Value{})
		assert.Nil(t, lookupCilium4(src, dst), "unexpected backend value type")
	})
}

func TestLookupCilium6(t *testing.T) {
	src, dst := netaddr.MustParseIPPort("[fd00::5]:40000"), netaddr.MustParseIPPort("[fd00:96::a]:443")
	want := netaddr.MustParseIPPort("[fd00:1::7]:8443")
	v2, err := lbmap.NewBackend6Value(net.ParseIP("fd00:1::7"), 8443, u8proto.TCP, loadbalancer.BackendStateActive)
	require.NoError(t, err)
	v3, err := lbmap.NewBackend6ValueV3(cmtypes.MustParseAddrCluster("fd00:1::7"), 8443, u8proto.TCP, loadbalancer.BackendStateActive, 0)
	require.NoError(t, err)

	// the maps hold network byte order values
	for name, backend := range map[string]bpf.MapValue{"v2 value": v2.ToNetwork(), "v3 value": v3.ToNetwork()} {
		t.Run(name, func(t *testing.T) {
			keys := ciliumTestMaps(t, nil, &ctmap.CtEntry{BackendID: 9}, backend)
			got := lookupCiliumConntrackTable(src, dst)
			require.NotNil(t, got)
			assert.Equal(t, want, *got)

			require.Len(t, *keys, 2)
			ctKey, ok := (*keys)[0].(*ctmap.CtKey6Global)
			require.True(t, ok)
			host := ctKey.ToHost().(*ctmap.CtKey6Global)
			assert.Equal(t, src.IP().As16(), [16]byte(host.SourceAddr))
			assert.Equal(t, dst.IP().As16(), [16]byte(host.DestAddr))
			assert.Equal(t, dst.Port(), host.SourcePort)
			assert.Equal(t, src.Port(), host.DestPort)
			assert.Equal(t, lbmap.NewBackend6KeyV3(9), (*keys)[1])
		})
	}

	t.Run("misses", func(t *testing.T) {
		ciliumTestMaps(t, errors.New("key does not exist"), nil, nil)
		assert.Nil(t, lookupCilium6(src, dst), "no conntrack entry")
		ciliumTestMaps(t, nil, nil, nil)
		assert.Nil(t, lookupCilium6(src, dst), "nil conntrack entry")
		ciliumTestMaps(t, nil, &ctmap.CtEntry{BackendID: 9}, nil)
		assert.Nil(t, lookupCilium6(src, dst), "backend is gone")
		ciliumTestMaps(t, nil, &ctmap.CtEntry{BackendID: 9}, &lbmap.Backend4Value{})
		assert.Nil(t, lookupCilium6(src, dst), "unexpected backend value type")
	})
}
