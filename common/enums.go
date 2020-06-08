// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package common

import (
	"fmt"
	"strconv"
	"strings"
)

// AsimovNet represents which asimov network a message belongs to.
type AsimovNet uint32

// Constants used to indicate the message asimov network.  They can also be
// used to seek to the next message when a stream's state is unknown, but
// this package does not provide that functionality since it's generally a
// better idea to simply disconnect clients that are misbehaving over TCP.
const (
	// MainNet represents the main asimov network.
	MainNet AsimovNet = 0xd9b4bef9

	// DevelopNet represents the regression test network.
	DevelopNet AsimovNet = 0x39b187f3

	// TestNet represents the test network.
	TestNet AsimovNet = 0x0709110b
)

// bnStrings is a map of asimov networks back to their constant names for
// pretty printing.
var bnStrings = map[AsimovNet]string{
	MainNet:    "mainnet",
	DevelopNet: "devnet",
	TestNet:    "testnet",
}

// String returns the types.AsimovNet in human-readable form.
func (n AsimovNet) String() string {
	if s, ok := bnStrings[n]; ok {
		return s
	}

	return fmt.Sprintf("Unknown AsimovNet (%d)", uint32(n))
}

// BehaviorFlags is a bitmask defining tweaks to the normal behavior when
// performing chain processing and consensus rules checks.
type BehaviorFlags uint32

const (
	// BFFastAdd may be set to indicate that several checks can be avoided
	// for the block since it is already known to fit into the chain due to
	// already proving it correct links into the chain up to a known
	// state db.  This is primarily used for miner.
	BFFastAdd BehaviorFlags = 1 << iota

	// BFNone is a convenience value to specifically indicate no flags.
	BFNone BehaviorFlags = 0
)

// ServiceFlag identifies services supported by a bitcoin peer.
type ServiceFlag uint64

const (
	// SFNodeNetwork is a flag used to indicate a peer is a full node.
	SFNodeNetwork ServiceFlag = 1 << iota

	// SFNodeBloom is a flag used to indicate a peer supports bloom
	// filtering.
	SFNodeBloom

	// SFNodeCF is a flag used to indicate a peer supports committed
	// filters (CFs).
	SFNodeCF
)

// Map of service flags back to their constant names for pretty printing.
var sfStrings = map[ServiceFlag]string{
	SFNodeNetwork: "SFNodeNetwork",
	SFNodeBloom:   "SFNodeBloom",
	SFNodeCF:      "SFNodeCF",
}

// orderedSFStrings is an ordered list of service flags from highest to
// lowest.
var orderedSFStrings = []ServiceFlag{
	SFNodeNetwork,
	SFNodeBloom,
	SFNodeCF,
}

// String returns the ServiceFlag in human-readable form.
func (f ServiceFlag) String() string {
	// No flags are set.
	if f == 0 {
		return "0x0"
	}

	// Add individual bit flags.
	s := ""
	for _, flag := range orderedSFStrings {
		if f&flag == flag {
			s += sfStrings[flag] + "|"
			f -= flag
		}
	}

	// Add any remaining flags which aren't accounted for as hex.
	s = strings.TrimRight(s, "|")
	if f != 0 {
		s += "|0x" + strconv.FormatUint(uint64(f), 16)
	}
	s = strings.TrimLeft(s, "|")
	return s
}
