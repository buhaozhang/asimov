// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package chaincfg_test

import (
	"github.com/AsimovNetwork/asimov/common"
	"testing"

	. "github.com/AsimovNetwork/asimov/chaincfg"
)

// Define some of the required parameters for a user-registered
// network.  This is necessary to test the registration of and
// lookup of encoding magics from the network.
var mockNetParams = Params{
	Net:              1<<32 - 1,
}

func TestRegister(t *testing.T) {
	type registerTest struct {
		name   string
		params *Params
		err    error
	}
	type magicTest struct {
		magic byte
		valid bool
	}

	tests := []struct {
		name           string
		register       []registerTest
		p2pkhMagics    []magicTest
		p2shMagics     []magicTest
	}{
		{
			name: "default networks",
			register: []registerTest{
				{
					name:   "duplicate mainnet",
					params: &MainNetParams,
					err:    ErrDuplicateNet,
				},
				{
					name:   "develop net",
					params: &DevelopNetParams,
					err:    ErrDuplicateNet,
				},
				{
					name:   "duplicate testnet",
					params: &TestNetParams,
					err:    ErrDuplicateNet,
				},
			},
			p2pkhMagics: []magicTest{
				{
					magic: common.PubKeyHashAddrID,
					valid: true,
				},
				{
					magic: 0xFF,
					valid: false,
				},
			},
			p2shMagics: []magicTest{
				{
					magic: common.ScriptHashAddrID,
					valid: true,
				},
				{
					magic: 0xFF,
					valid: false,
				},
			},
		},
		{
			name: "register mocknet",
			register: []registerTest{
				{
					name:   "mocknet",
					params: &mockNetParams,
					err:    nil,
				},
			},
			p2pkhMagics: []magicTest{
				{
					magic: common.PubKeyHashAddrID,
					valid: true,
				},
				{
					magic: 0xFF,
					valid: false,
				},
			},
			p2shMagics: []magicTest{
				{
					magic: common.ScriptHashAddrID,
					valid: true,
				},
				{
					magic: 0xFF,
					valid: false,
				},
			},
		},
		{
			name: "more duplicates",
			register: []registerTest{
				{
					name:   "duplicate mainnet",
					params: &MainNetParams,
					err:    ErrDuplicateNet,
				},
				{
					name:   "duplicate devtest",
					params: &DevelopNetParams,
					err:    ErrDuplicateNet,
				},
				{
					name:   "duplicate testnet",
					params: &TestNetParams,
					err:    ErrDuplicateNet,
				},
				{
					name:   "duplicate mocknet",
					params: &mockNetParams,
					err:    ErrDuplicateNet,
				},
			},
			p2pkhMagics: []magicTest{
				{
					magic: common.PubKeyHashAddrID,
					valid: true,
				},
				{
					magic: 0xFF,
					valid: false,
				},
			},
			p2shMagics: []magicTest{
				{
					magic: common.ScriptHashAddrID,
					valid: true,
				},
				{
					magic: 0xFF,
					valid: false,
				},
			},
		},
	}

	for _, test := range tests {
		for _, regTest := range test.register {
			err := Register(regTest.params)
			if err != regTest.err {
				t.Errorf("%s:%s: Registered network with unexpected error: got %v expected %v",
					test.name, regTest.name, err, regTest.err)
			}
		}
		for i, magTest := range test.p2pkhMagics {
			valid := common.PubKeyHashAddrID == magTest.magic
			if valid != magTest.valid {
				t.Errorf("%s: P2PKH magic %d valid mismatch: got %v expected %v",
					test.name, i, valid, magTest.valid)
			}
		}
		for i, magTest := range test.p2shMagics {
			valid := common.ScriptHashAddrID == magTest.magic
			if valid != magTest.valid {
				t.Errorf("%s: P2SH magic %d valid mismatch: got %v expected %v",
					test.name, i, valid, magTest.valid)
			}
		}
	}
}
