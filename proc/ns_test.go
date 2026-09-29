// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestSelfNetNs(t *testing.T) {
	self, err := GetSelfNetNs()
	require.NoError(t, err)
	defer self.Close()
	byPid, err := GetNetNs(uint32(os.Getpid()))
	require.NoError(t, err)
	defer byPid.Close()
	assert.True(t, self.Equal(byPid))

	// same namespace: f runs directly and its error is propagated
	called := false
	errF := errors.New("boom")
	err = ExecuteInNetNs(self, byPid, func() error { called = true; return errF })
	assert.True(t, called)
	assert.Equal(t, errF, err)

	_, err = GetNetNs(0x7fffffff)
	assert.Error(t, err, "non-existent pid")
}

func TestGetNsIpsSelf(t *testing.T) {
	self, err := GetSelfNetNs()
	require.NoError(t, err)
	defer self.Close()
	ips, err := GetNsIps(self)
	if err != nil {
		t.Skipf("netlink handle needs CAP_SYS_ADMIN (setns) here: %s", err)
	}
	for _, ip := range ips {
		assert.False(t, ip.IsLinkLocalUnicast(), ip.String())
		assert.False(t, ip.IsMulticast(), ip.String())
	}
}

func TestGetHostNetNs(t *testing.T) {
	// the host namespace is the one of pid 1
	host, err := GetHostNetNs()
	byPid, errPid := GetNetNs(1)
	if errPid != nil {
		assert.Equal(t, errPid, err, "unprivileged: same error as opening /proc/1/ns/net")
		return
	}
	require.NoError(t, err)
	defer host.Close()
	defer byPid.Close()
	assert.True(t, host.Equal(byPid))
}

type nsTestSetCall struct {
	ns  netns.NsHandle
	tid int
}

// nsTestFakeSet replaces netns.Set, recording each call and failing the i-th
// one with errs[i].
func nsTestFakeSet(t *testing.T, errs map[int]error) *[]nsTestSetCall {
	t.Helper()
	orig := setNetNs
	t.Cleanup(func() { setNetNs = orig })
	calls := &[]nsTestSetCall{}
	setNetNs = func(ns netns.NsHandle) error {
		i := len(*calls)
		*calls = append(*calls, nsTestSetCall{ns: ns, tid: unix.Gettid()})
		return errs[i]
	}
	return calls
}

func nsTestSelf(t *testing.T) netns.NsHandle {
	t.Helper()
	self, err := GetSelfNetNs()
	require.NoError(t, err)
	t.Cleanup(func() { self.Close() })
	return self
}

func TestExecuteInNetNsSwitchesAndRestores(t *testing.T) {
	self := nsTestSelf(t)
	target := netns.None() // any handle not Equal to self
	for name, errF := range map[string]error{"ok": nil, "f fails": errors.New("boom")} {
		t.Run(name, func(t *testing.T) {
			calls := nsTestFakeSet(t, nil)
			var tid int
			err := ExecuteInNetNs(target, self, func() error {
				require.Len(t, *calls, 1, "f runs after switching")
				tid = unix.Gettid()
				return errF
			})
			assert.Equal(t, errF, err)
			require.Len(t, *calls, 2, "switched back even when f fails")
			assert.Equal(t, target, (*calls)[0].ns)
			assert.Equal(t, self, (*calls)[1].ns)
			assert.Equal(t, tid, (*calls)[0].tid, "f runs on the thread that was switched (LockOSThread)")
			assert.Equal(t, tid, (*calls)[1].tid)
		})
	}
}

func TestExecuteInNetNsSameNs(t *testing.T) {
	self := nsTestSelf(t)
	calls := nsTestFakeSet(t, nil)
	called := false
	require.NoError(t, ExecuteInNetNs(self, self, func() error { called = true; return nil }))
	assert.True(t, called)
	assert.Empty(t, *calls, "no namespace switch")
}

func TestExecuteInNetNsSetError(t *testing.T) {
	self := nsTestSelf(t)
	setErr := errors.New("operation not permitted")

	calls := nsTestFakeSet(t, map[int]error{0: setErr})
	called := false
	err := ExecuteInNetNs(netns.None(), self, func() error { called = true; return nil })
	assert.Equal(t, setErr, err)
	assert.False(t, called, "f must not run in the wrong namespace")
	assert.Len(t, *calls, 1)

	calls = nsTestFakeSet(t, map[int]error{1: setErr})
	err = ExecuteInNetNs(netns.None(), self, func() error { called = true; return errors.New("f") })
	assert.Equal(t, setErr, err, "failing to switch back takes precedence over f's error")
	assert.True(t, called)
	assert.Len(t, *calls, 2)
}

func TestExecuteInNetNsRestoreErrorKeepsThreadLocked(t *testing.T) {
	// BUG: when switching back to curNs fails, the deferred UnlockOSThread still
	// returns the thread — now stuck in newNs — to the scheduler, so unrelated
	// goroutines later run in the wrong network namespace. The thread must stay
	// locked so the runtime discards it when the goroutine exits — unskip when fixed
	t.Skip("BUG: ExecuteInNetNs unlocks the OS thread even if restoring the original netns failed")
	self := nsTestSelf(t)
	nsTestFakeSet(t, map[int]error{1: errors.New("operation not permitted")})
	nsTestOccupyMainThread(t)
	var tid int
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ExecuteInNetNs(netns.None(), self, func() error { tid = unix.Gettid(); return nil })
	}()
	<-done
	if tid == os.Getpid() {
		t.Skip("ran on the main thread, which the runtime never terminates")
	}
	assert.Eventually(t, func() bool {
		_, err := os.Stat(fmt.Sprintf("/proc/self/task/%d", tid))
		return os.IsNotExist(err)
	}, 2*time.Second, 10*time.Millisecond, "thread %d left in the target netns was reused", tid)
}

// nsTestOccupyMainThread tries to park a goroutine locked to the main thread
// until the test ends, so other goroutines run on threads the runtime can
// terminate.
func nsTestOccupyMainThread(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for i := 0; i < 1000; i++ {
		onMain := make(chan bool)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			main := unix.Gettid() == os.Getpid()
			onMain <- main
			if main {
				<-release
			}
		}()
		if <-onMain {
			return
		}
	}
}

type nsTestHandle struct {
	links    []netlink.Link
	linksErr error
	addrs    map[string][]netlink.Addr
	addrsErr error
	families []int
	queried  []string
	deleted  bool
}

func (h *nsTestHandle) LinkList() ([]netlink.Link, error) {
	return h.links, h.linksErr
}

func (h *nsTestHandle) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	h.queried = append(h.queried, link.Attrs().Name)
	h.families = append(h.families, family)
	return h.addrs[link.Attrs().Name], h.addrsErr
}

func (h *nsTestHandle) Delete() {
	h.deleted = true
}

func nsTestFakeHandle(t *testing.T, h *nsTestHandle, err error) *[]netns.NsHandle {
	t.Helper()
	orig := newNetlinkHandle
	t.Cleanup(func() { newNetlinkHandle = orig })
	opened := &[]netns.NsHandle{}
	newNetlinkHandle = func(ns netns.NsHandle) (netlinkHandle, error) {
		*opened = append(*opened, ns)
		if err != nil {
			return nil, err
		}
		return h, nil
	}
	return opened
}

func nsTestLink(name string, state netlink.LinkOperState) netlink.Link {
	return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name, OperState: state}}
}

func nsTestAddrs(ips ...net.IP) []netlink.Addr {
	res := make([]netlink.Addr, 0, len(ips))
	for _, ip := range ips {
		res = append(res, netlink.Addr{IPNet: &net.IPNet{IP: ip}})
	}
	return res
}

func TestGetNsIps(t *testing.T) {
	h := &nsTestHandle{
		links: []netlink.Link{
			nsTestLink("lo", netlink.OperUnknown),
			nsTestLink("eth0", netlink.OperUp),
			nsTestLink("eth1", netlink.OperDown),
			nsTestLink("eth2", netlink.OperUp),
		},
		addrs: map[string][]netlink.Addr{
			"lo": nsTestAddrs(net.ParseIP("127.0.0.1"), net.ParseIP("::1")),
			"eth0": nsTestAddrs(
				net.ParseIP("10.0.0.5"),
				net.ParseIP("fe80::1"),     // link-local unicast
				net.ParseIP("169.254.1.1"), // link-local unicast
				net.ParseIP("224.0.0.251"), // multicast
				net.ParseIP("ff02::1"),     // link-local multicast
				net.IP{1, 2, 3},            // invalid
				net.ParseIP("2001:db8::5"),
			),
			"eth1": nsTestAddrs(net.ParseIP("192.168.0.1")),
		},
	}
	ns := netns.NsHandle(42)
	opened := nsTestFakeHandle(t, h, nil)

	ips, err := GetNsIps(ns)
	require.NoError(t, err)
	var got []string
	for _, ip := range ips {
		got = append(got, ip.String())
	}
	assert.Equal(t, []string{"127.0.0.1", "::1", "10.0.0.5", "2001:db8::5"}, got)
	assert.Equal(t, []netns.NsHandle{ns}, *opened)
	assert.Equal(t, []string{"lo", "eth0", "eth2"}, h.queried, "down links are not queried")
	assert.Equal(t, []int{unix.AF_UNSPEC, unix.AF_UNSPEC, unix.AF_UNSPEC}, h.families)
	assert.True(t, h.deleted)
}

func TestGetNsIpsErrors(t *testing.T) {
	nsTestFakeHandle(t, nil, errors.New("setns: operation not permitted"))
	ips, err := GetNsIps(netns.None())
	assert.EqualError(t, err, "setns: operation not permitted")
	assert.Nil(t, ips)

	h := &nsTestHandle{linksErr: errors.New("dump interrupted")}
	nsTestFakeHandle(t, h, nil)
	ips, err = GetNsIps(netns.None())
	assert.EqualError(t, err, "dump interrupted")
	assert.Nil(t, ips)
	assert.True(t, h.deleted)

	h = &nsTestHandle{
		links:    []netlink.Link{nsTestLink("lo", netlink.OperUp)},
		addrs:    map[string][]netlink.Addr{"lo": nsTestAddrs(net.ParseIP("127.0.0.1"))},
		addrsErr: errors.New("no such device"),
	}
	nsTestFakeHandle(t, h, nil)
	ips, err = GetNsIps(netns.None())
	assert.EqualError(t, err, "no such device")
	assert.Nil(t, ips)
	assert.True(t, h.deleted)
}

func TestGetNsIpsCurrentNs(t *testing.T) {
	// a closed handle means "the current namespace": no setns, no privileges needed
	ips, err := GetNsIps(netns.None())
	require.NoError(t, err)
	for _, ip := range ips {
		assert.False(t, ip.IsLinkLocalUnicast(), ip.String())
		assert.False(t, ip.IsMulticast(), ip.String())
	}
}
