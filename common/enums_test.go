// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package common

import (
	"testing"
)

// TestServiceFlagStringer tests the stringized output for service flag
func TestServiceFlagStringer(t *testing.T) {
	tests := []struct {
		in   ServiceFlag
		want string
	}{
		{0, "0x0"},
		{SFNodeNetwork, "SFNodeNetwork"},
		{SFNodeBloom, "SFNodeBloom"},
		{SFNodeCF, "SFNodeCF"},
		{0xffffffff, "SFNodeNetwork|SFNodeBloom|SFNodeCF|0xfffffff8"},
	}

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		result := test.in.String()
		if result != test.want {
			t.Errorf("String #%d\n got: %s want: %s", i, result,
				test.want)
			continue
		}
	}
}

// TestAsimovNetStringer tests the stringized output for asimov net
func TestAsimovNetStringer(t *testing.T) {
	tests := []struct {
		in   AsimovNet
		want string
	}{
		{MainNet, "mainnet"},
		{DevelopNet, "devnet"},
		{TestNet, "testnet"},
		{0xffffffff, "Unknown AsimovNet (4294967295)"},
	}

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		result := test.in.String()
		if result != test.want {
			t.Errorf("String #%d\n got: %s want: %s", i, result,
				test.want)
			continue
		}
	}
}
