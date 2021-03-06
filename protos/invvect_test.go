// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package protos

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/AsimovNetwork/asimov/common"
)

// TestInvVectStringer tests the stringized output for inventory vector types.
func TestInvTypeStringer(t *testing.T) {
	tests := []struct {
		in   InvType
		want string
	}{
		{InvTypeError, "ERROR"},
		{InvTypeTx, "MSG_TX"},
		{InvTypeBlock, "MSG_BLOCK"},
		{InvTypeFilteredBlock,"MSG_FILTERED_BLOCK"},
		{InvTypeSignature,"MSG_SIGNATURE"},
		{0xffffffff, "Unknown InvType (4294967295)"},
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

// TestInvVect tests the InvVect API.
func TestInvVect(t *testing.T) {
	ivType := InvTypeBlock
	hash := common.Hash{}

	// Ensure we get the same payload and signature back out.
	iv := NewInvVect(ivType, &hash)
	if iv.Type != ivType {
		t.Errorf("NewInvVect: wrong type - got %v, want %v",
			iv.Type, ivType)
	}
	if !iv.Hash.IsEqual(&hash) {
		t.Errorf("NewInvVect: wrong hash - got %v, want %v",
			iv.Hash, hash)
	}

}

// TestInvVectWire tests the InvVect protos encode and decode for various
// protocol versions and supported inventory vector types.
func TestInvVectWire(t *testing.T) {
	// Block 203707 hash.
	hashStr := "3264bc2ac36a60840790ba1d475d01367e7c723da941069e9dc"
	baseHash, err :=  common.NewHashFromStr(hashStr)
	if err != nil {
		t.Errorf("NewHashFromStr: %v", err)
	}

	// errInvVect is an inventory vector with an error.
	errInvVect := InvVect{
		Type: InvTypeError,
		Hash: common.Hash{},
	}

	// errInvVectEncoded is the protos encoded bytes of errInvVect.
	errInvVectEncoded := []byte{
		0x00, 0x00, 0x00, 0x00, // InvTypeError
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // No hash
	}

	// txInvVect is an inventory vector representing a transaction.
	txInvVect := InvVect{
		Type: InvTypeTx,
		Hash: *baseHash,
	}

	// txInvVectEncoded is the protos encoded bytes of txInvVect.
	txInvVectEncoded := []byte{
		0x01, 0x00, 0x00, 0x00, // InvTypeTx
		0xdc, 0xe9, 0x69, 0x10, 0x94, 0xda, 0x23, 0xc7,
		0xe7, 0x67, 0x13, 0xd0, 0x75, 0xd4, 0xa1, 0x0b,
		0x79, 0x40, 0x08, 0xa6, 0x36, 0xac, 0xc2, 0x4b,
		0x26, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Block 203707 hash
	}

	// blockInvVect is an inventory vector representing a block.
	blockInvVect := InvVect{
		Type: InvTypeBlock,
		Hash: *baseHash,
	}

	// blockInvVectEncoded is the protos encoded bytes of blockInvVect.
	blockInvVectEncoded := []byte{
		0x02, 0x00, 0x00, 0x00, // InvTypeBlock
		0xdc, 0xe9, 0x69, 0x10, 0x94, 0xda, 0x23, 0xc7,
		0xe7, 0x67, 0x13, 0xd0, 0x75, 0xd4, 0xa1, 0x0b,
		0x79, 0x40, 0x08, 0xa6, 0x36, 0xac, 0xc2, 0x4b,
		0x26, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Block 203707 hash
	}

	//FilteredBlockVect is an inventory vector representing a filter block.
	filterBlockInvVect := InvVect{
			Type: InvTypeFilteredBlock,
			Hash: *baseHash,
	}

	// filterBlockInvVectEncoded is the protos encoded bytes of filterBlockInvVect.
	filterBlockInvVectEncoded := []byte{
		0x03, 0x00, 0x00, 0x00, // InvTypeFilterBlock
		0xdc, 0xe9, 0x69, 0x10, 0x94, 0xda, 0x23, 0xc7,
		0xe7, 0x67, 0x13, 0xd0, 0x75, 0xd4, 0xa1, 0x0b,
		0x79, 0x40, 0x08, 0xa6, 0x36, 0xac, 0xc2, 0x4b,
		0x26, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Block 203707 hash
	}


	//signatureVect is an inventory vector representing a signature.
	signatureInvVect := InvVect{
		Type: InvTypeSignature,
		Hash: *baseHash,
	}

	// filterBlockInvVectEncoded is the protos encoded bytes of filterBlockInvVect.
	signatureInvVectEncoded := []byte{
		0x04, 0x00, 0x00, 0x00, // InvTypeSignature
		0xdc, 0xe9, 0x69, 0x10, 0x94, 0xda, 0x23, 0xc7,
		0xe7, 0x67, 0x13, 0xd0, 0x75, 0xd4, 0xa1, 0x0b,
		0x79, 0x40, 0x08, 0xa6, 0x36, 0xac, 0xc2, 0x4b,
		0x26, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // Block 203707 hash
	}


	tests := []struct {
		in   InvVect // NetAddress to encode
		out  InvVect // Expected decoded NetAddress
		buf  []byte  // Wire encoding
	}{
		// Latest protocol version error inventory vector.
		{
			errInvVect,
			errInvVect,
			errInvVectEncoded,
		},

		// Latest protocol version tx inventory vector.
		{
			txInvVect,
			txInvVect,
			txInvVectEncoded,
		},

		// Latest protocol version block inventory vector.
		{
			blockInvVect,
			blockInvVect,
			blockInvVectEncoded,
		},
		// Latest protocol version block inventory vector.
		{
			filterBlockInvVect,
			filterBlockInvVect,
			filterBlockInvVectEncoded,
		},
		// Latest protocol version block inventory vector.
		{
			signatureInvVect,
			signatureInvVect,
			signatureInvVectEncoded,
		},
	}

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		// Encode to protos format.
		var buf bytes.Buffer
		err := writeInvVect(&buf, &test.in)
		if err != nil {
			t.Errorf("writeInvVect #%d error %v", i, err)
			continue
		}
		if !bytes.Equal(buf.Bytes(), test.buf) {
			t.Errorf("writeInvVect #%d\n got: %v want: %v", i,
				buf.Bytes(), test.buf)
			continue
		}

		// Decode the message from protos format.
		var iv InvVect
		rbuf := bytes.NewReader(test.buf)
		err = readInvVect(rbuf, &iv)
		if err != nil {
			t.Errorf("readInvVect #%d error %v", i, err)
			continue
		}
		if !reflect.DeepEqual(iv, test.out) {
			t.Errorf("readInvVect #%d\n got: %v want: %v", i,
				iv, test.out)
			continue
		}
	}
}
