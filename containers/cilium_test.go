// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package containers

import (
	"testing"

	"github.com/cilium/cilium/pkg/maps/lbmap"
	"github.com/stretchr/testify/assert"
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
