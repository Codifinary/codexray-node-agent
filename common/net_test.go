// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"inet.af/netaddr"
)

func TestConnectionFilter(t *testing.T) {
	f := connectionFilter{whitelist: map[string]netaddr.IPPrefix{}}
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("127.0.0.1"), netaddr.MustParseIP("127.0.0.1")))
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("192.168.1.1"), netaddr.MustParseIP("127.0.0.1")))

	assert.True(t, f.ShouldBeSkipped(netaddr.MustParseIP("1.1.1.1"), netaddr.MustParseIP("2.2.2.2")))
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("1.1.1.1"), netaddr.MustParseIP("192.168.1.1")))
	// because the actual dest is allowed, the dest is added to whitelist
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("1.1.1.1"), netaddr.MustParseIP("2.2.2.2")))

	assert.True(t, f.ShouldBeSkipped(netaddr.MustParseIP("2.2.2.2"), netaddr.MustParseIP("2.2.2.2")))
	f.WhitelistPrefix(netaddr.MustParseIPPrefix("2.2.2.0/24"))
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("2.2.2.2"), netaddr.MustParseIP("2.2.2.2")))

	assert.True(t, f.ShouldBeSkipped(netaddr.MustParseIP("3.3.3.3"), netaddr.MustParseIP("3.3.3.3")))
	f.WhitelistPrefix(netaddr.MustParseIPPrefix("4.4.4.4/32"))
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("3.3.3.3"), netaddr.MustParseIP("4.4.4.4")))
}

func TestDestinationKey(t *testing.T) {
	d := netaddr.IPPortFrom(netaddr.MustParseIP("1.1.1.1"), 443)
	ad := netaddr.IPPortFrom(netaddr.MustParseIP("2.2.2.2"), 443)

	assert.Equal(t, "1.1.1.1:443 (2.2.2.2:443)", NewDestinationKey(d, ad, nil).String())

	assert.Equal(t,
		"aa.bb.s3.amazonaws.com:443 ()",
		NewDestinationKey(d, ad, &Domain{FQDN: "aa.bb.s3.amazonaws.com", SpecifyIP: false}).String(),
	)
	assert.Equal(t,
		"1.1.1.1:443 (2.2.2.2:443)",
		NewDestinationKey(d, ad, &Domain{FQDN: "aa.bb.s3.amazonaws.com", SpecifyIP: true}).String(),
	)
}

func TestDomain(t *testing.T) {
	assert.Equal(t, "Domain(fqdn,true)", NewDomain("fqdn", []netaddr.IP{netaddr.MustParseIP("127.0.0.1")}).String())
	assert.Equal(t, "Domain(fqdn,true)", NewDomain("fqdn", []netaddr.IP{netaddr.MustParseIP("192.168.1.1")}).String())
	assert.Equal(t, "Domain(fqdn,true)", NewDomain("fqdn", []netaddr.IP{
		netaddr.MustParseIP("1.1.1.1"),
		netaddr.MustParseIP("192.168.1.1"),
	}).String())
	assert.Equal(t, "Domain(fqdn,true)", NewDomain("fqdn", []netaddr.IP{
		netaddr.MustParseIP("1.1.1.1"),
	}).String())
	assert.Equal(t, "Domain(fqdn,false)", NewDomain("fqdn", []netaddr.IP{
		netaddr.MustParseIP("1.1.1.1"),
		netaddr.MustParseIP("1.1.1.2"),
	}).String())

}

func TestNormalizeFQDN(t *testing.T) {
	assert.Equal(t, "IP.in-addr.arpa", NormalizeFQDN("4.3.2.1.in-addr.arpa", "TypePTR"))
	assert.Equal(t, "codexray.io", NormalizeFQDN("codexray.io", "TypeA"))
	assert.Equal(t, "IP.ec2.internal", NormalizeFQDN("ip-172-1-2-3.ec2.internal", "TypeA"))
	assert.Equal(t, "IP.ec2", NormalizeFQDN("ip-172-1-2-3.ec2", "TypeA"))

	assert.Equal(t, "example.com", NormalizeFQDN("example.com", "TypeA"))
	assert.Equal(t, "example.com.search_path_suffix", NormalizeFQDN("example.com.cluster.local", "TypeA"))
	assert.Equal(t, "example.com.search_path_suffix", NormalizeFQDN("example.com.svc.cluster.local", "TypeA"))
	assert.Equal(t, "example.com.search_path_suffix", NormalizeFQDN("example.com.svc.default.cluster.local", "TypeA"))

	assert.Equal(t, "example.net.search_path_suffix", NormalizeFQDN("example.net.svc.default.cluster.local", "TypeA"))
	assert.Equal(t, "example.org.search_path_suffix", NormalizeFQDN("example.org.svc.default.cluster.local", "TypeA"))
	assert.Equal(t, "example.io.search_path_suffix", NormalizeFQDN("example.io.svc.default.cluster.local", "TypeA"))
}

func BenchmarkNormalizeFQDN(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NormalizeFQDN("ip-172-1-2-3.ec2.internal", "TypeA")
		NormalizeFQDN("example.io.svc.default.cluster.local", "TypeA")
	}
}

func TestIsIpPrivate(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":        true,
		"172.16.0.1":      true,
		"172.31.255.255":  true,
		"172.32.0.1":      false,
		"192.168.1.1":     true,
		"100.64.0.1":      true, // CGNAT 100.64.0.0/10
		"100.127.255.255": true,
		"100.63.255.255":  false,
		"100.128.0.0":     false,
		"8.8.8.8":         false,
		"127.0.0.1":       false, // loopback is not private
		"fd00::1":         true,  // ULA fc00::/7
		"fc00::1":         true,
		"2001:4860::8888": false,
		"::1":             false,
		"fe80::1":         false,
	}
	for ip, want := range cases {
		assert.Equal(t, want, IsIpPrivate(netaddr.MustParseIP(ip)), ip)
	}
}

func TestIsIpExternal(t *testing.T) {
	assert.True(t, IsIpExternal(netaddr.MustParseIP("8.8.8.8")))
	assert.True(t, IsIpExternal(netaddr.MustParseIP("2001:4860::8888")))
	assert.False(t, IsIpExternal(netaddr.MustParseIP("127.0.0.1")))
	assert.False(t, IsIpExternal(netaddr.MustParseIP("::1")))
	assert.False(t, IsIpExternal(netaddr.MustParseIP("10.1.2.3")))
	assert.False(t, IsIpExternal(netaddr.MustParseIP("100.100.1.1")))
}

func TestConnectionFilterLinkLocalAndIPv6(t *testing.T) {
	f := connectionFilter{whitelist: map[string]netaddr.IPPrefix{}}
	// link-local (e.g. cloud metadata 169.254.169.254) is always skipped
	assert.True(t, f.ShouldBeSkipped(netaddr.MustParseIP("169.254.169.254"), netaddr.MustParseIP("10.0.0.1")))
	assert.True(t, f.ShouldBeSkipped(netaddr.MustParseIP("fe80::1"), netaddr.MustParseIP("fe80::1")))

	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("fd00::1"), netaddr.MustParseIP("fd00::1")))
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("::1"), netaddr.MustParseIP("::1")))
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("100.64.1.1"), netaddr.MustParseIP("100.64.1.1")))

	pub6 := netaddr.MustParseIP("2001:db8::1")
	assert.True(t, f.ShouldBeSkipped(pub6, pub6))
	// NAT'ed to a private IPv6 address -> destination gets whitelisted as /128
	assert.False(t, f.ShouldBeSkipped(pub6, netaddr.MustParseIP("fd00::2")))
	assert.Contains(t, f.whitelist, "2001:db8::1/128")
	assert.False(t, f.ShouldBeSkipped(pub6, pub6))

	// WhitelistIP for IPv4 is a /32, and adding it twice is idempotent
	f.WhitelistIP(netaddr.MustParseIP("5.5.5.5"))
	f.WhitelistIP(netaddr.MustParseIP("5.5.5.5"))
	assert.Contains(t, f.whitelist, "5.5.5.5/32")
	assert.False(t, f.ShouldBeSkipped(netaddr.MustParseIP("5.5.5.5"), netaddr.MustParseIP("5.5.5.5")))
	assert.True(t, f.ShouldBeSkipped(netaddr.MustParseIP("5.5.5.6"), netaddr.MustParseIP("5.5.5.6")))
}

func TestPortFilter(t *testing.T) {
	var nilFilter *portFilter
	assert.False(t, nilFilter.ShouldBeSkipped(40000), "nil filter (no ephemeral range) skips nothing")

	f := &portFilter{from: 32768, to: 60999}
	assert.False(t, f.ShouldBeSkipped(80))
	assert.False(t, f.ShouldBeSkipped(32767))
	assert.True(t, f.ShouldBeSkipped(32768))
	assert.True(t, f.ShouldBeSkipped(45000))
	assert.True(t, f.ShouldBeSkipped(60999))
	assert.False(t, f.ShouldBeSkipped(61000))
}

func TestHttpFilter(t *testing.T) {
	f, err := newHttpFilter(nil)
	require.NoError(t, err)
	assert.False(t, f.ShouldBeSkipped("/health"))

	f, err = newHttpFilter([]string{"/health*", "/api/*/metrics", "/exact"})
	require.NoError(t, err)
	assert.True(t, f.ShouldBeSkipped("/health"))
	assert.True(t, f.ShouldBeSkipped("/healthz"))
	assert.True(t, f.ShouldBeSkipped("/api/v1/metrics"))
	assert.True(t, f.ShouldBeSkipped("/exact"))
	assert.False(t, f.ShouldBeSkipped("/exact/more"))
	assert.False(t, f.ShouldBeSkipped("/api/v1/users"))
	assert.False(t, f.ShouldBeSkipped(""))

	_, err = newHttpFilter([]string{"/bad[glob"})
	assert.Error(t, err)
}

func TestHostPort(t *testing.T) {
	hp := HostPortFromIPPort(netaddr.MustParseIPPort("10.0.0.1:5432"))
	assert.Equal(t, "10.0.0.1", hp.Host())
	assert.Equal(t, uint16(5432), hp.Port())
	assert.Equal(t, netaddr.MustParseIP("10.0.0.1"), hp.IP())
	assert.Equal(t, netaddr.MustParseIPPort("10.0.0.1:5432"), hp.IPPort())
	assert.Equal(t, "10.0.0.1:5432", hp.String())

	hp6 := HostPortFromIPPort(netaddr.MustParseIPPort("[2001:db8::1]:443"))
	assert.Equal(t, "[2001:db8::1]:443", hp6.String())

	named := HostPortWithEmptyIP("db.example.com", 3306)
	assert.Equal(t, "db.example.com", named.Host())
	assert.True(t, named.IP().IsZero())
	assert.Equal(t, "db.example.com:3306", named.String())

	// port 0 means unknown -> empty label value
	assert.Equal(t, "", HostPort{}.String())
	assert.Equal(t, "", HostPortWithEmptyIP("x", 0).String())
}

func TestDestinationKeyAccessors(t *testing.T) {
	d := netaddr.MustParseIPPort("10.96.0.10:53")
	ad := netaddr.MustParseIPPort("10.0.0.5:53")

	k := NewDestinationKey(d, ad, nil)
	assert.Equal(t, "10.96.0.10:53", k.DestinationLabelValue())
	assert.Equal(t, "10.0.0.5:53", k.ActualDestinationLabelValue())
	assert.Equal(t, d, k.Destination().IPPort())
	assert.Equal(t, ad, k.ActualDestination().IPPort())
	assert.Equal(t, ad, k.ActualDestinationIfKnown().IPPort())

	// no actual destination known (conntrack miss) -> fall back to destination
	k = NewDestinationKey(d, netaddr.IPPort{}, nil)
	assert.Equal(t, "", k.ActualDestinationLabelValue())
	assert.Equal(t, d, k.ActualDestinationIfKnown().IPPort())

	// external destination behind a multi-IP domain collapses to the FQDN only
	ext := netaddr.MustParseIPPort("52.1.1.1:443")
	k = NewDestinationKey(ext, ext, NewDomain("s3.amazonaws.com", []netaddr.IP{
		netaddr.MustParseIP("52.1.1.1"), netaddr.MustParseIP("52.1.1.2"),
	}))
	assert.Equal(t, "s3.amazonaws.com:443", k.DestinationLabelValue())
	assert.Equal(t, "", k.ActualDestinationLabelValue())
	assert.Equal(t, "s3.amazonaws.com:443", k.ActualDestinationIfKnown().String())

	// private actual destination keeps IPs even if the domain says otherwise
	k = NewDestinationKey(ext, ad, &Domain{FQDN: "x.example.com", SpecifyIP: false})
	assert.Equal(t, "52.1.1.1:443", k.DestinationLabelValue())
	assert.Equal(t, "10.0.0.5:53", k.ActualDestinationLabelValue())
}

func TestNewDomainEdgeCases(t *testing.T) {
	assert.True(t, NewDomain("x", nil).SpecifyIP)
	assert.False(t, NewDomain("x", []netaddr.IP{
		netaddr.MustParseIP("2001:db8::1"), netaddr.MustParseIP("2001:db8::2"),
	}).SpecifyIP)
	assert.True(t, NewDomain("x", []netaddr.IP{
		netaddr.MustParseIP("8.8.8.8"), netaddr.MustParseIP("100.64.0.1"),
	}).SpecifyIP)
}

func TestNormalizeFQDNEdgeCases(t *testing.T) {
	assert.Equal(t, "", NormalizeFQDN("", "TypeA"))
	assert.Equal(t, "IP.in-addr.arpa", NormalizeFQDN("", "TypePTR"))
	assert.Equal(t, "localhost", NormalizeFQDN("localhost", "TypeA"))
	// ip- prefix that is not an EC2 name is left alone
	assert.Equal(t, "ip-foo.example.org", NormalizeFQDN("ip-foo.example.org", "TypeAAAA"))
	assert.Equal(t, "ip-10-0-0-1", NormalizeFQDN("ip-10-0-0-1", "TypeA"))
	assert.Equal(t, "IP.ec2.internal", NormalizeFQDN("ip-10-0-0-1.ec2.internal", "TypeAAAA"))
	// a TLD-like label as the first label is not a search-path suffix
	assert.Equal(t, "com.example", NormalizeFQDN("com.example", "TypeA"))
	assert.Equal(t, "api.example.com", NormalizeFQDN("api.example.com", "TypeA"))
	assert.Equal(t, "api.example.com.search_path_suffix", NormalizeFQDN("api.example.com.default.svc.cluster.local", "TypeA"))
	// multibyte runes are preserved
	assert.Equal(t, "пример.рф", NormalizeFQDN("пример.рф", "TypeA"))
}
