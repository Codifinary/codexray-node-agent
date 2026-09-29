// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"inet.af/netaddr"
)

func TestNetDeviceFilter(t *testing.T) {
	assert.True(t, netDeviceFilter("eth0"))
	assert.True(t, netDeviceFilter("eth0@if699"))
	assert.True(t, netDeviceFilter("enp2s0"))
	assert.True(t, netDeviceFilter("bond0"))
	assert.True(t, netDeviceFilter("ens1"))
	assert.True(t, netDeviceFilter("p1p1"))
	assert.True(t, netDeviceFilter("eno2"))
	assert.True(t, netDeviceFilter("em1"))
	assert.True(t, netDeviceFilter("enx78e7d1ea46da"))
	assert.True(t, netDeviceFilter("enP4p65s0"))
	assert.True(t, netDeviceFilter("enP2p33s0"))
	assert.True(t, netDeviceFilter("enX0"))

	assert.False(t, netDeviceFilter("dummy0"))
	assert.False(t, netDeviceFilter("docker0"))
	assert.False(t, netDeviceFilter("kube-ipvs0"))
	assert.False(t, netDeviceFilter("veth1b0c947@if2"))
	assert.False(t, netDeviceFilter("flannel.1"))
	assert.False(t, netDeviceFilter("cni0"))
	assert.False(t, netDeviceFilter("lxc00aa@if698"))
}

// netTestHandle is an in-memory netlinkHandle.
type netTestHandle struct {
	links    []netlink.Link
	addrs    map[string][]netlink.Addr
	linkErr  error
	addrErr  error
	families []int
	deleted  bool
}

func (h *netTestHandle) LinkList() ([]netlink.Link, error) { return h.links, h.linkErr }

func (h *netTestHandle) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	h.families = append(h.families, family)
	if h.addrErr != nil {
		return nil, h.addrErr
	}
	return h.addrs[link.Attrs().Name], nil
}

func (h *netTestHandle) Delete() { h.deleted = true }

func netTestInstall(t *testing.T, h netlinkHandle, err error) {
	t.Helper()
	orig := newHostNetlinkHandle
	newHostNetlinkHandle = func() (netlinkHandle, error) { return h, err }
	t.Cleanup(func() { newHostNetlinkHandle = orig })
}

func netTestLink(name string, state netlink.LinkOperState, rxB, txB, rxP, txP uint64) netlink.Link {
	return &netlink.Device{LinkAttrs: netlink.LinkAttrs{
		Name:       name,
		OperState:  state,
		Statistics: &netlink.LinkStatistics{RxBytes: rxB, TxBytes: txB, RxPackets: rxP, TxPackets: txP},
	}}
}

// netTestAddr keeps the host address (as the kernel reports it), not the network address.
func netTestAddr(t *testing.T, cidr string) netlink.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	n.IP = ip
	return netlink.Addr{IPNet: n}
}

func TestNetDevices(t *testing.T) {
	h := &netTestHandle{
		links: []netlink.Link{
			netTestLink("lo", netlink.OperUnknown, 1, 1, 1, 1),
			netTestLink("eth0", netlink.OperUp, 1000, 2000, 10, 20),
			netTestLink("docker0", netlink.OperUp, 5, 5, 5, 5),
			netTestLink("veth1b0c947", netlink.OperUp, 5, 5, 5, 5),
			netTestLink("enp2s0", netlink.OperDown, 0, 0, 0, 0),
			netTestLink("bond0", netlink.OperUnknown, 7, 8, 9, 10),
		},
		addrs: map[string][]netlink.Addr{
			"lo":      {netTestAddr(t, "127.0.0.1/8")},
			"docker0": {netTestAddr(t, "172.17.0.1/16")},
			"eth0": {
				netTestAddr(t, "10.0.0.5/24"),
				netTestAddr(t, "169.254.10.1/16"), // link-local unicast
				netTestAddr(t, "fe80::1/64"),      // link-local unicast
				netTestAddr(t, "224.0.0.251/32"),  // multicast
				netTestAddr(t, "ff02::1/128"),     // link-local multicast
				netTestAddr(t, "2001:db8::5/64"),
				{IPNet: &net.IPNet{IP: net.ParseIP("192.168.1.1")}}, // no mask: not a valid prefix
			},
		},
	}
	netTestInstall(t, h, nil)

	res, err := NetDevices()
	require.NoError(t, err)
	assert.Equal(t, []NetDeviceInfo{
		{
			Name:       "eth0",
			Up:         1,
			IPPrefixes: []netaddr.IPPrefix{netaddr.MustParseIPPrefix("10.0.0.5/24"), netaddr.MustParseIPPrefix("2001:db8::5/64")},
			RxBytes:    1000, TxBytes: 2000, RxPackets: 10, TxPackets: 20,
		},
		{Name: "enp2s0", Up: 0},
		{Name: "bond0", Up: 0, RxBytes: 7, TxBytes: 8, RxPackets: 9, TxPackets: 10},
	}, res)
	assert.Equal(t, []int{unix.AF_UNSPEC, unix.AF_UNSPEC, unix.AF_UNSPEC}, h.families, "addresses are listed only for matching devices, all families")
	assert.True(t, h.deleted)
}

func TestNetDevicesErrors(t *testing.T) {
	t.Run("handle", func(t *testing.T) {
		netTestInstall(t, nil, errors.New("permission denied"))
		res, err := NetDevices()
		assert.EqualError(t, err, "permission denied")
		assert.Nil(t, res)
	})
	t.Run("link list", func(t *testing.T) {
		h := &netTestHandle{linkErr: errors.New("dump interrupted")}
		netTestInstall(t, h, nil)
		res, err := NetDevices()
		assert.EqualError(t, err, "dump interrupted")
		assert.Nil(t, res)
		assert.True(t, h.deleted)
	})
	t.Run("addr list", func(t *testing.T) {
		h := &netTestHandle{
			links:   []netlink.Link{netTestLink("eth0", netlink.OperUp, 0, 0, 0, 0)},
			addrErr: errors.New("no such device"),
		}
		netTestInstall(t, h, nil)
		res, err := NetDevices()
		assert.EqualError(t, err, "no such device")
		assert.Nil(t, res)
		assert.True(t, h.deleted)
	})
	t.Run("no links", func(t *testing.T) {
		netTestInstall(t, &netTestHandle{}, nil)
		res, err := NetDevices()
		assert.NoError(t, err)
		assert.Empty(t, res)
	})
}

func TestNetDevicesRealNetlink(t *testing.T) {
	// Reading links/addresses of the test's own netns needs no privileges.
	orig := newHostNetlinkHandle
	t.Cleanup(func() { newHostNetlinkHandle = orig })
	newHostNetlinkHandle = func() (netlinkHandle, error) {
		h, err := netlink.NewHandle()
		if err != nil {
			return nil, err
		}
		return h, nil
	}
	res, err := NetDevices()
	require.NoError(t, err)
	links, err := netlink.LinkList()
	require.NoError(t, err)
	var want []string
	for _, l := range links {
		if netDeviceFilter(l.Attrs().Name) {
			want = append(want, l.Attrs().Name)
		}
	}
	var got []string
	for _, d := range res {
		got = append(got, d.Name)
		assert.Contains(t, []float64{0, 1}, d.Up)
	}
	assert.Equal(t, want, got)
}

func TestNetDevicesDefaultHandle(t *testing.T) {
	// Without root /proc/1/ns/net is not accessible and the default handle
	// fails; as root it succeeds. Either way only filtered devices come back.
	res, err := NetDevices()
	if err != nil {
		assert.Nil(t, res)
		return
	}
	for _, d := range res {
		assert.True(t, netDeviceFilter(d.Name), d.Name)
	}
}
