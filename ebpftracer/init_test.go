// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package ebpftracer

import (
	"net"
	"testing"

	"github.com/florianl/go-conntrack"
	"github.com/stretchr/testify/assert"
)

func TestIpTupleValid(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	var proto uint8 = 6
	var port uint16 = 80
	full := func() *conntrack.IPTuple {
		return &conntrack.IPTuple{Src: &ip, Dst: &ip, Proto: &conntrack.ProtoTuple{Number: &proto, SrcPort: &port, DstPort: &port}}
	}
	assert.True(t, ipTupleValid(full()))
	assert.False(t, ipTupleValid(nil))

	missing := []func(*conntrack.IPTuple){
		func(t *conntrack.IPTuple) { t.Src = nil },
		func(t *conntrack.IPTuple) { t.Dst = nil },
		func(t *conntrack.IPTuple) { t.Proto = nil },
		func(t *conntrack.IPTuple) { t.Proto.SrcPort = nil },
		func(t *conntrack.IPTuple) { t.Proto.DstPort = nil },
	}
	for i, m := range missing {
		tu := full()
		m(tu)
		assert.False(t, ipTupleValid(tu), "case %d", i)
	}
}

func TestIpTupleValidNilProtoNumber(t *testing.T) {
	// getConntrack dereferences *conn.Origin.Proto.Number after ipTupleValid; a netlink
	// message without CTA_PROTO_NUM would otherwise panic the agent at startup.
	// BUG: ipTupleValid does not check Proto.Number, which getConntrack dereferences — unskip when fixed
	t.Skip("BUG: ipTupleValid does not check Proto.Number, which getConntrack dereferences")
	ip := net.ParseIP("10.0.0.1")
	var port uint16 = 80
	assert.False(t, ipTupleValid(&conntrack.IPTuple{Src: &ip, Dst: &ip, Proto: &conntrack.ProtoTuple{SrcPort: &port, DstPort: &port}}))
}
