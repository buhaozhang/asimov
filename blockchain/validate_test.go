// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"crypto/ecdsa"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestIsCoinBaseTx(t *testing.T) {
	//test multIn tx:
	multInTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	preOutNilTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	preOutHashTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{0x01,0x02,}, Index:protos.MaxPrevOutIndex,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	preOutIdxTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-3,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	coinbase := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	tests := []struct {
		tx     	*protos.MsgTx
		result 	bool
	} {
		{
			multInTx,
			false,
		},
		{
			preOutNilTx,
			false,
		},
		{
			preOutHashTx,
			false,
		},
		{
			preOutIdxTx,
			false,
		},
		{
			coinbase,
			true,
		},
	}

	t.Logf("Running %d TestIsCoinBaseTx tests", len(tests))
	for i, test := range tests {
		result := IsCoinBaseTx(test.tx)
		if result != test.result {
			t.Errorf("the %d test error: want: %v, but got: %v", i, test.result, result)
		}
	}
}


func TestIsTransferCreateOrMintTx(t *testing.T) {
	//test multIn tx:
	multInTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	preOutNilTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	preOutHashTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{0x01,0x02,}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	preOutIdxTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-3,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	transferCreationTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	transferMintTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	tests := []struct {
		tx     	*protos.MsgTx
		result 	bool
	} {
		{
			multInTx,
			false,
		},
		{
			preOutNilTx,
			false,
		},
		{
			preOutHashTx,
			false,
		},
		{
			preOutIdxTx,
			false,
		},
		{
			transferCreationTx,
			true,
		},
		{
			transferMintTx,
			true,
		},
	}

	t.Logf("Running %d TestIsTransferCreateOrMintTx tests", len(tests))
	for i, test := range tests {
		result := IsTransferCreateOrMintTx(test.tx)
		if result != test.result {
			t.Errorf("the %d test error: want: %v, but got: %v", i, test.result, result)
		}
	}
}


func TestIsFinalizedTransaction(t *testing.T) {
	//test multIn tx:
	lockTimeZeroTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   0,
	}

	lockHeightTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   123,
	}

	lockTimeTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   1234567890,
	}

	invalidSequenceTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   3234567890,
	}
	invalidSequenceTx.TxIn[0].Sequence = math.MaxUint32
	invalidSequenceTx.TxIn[1].Sequence = math.MaxUint32 - 1

	validSequenceTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   3234567890,
	}
	validSequenceTx.TxIn[0].Sequence = math.MaxUint32
	validSequenceTx.TxIn[1].Sequence = math.MaxUint32


	timestamp := int64(2234567890)
	tests := []struct {
		tx     	*asiutil.Tx
		height  int32
		time 	int64
		result  bool
	} {
		{
			asiutil.NewTx(lockTimeZeroTx),
			1234,
			timestamp,
			true,
		},
		{
			asiutil.NewTx(lockHeightTx),
			1234,
			timestamp,
			true,
		},
		{
			asiutil.NewTx(lockTimeTx),
			1234,
			timestamp,
			true,
		},
		{
			asiutil.NewTx(invalidSequenceTx),
			1234,
			timestamp,
			false,
		},
		{
			asiutil.NewTx(validSequenceTx),
			1234,
			timestamp,
			true,
		},

	}

	t.Logf("Running %d TestIsFinalizedTransaction tests", len(tests))
	for i, test := range tests {
		result := IsFinalizedTransaction(test.tx,test.height,test.time)
		if result != test.result {
			t.Errorf("the %d test error: want: %v, but got: %v", i, test.result, result)
		}
	}
}

func TestCheckTransactionSanity(t *testing.T) {
	noInTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   123,
	}

	maxSerializeTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract: protos.TxContract{21000},
		LockTime:   0,
	}
	tmpTxIn := protos.NewTxIn(&protos.OutPoint{
		Hash:common.Hash{},
		Index:protos.MaxPrevOutIndex-2,
	}, nil)
	tmpTxIn.Sequence = 123456
	tmpTxOut := protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset)
	tmpTxOut.Data = []byte{0x01,0x02,0x03,0x04,0x05,0x06,0x07,0x08,0x01,0x02,0x03,0x04,0x05,0x06,0x07,0x08,0x01,0x02,0x03,0x04,0x05,0x06,0x07,0x08,0x01,0x02,0x03,0x04,0x05,0x06,0x07,0x08,}
	for k:=0; k<30000; k++ {
		maxSerializeTx.TxIn = append(maxSerializeTx.TxIn,tmpTxIn)
		maxSerializeTx.TxOut = append(maxSerializeTx.TxOut,tmpTxOut)
	}

	negativeOutTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(-1, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	maxValueTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(common.MaxXing+1, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	totalMaxValueTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(common.MaxXing-1, []byte{txscript.OP_1}, protos.Asset{1,0}),
			protos.NewTxOut(common.MaxXing-1, []byte{txscript.OP_1}, protos.Asset{1,0}),
			protos.NewTxOut(common.MaxXing-1, []byte{txscript.OP_1}, protos.Asset{0,0}),
			protos.NewTxOut(common.MaxXing-1, []byte{txscript.OP_1}, protos.Asset{0,0}),
		},
	}

	duplicateInPutTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex-2,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex-2,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(123456, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	coinbaseSignatureScriptTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(123456, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	preOutIsNilTx := &protos.MsgTx{
		Version:1,
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{0x01,},
				Index:protos.MaxPrevOutIndex-3,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{},
				Index:protos.MaxPrevOutIndex,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(123456, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
		TxContract:protos.TxContract{
			GasLimit:1000000,
		},
	}

	tests := []struct {
		tx     	*asiutil.Tx
		errStr   string
	} {
		{
			asiutil.NewTx(noInTx),
			"ErrNoTxInputs",
		},
		{
			asiutil.NewTx(maxSerializeTx),
			"ErrTxTooBig",
		},
		{
			asiutil.NewTx(negativeOutTx),
			"ErrBadTxOutValue",
		},
		{
			asiutil.NewTx(maxValueTx),
			"ErrBadTxOutValue",
		},
		{
			asiutil.NewTx(totalMaxValueTx),
			"ErrBadTxOutValue",
		},
		{
			asiutil.NewTx(duplicateInPutTx),
			"ErrDuplicateTxInputs",
		},
		{
			asiutil.NewTx(coinbaseSignatureScriptTx),
			"ErrBadCoinbaseScriptLen",
		},
		{
			asiutil.NewTx(preOutIsNilTx),
			"ErrBadTxInput",
		},
	}

	t.Logf("Running %d TestCheckTransactionSanity tests", len(tests))
	for i, test := range tests {
		err := CheckTransactionSanity(test.tx)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v, but got: %v",
						i, test.errStr, dbErr.ErrorCode.String())
				}
			}
		}
	}
}


func TestCountP2SHSigOps(t *testing.T) {
	//test coinbase tx:
	coinbaseTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	coinbaseView := txo.NewUtxoViewpoint()

	noUtxoTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-6,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	noUtxoView := txo.NewUtxoViewpoint()

	hasUtxoTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-6,
			}, nil),
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{0xfe,}, Index:protos.MaxPrevOutIndex-7,},
				[]byte{0x04, 0x31, 0xdc, 0x00, 0x1b, 0x01, 0x62},),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}
	hasUtxoView := txo.NewUtxoViewpoint()
	hasUtxoView.AddEntry(hasUtxoTx.TxIn[0].PreviousOutPoint, txo.NewUtxoEntry(
		300000000,
		nil,
		0,
		false,
		&asiutil.AsimovAsset,
		nil))
	hasUtxoView.AddEntry(hasUtxoTx.TxIn[1].PreviousOutPoint, txo.NewUtxoEntry(
		300000000,
		hexToBytes("a915731018853670f9f3b0582c5b9ee8ce93764ac32b93c4"),
		0,
		false,
		&asiutil.AsimovAsset,
		nil))

	tests := []struct {
		tx     		*asiutil.Tx
		utxoView 	*txo.UtxoViewpoint
		result 		int
		errStr		string
	} {
		{
			asiutil.NewTx(coinbaseTx),
			coinbaseView,
			0,
			"",

		},
		{
			asiutil.NewTx(noUtxoTx),
			noUtxoView,
			0,
			"ErrMissingTxOut",

		},
		{
			asiutil.NewTx(hasUtxoTx),
			hasUtxoView,
			0,
			"",

		},
	}

	t.Logf("Running %d TestCountP2SHSigOps tests", len(tests))
	for i, test := range tests {
		iscoinbase := IsCoinBaseTx(test.tx.MsgTx())
		result,err := CountP2SHSigOps(test.tx,iscoinbase, test.utxoView)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v,but got %v",
						i, test.errStr, dbErr.ErrorCode.String())
				}
			}
		} else {
			if result != test.result {
				t.Errorf("the %d test error: result mismatch: want:%v,but got %v ",i, test.result, result)
			}
		}
	}
}


func TestExtractCoinbaseHeight(t *testing.T) {
	//test coinbase tx:
	sigScriptNilTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex,
			}, nil),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	singleByteSigTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, []byte{0x00}),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	coinbaseScript, _ := StandardCoinbaseScript(15, uint64(0))
	coinbaseTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, coinbaseScript),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	ScriptLenTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, []byte{0x03,0x02}),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	testHeight := int32(1000)
	coinbaseScript, _ = StandardCoinbaseScript(testHeight, uint64(0))
	normalCoinbaseTx := &protos.MsgTx{
		TxIn: []*protos.TxIn {
			protos.NewTxIn(&protos.OutPoint{
				Hash:common.Hash{}, Index:protos.MaxPrevOutIndex-1,
			}, coinbaseScript),
		},
		TxOut: []*protos.TxOut {
			protos.NewTxOut(10000000000, []byte{txscript.OP_1}, asiutil.AsimovAsset),
		},
	}

	tests := []struct {
		tx     	*asiutil.Tx
		result 	int32
		errStr	string
	} {
		{
			asiutil.NewTx(sigScriptNilTx),
			0,
			"ErrMissingCoinbaseHeight",
		},
		{
			asiutil.NewTx(singleByteSigTx),
			0,
			"",
		},
		{
			asiutil.NewTx(coinbaseTx),
			15,
			"",
		},
		{
			asiutil.NewTx(ScriptLenTx),
			15,
			"ErrMissingCoinbaseHeight",
		},
		{
			asiutil.NewTx(normalCoinbaseTx),
			testHeight,
			"",
		},
	}

	t.Logf("Running %d TestExtractCoinbaseHeight tests", len(tests))
	for i, test := range tests {
		result,err := ExtractCoinbaseHeight(test.tx)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				if !ok {
					t.Errorf("the %d test error:: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error:: errCode mismatch: want:%v,but got %v",
						i, test.errStr, dbErr.ErrorCode.String())
				}
			}
		} else {
			if result != test.result {
				t.Errorf("the %d test error: result mismatch: want: %v, but got: %v",i, test.result, result)
			}
		}
	}
}


// TestBlockHeaderSerialize tests BlockHeader serialize and deserialize.
func TestCheckBlockHeaderContext(t *testing.T) {
	parivateKeyList := []string{
		"0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25",  //privateKey0
	}
	_, _, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()


	best := chain.BestSnapshot()
	bestNode,_ := chain.GetNodeByHeight(best.Height)
	bestNode.timestamp = 123456789

	errTimestampHeader := &protos.BlockHeader{
		Version:       1,
		PrevBlock:     best.Hash,
		MerkleRoot:    common.Hash{0x00,},
		Timestamp:     113456789,
		Height:        best.Height+1,
		StateRoot:     common.Hash{0x00,},
		GasLimit:      10000000,
		GasUsed:       9000000,
		Round:		   1,
		SlotIndex:     122,
		CoinBase:      [21]byte{0x66},
		SigData:       [65]byte{0x63, 0xe3, 0x83, 0x03, 0xb3, 0xf3, 0xb3, 0x73, 0x83},
	}

	ts := medianAdjustedTime(chain.BestSnapshot(), chain.timeSource)
	errSlotHeader := &protos.BlockHeader{
		Version:       1,
		PrevBlock:     best.Hash,
		MerkleRoot:    common.Hash{0x00,},
		Timestamp:     ts,
		Height:        best.Height+1,
		StateRoot:     common.Hash{0x00,},
		GasLimit:      50048827,
		GasUsed:       9000000,
		Round:		   1,
		SlotIndex:     122,
		CoinBase:      [21]byte{0x66},
		SigData:       [65]byte{0x63, 0xe3, 0x83, 0x03, 0xb3, 0xf3, 0xb3, 0x73, 0x83},
	}

	normalHeader := &protos.BlockHeader{
		Version:       1,
		PrevBlock:     best.Hash,
		MerkleRoot:    common.Hash{0x00,},
		Timestamp:     ts,
		Height:        best.Height+1,
		StateRoot:     common.Hash{0x00,},
		GasLimit:      50048827,
		GasUsed:       9000000,
		Round:		   1,
		SlotIndex:     10,
		CoinBase:      [21]byte{0x66},
		SigData:       [65]byte{0x63, 0xe3, 0x83, 0x03, 0xb3, 0xf3, 0xb3, 0x73, 0x83},
	}

	tests := []struct {
		in  	*protos.BlockHeader // Data to encode
		errStr	string
	}{
		{
			errTimestampHeader,
			"ErrTimeStampOutOfRange",
		},
		{
			errSlotHeader,
			"ErrInvalidSlotIndex",
		},
		{
			normalHeader,
			"",
		},
	}

	t.Logf("Running %d TestCheckBlockHeaderContext tests", len(tests))
	for i, test := range tests {
		err := chain.checkBlockHeaderContext(test.in, bestNode, bestNode.round)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v, but got: %v",
						i, test.errStr, dbErr.ErrorCode.String())
				}
			}
		}
	}
}


func TestCheckBlockContext(t *testing.T) {
	parivateKeyList := []string{
		"0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25",  //privateKey0
	}
	_, _, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()

	genesisNode := chain.bestChain.tip()

	//address that is not the validator:
	privateKey := "0x77366e621236e71a77236e0858cd652e92c3af0908527b5bd1542992c4d7cace"
	tmpAcc, _ := crypto.NewAccount(privateKey)
	tmpAddress := tmpAcc.Address

	//create empty block: checkBlockHeaderContext error:
	timestampErrBlock, err := createTestBlock(chain, uint32(1), uint16(0), 0, protos.Asset{0,0},
	0, tmpAddress,nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("createTestBlock error %v", err)
	}
	timestampErrBlock.MsgBlock().Header.Timestamp = time.Now().Unix() + 1000

	//not validator:
	notValidatorBlock, err := createTestBlock(chain, uint32(1), uint16(0), 0, protos.Asset{0,0},
	0, tmpAddress,nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("createTestBlock error %v", err)
	}

	validators, _, _ := chain.GetValidatorsByNode(1,genesisNode)
	var coinBaseAddr *common.Address
	if len(validators) > 0 {
		coinBaseAddr = validators[0]
	} else {
		coinBaseAddr = tmpAddress
	}
	//signature error:
	signatureErrBlock, err := createTestBlock(chain, uint32(1), uint16(0), 0, protos.Asset{0,0},
	0, coinBaseAddr,nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("createTestBlock error %v", err)
	}
	blockHash := signatureErrBlock.Hash()
	signature, signErr := crypto.Sign(blockHash[:], (*ecdsa.PrivateKey)(&tmpAcc.PrivateKey))
	if err != nil {
		t.Errorf("crypto.Sign error %v", signErr)
	}
	copy(signatureErrBlock.MsgBlock().Header.SigData[:], signature)

	tests := []struct {
		block     *asiutil.Block
		errStr	  string
	} {
		//timeStamp error:
		{
			timestampErrBlock,
			"ErrTimeStampOutOfRange",
		},
		//not validators:
		{
			notValidatorBlock,
			"ErrValidatorMismatch",
		},
		//signature error:
		{
			signatureErrBlock,
			"ErrSigAndKeyMismatch",
		},
	}

	t.Logf("Running %d TestCheckBlockContext tests", len(tests))
	for i, test := range tests {
		err = chain.checkBlockContext(test.block, genesisNode, genesisNode.round, 0)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v, but got: %v",
						i, test.errStr, dbErr.ErrorCode.String())
				}
			}
		}
	}
}

func TestCheckSignatures(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e",  //privateKey0
		"0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2",
		"0x77366e621236e71a77236e0858cd652e92c3af0908527b5bd1542992c4d7cace",
	}
	_, netParam, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()

	privateKey := "0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25"
	tmpAcc, _ := crypto.NewAccount(privateKey)
	tmpAddress := tmpAcc.Address

	//signature error:
	emptyBlock, err := createTestBlock(chain, uint32(1), uint16(0), 0, protos.Asset{0,0},
	0, tmpAddress,nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("createTestBlock error %v", err)
	}
	emptyBlock.MsgBlock().Header.Timestamp = 0x495fab29
	blockHash := emptyBlock.Hash()
	signature, signErr := crypto.Sign(blockHash[:], (*ecdsa.PrivateKey)(&tmpAcc.PrivateKey))
	if err != nil {
		t.Errorf("crypto.Sign error %v", signErr)
	}
	copy(emptyBlock.MsgBlock().Header.SigData[:], signature)
	emptyBlock.MsgBlock().Header.Height = 50

	//the preSign block height is to old:
	errHeightPreSign := make([]*protos.MsgBlockSign,0)
	tmpMsgSign := &protos.MsgBlockSign {
		BlockHeight:1,
		BlockHash:common.Hash{0x02,},
		Signer:common.Address{0x66,0x23,},
		Signature: [protos.HashSignLen]byte{0x25,},
	}
	errHeightPreSign = append(errHeightPreSign,tmpMsgSign)
	emptyBlock.MsgBlock().PreBlockSigs = errHeightPreSign
	oldHeightPreSignCodeStr := "ErrBadPreSigHeight"
	checkErr := chain.checkSignatures(emptyBlock)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != oldHeightPreSignCodeStr {
			if !ok {
				t.Errorf("tests checkSignatures error: can not get the errorCode: %v", checkErr)
			} else {
				t.Errorf("tests checkSignatures error: errCode mismatch: want:%v, but got:%v",
					oldHeightPreSignCodeStr, dbErr.ErrorCode.String())
			}
		}
	}

	//the preSign block height is bigger than cur block height:
	errHeightPreSign[0].BlockHeight = 100
	oldHeightPreSignStr := "ErrBadPreSigHeight"
	checkErr = chain.checkSignatures(emptyBlock)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != oldHeightPreSignStr {
			if !ok {
				t.Errorf("tests checkSignatures error: can not get the errorCode: %v", checkErr)
			} else {
				t.Errorf("tests checkSignatures error: errCode mismatch: want:%v, but got:%v",
					oldHeightPreSignStr, dbErr.ErrorCode.String())
			}
		}
	}

	//the signer of presign is unknown:
	emptyBlock.MsgBlock().PreBlockSigs[0].BlockHeight = 45
	signerStr := "ErrValidatorMismatch"
	checkErr = chain.checkSignatures(emptyBlock)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != signerStr {
			if !ok {
				t.Errorf("test checkSignatures error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignatures error: errCode mismatch: want:%v, but got: %v",
					signerStr, dbErr.ErrorCode.String())
			}
		}
	}

	//preSign signature verify error:
	emptyBlock.MsgBlock().PreBlockSigs[0].Signer = netParam.GenesisCandidates[0]
	signCheckStr := "ErrInvalidSigData"
	checkErr = chain.checkSignatures(emptyBlock)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != signCheckStr {
			if !ok {
				t.Errorf("test checkSignatures error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignatures error: errCode mismatch: want:%v, but got: %v",
					signCheckStr, dbErr.ErrorCode.String())
			}
		}
	}

	//curBlock signature verify error:
	emptyBlock.MsgBlock().PreBlockSigs = []*protos.MsgBlockSign{}
	emptyBlock.MsgBlock().Header.SigData = [protos.HashSignLen]byte{0x25,}
	curSignCheckStr := "ErrInvalidSigData"
	checkErr = chain.checkSignatures(emptyBlock)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != curSignCheckStr {
			if !ok {
				t.Errorf("test checkSignatures error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignatures error: errCode mismatch: want:%v, but got: %v",
					curSignCheckStr, dbErr.ErrorCode.String())
			}
		}
	}
}

func TestCheckSignaturesWeight(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e",  //privateKey0
		"0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2",  //privateKey1
		"0x77366e621236e71a77236e0858cd652e92c3af0908527b5bd1542992c4d7cace",  //privateKey2
	}
	accList, netParam, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()

	//create address that is not validator
	privateKey := "0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25"
	tmpAcc, _ := crypto.NewAccount(privateKey)
	tmpAddress := tmpAcc.Address

	genesisNode := chain.bestChain.tip()
	validators, filters, _ := chain.GetValidatorsByNode(1,genesisNode)

	// check block coinbase addr:
	CoinbaseAddrErrblock, err := createTestBlock(chain,1, 0, 0, protos.Asset{0,0},
	0,tmpAddress,nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("create block error %v", err)
	}
	round := genesisNode.round
	if round.Round != CoinbaseAddrErrblock.MsgBlock().Header.Round {
		round, err = chain.roundManager.GetNextRound(round)
		if err != nil {
			t.Errorf("create block error %v", err)
			return
		}
	}
	newNode := newBlockNode(round, &CoinbaseAddrErrblock.MsgBlock().Header, genesisNode)
	coinbaseAddrErrStr := "ErrValidatorMismatch"
	checkErr := chain.checkSignaturesWeight(newNode, CoinbaseAddrErrblock,nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != coinbaseAddrErrStr {
			if !ok {
				t.Errorf("test checkSignaturesWeight error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignaturesWeight error: errCode mismatch: want:%v, but got: %v",
					coinbaseAddrErrStr, dbErr.ErrorCode.String())
			}
		}
	}

	//preSign blockHeight is too small: The max height depth is 10
	// check block coinbase addr:
	sigHeightErrblock, err := createTestBlock(chain,1, 20, 20, protos.Asset{0,0},
	0,validators[20],nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("create block error %v", err)
	}
	//the preSign block height is to old:
	errHeightPreSign := make([]*protos.MsgBlockSign,0)
	tmpMsgSign := &protos.MsgBlockSign {
		BlockHeight:1,
		BlockHash:common.Hash{0x02,},
		Signer:common.Address{0x66,0x23,},
		Signature: [protos.HashSignLen]byte{0x25,},
	}
	errHeightPreSign = append(errHeightPreSign,tmpMsgSign)
	sigHeightErrblock.MsgBlock().PreBlockSigs = errHeightPreSign
	round = genesisNode.round
	if round.Round != sigHeightErrblock.MsgBlock().Header.Round {
		round, err = chain.roundManager.GetNextRound(round)
		if err != nil {
			t.Errorf("create block error %v", err)
			return
		}
	}
	newNode = newBlockNode(round, &sigHeightErrblock.MsgBlock().Header, genesisNode)
	badPresigHeight := "ErrBadPreSigHeight"
	checkErr = chain.checkSignaturesWeight(newNode, sigHeightErrblock, nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != badPresigHeight {
			if !ok {
				t.Errorf("test checkSignaturesWeight error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignaturesWeight error: errCode mismatch: want:%v, but got: %v",
					badPresigHeight, dbErr.ErrorCode.String())
			}
		}
	}

	blockList := make([]*asiutil.Block,0)
	//添加4个block到bestChain:
	for i:=int32(0); i<4; i++ {
		block, err := createTestBlock(chain,1, uint16(i), i, protos.Asset{0,0}, 0,
		validators[i],nil,chain.bestChain.Tip())
		if err != nil {
			t.Errorf("create block error %v", err)
		}
		best := chain.BestSnapshot()
		preNode, _ := chain.GetNodeByHeight(best.Height)
		round = preNode.round
		if round.Round != block.MsgBlock().Header.Round {
			round, err = chain.roundManager.GetNextRound(round)
			if err != nil {
				t.Errorf("create block error %v", err)
				return
			}
		}
		newNode = newBlockNode(round, &block.MsgBlock().Header, preNode)
		blockWeight := uint16(0)

		for k, v := range filters {
			if k == *validators[i] {
				blockWeight += v
			}
		}

		PreSigns := make(protos.BlockSignList,0)
		if i > 0 {
			targetBlock := blockList[i-1]
			sigHeight := targetBlock.MsgBlock().Header.Height
			sigHash := targetBlock.MsgBlock().BlockHash()
			for k:=0; k<len(netParam.GenesisCandidates); k++ {
				if netParam.GenesisCandidates[k] != targetBlock.MsgBlock().Header.CoinBase {
					checkSign := &protos.MsgBlockSign {
						BlockHeight:sigHeight,
						BlockHash:sigHash,
						Signer:netParam.GenesisCandidates[k],
					}
					signature, signErr := crypto.Sign(checkSign.BlockHash[:], (*ecdsa.PrivateKey)(&accList[k].PrivateKey))
					if err != nil {
						t.Errorf("crypto.Sign error %v", signErr)
					}
					copy(checkSign.Signature[:], signature)
					blockWeight += filters[netParam.GenesisCandidates[k]]
					PreSigns = append(PreSigns, checkSign)
				}
			}
		}
		sort.Sort(PreSigns)
		block.MsgBlock().PreBlockSigs = PreSigns
		block.MsgBlock().Header.Weight = blockWeight
		block.MsgBlock().Header.PoaHash = block.MsgBlock().CalculatePoaHash()
		hash := block.MsgBlock().Header.BlockHash()
		index := 0
		for k:=0; k<len(netParam.GenesisCandidates); k++ {
			if netParam.GenesisCandidates[k] == block.MsgBlock().Header.CoinBase {
				index = k
			}
		}
		signature, signErr := crypto.Sign(hash[:], (*ecdsa.PrivateKey)(&accList[index].PrivateKey))
		if err != nil {
			t.Errorf("crypto.Sign error %v", signErr)
		}
		copy(block.MsgBlock().Header.SigData[:], signature)

		nodeErr := chain.addBlockNode(block)
		if nodeErr != nil {
			t.Errorf("tests error %v", err)
		}
		blockList = append(blockList, block)
	}

	//test preSign block errHash:
	errHashBlock := blockList[1]
	orgHash := errHashBlock.MsgBlock().PreBlockSigs[0].BlockHash
	errHashBlock.MsgBlock().PreBlockSigs[0].BlockHash = common.Hash{}
	node,_ := chain.GetNodeByHeight(errHashBlock.Height())
	errHashStr := "ErrHashMismatch"
	checkErr = chain.checkSignaturesWeight(node, errHashBlock, nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != errHashStr {
			if !ok {
				t.Errorf("test checkSignaturesWeight error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignaturesWeight error: errCode mismatch: want:%v, but got: %v",
					errHashStr, dbErr.ErrorCode.String())
			}
		}
	}


	//test preSign err Signer
	blockList[1].MsgBlock().PreBlockSigs[0].BlockHash = orgHash
	errSignerBlock := blockList[1]
	orgSigner := errSignerBlock.MsgBlock().PreBlockSigs[0].Signer
	preBlockCoinBaseAddr := *validators[errSignerBlock.MsgBlock().PreBlockSigs[0].BlockHeight-1]
	errSignerBlock.MsgBlock().PreBlockSigs[0].Signer = preBlockCoinBaseAddr
	node,_ = chain.GetNodeByHeight(errSignerBlock.Height())
	errSignerStr := "ErrValidatorMismatch"
	checkErr = chain.checkSignaturesWeight(node, errSignerBlock, nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != errSignerStr {
			if !ok {
				t.Errorf("test checkSignaturesWeight error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignaturesWeight error: errCode mismatch: want:%v, but got: %v",
					errSignerStr, dbErr.ErrorCode.String())
			}
		}
	}

	//test block header err weight
	blockList[1].MsgBlock().PreBlockSigs[0].Signer = orgSigner
	orgWeight := blockList[1].MsgBlock().Header.Weight
	errWeightBlock := blockList[1]
	errWeightBlock.MsgBlock().Header.Weight = orgWeight+2
	node,_ = chain.GetNodeByHeight(errWeightBlock.Height())
	errBadBlkWeightStr := "ErrBadBlockWeight"
	checkErr = chain.checkSignaturesWeight(node, errWeightBlock, nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != errBadBlkWeightStr {
			if !ok {
				t.Errorf("test checkSignaturesWeight error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignaturesWeight error: errCode mismatch: want:%v, but got: %v",
					errBadBlkWeightStr, dbErr.ErrorCode.String())
			}
		}
	}

	//test ErrPreSigOrder:
	blockList[1].MsgBlock().Header.Weight = orgWeight
	orgPreSigs := blockList[1].MsgBlock().PreBlockSigs
	tmpPreSigns := make(protos.BlockSignList,0)
	tmpPreSigns = append(tmpPreSigns,orgPreSigs[1])
	tmpPreSigns = append(tmpPreSigns,orgPreSigs[0])
	blockList[1].MsgBlock().PreBlockSigs = tmpPreSigns
	errHeightBlock := blockList[1]
	firstSign := errHeightBlock.MsgBlock().PreBlockSigs[0]
	errHeightSigns := &protos.MsgBlockSign {
		BlockHeight:firstSign.BlockHeight+100,
		BlockHash:firstSign.BlockHash,
		Signer:common.Address{},
	}
	errHeightBlock.MsgBlock().PreBlockSigs = append(errHeightBlock.MsgBlock().PreBlockSigs,errHeightSigns)
	node,_ = chain.GetNodeByHeight(errHeightBlock.Height())
	errHeightStr := "ErrPreSigOrder"
	checkErr = chain.checkSignaturesWeight(node, errHeightBlock, nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != errHeightStr {
			if !ok {
				t.Errorf("test checkSignaturesWeight error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkSignaturesWeight error: errCode mismatch: want:%v, but got: %v",
					errHeightStr, dbErr.ErrorCode.String())
			}
		}
	}

	//test found duplicated sign hash
	blockList[1].MsgBlock().PreBlockSigs = orgPreSigs
	duplicateSignBlock := blockList[1]
	node,_ = chain.GetNodeByHeight(blockList[1].Height())
	err = chain.db.Update(func(dbTx database.Tx) error {
		err = dbPutSignViewPoint(dbTx, duplicateSignBlock.Signs())
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Errorf("dbPutSignViewPoint error %v", err)
	}
	checkErrStr := "ErrSignDuplicate"
	checkErr = chain.checkSignaturesWeight(node, duplicateSignBlock, nil)
	if checkErr != nil {
		if dbErr, ok := checkErr.(RuleError); !ok || dbErr.ErrorCode.String() != checkErrStr {
			if !ok {
				t.Errorf("test checkConnectSignatures error: can not get ErrorCode: %v", err)
			} else {
				t.Errorf("test checkConnectSignatures error: errCode mismatch: want:%v, but got: %v",
					checkErrStr, dbErr.ErrorCode.String())
			}
		}
	}
}

// TestSequenceLocksActive tests the SequenceLockActive function to ensure it
// works as expected in all possible combinations/scenarios.
func TestSequenceLocksActive(t *testing.T) {
	seqLock := func(h int32, s int64) *SequenceLock {
		return &SequenceLock{
			Seconds:     s,
			BlockHeight: h,
		}
	}

	tests := []struct {
		seqLock     *SequenceLock
		blockHeight int32
		mtp         int64

		want bool
	}{
		// Block based sequence lock with equal block height.
		{seqLock: seqLock(1000, -1), blockHeight: 1001, mtp: 9, want: true},

		// Time based sequence lock with mtp past the absolute time.
		{seqLock: seqLock(-1, 30), blockHeight: 2, mtp: 31, want: true},

		// Block based sequence lock with current height below seq lock block height.
		{seqLock: seqLock(1000, -1), blockHeight: 90, mtp: 9, want: false},

		// Time based sequence lock with current time before lock time.
		{seqLock: seqLock(-1, 30), blockHeight: 2, mtp: 29, want: false},

		// Block based sequence lock at the same height, so shouldn't yet be active.
		{seqLock: seqLock(1000, -1), blockHeight: 1000, mtp: 9, want: false},

		// Time based sequence lock with current time equal to lock time, so shouldn't yet be active.
		{seqLock: seqLock(-1, 30), blockHeight: 2, mtp: 30, want: false},
	}

	t.Logf("Running %d TestSequenceLockActive tests", len(tests))
	for i, test := range tests {
		got := SequenceLockActive(test.seqLock,
			test.blockHeight, test.mtp)
		if got != test.want {
			t.Fatalf("the %d test error: got %v want %v", i,
				got, test.want)
		}
	}
}

func TestCheckBlockHeaderSanity(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e",  //privateKey0
	}
	_, _, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()

	best := chain.BestSnapshot()
	tmpNode, _ := chain.GetNodeByHeight(best.Height)
	tmpNode.round.Round = 1
	tmpNode.slot = 0
	validTimeOffsetSeconds := 1 * time.Second
	normalTimestamp := time.Now().Add(validTimeOffsetSeconds).Unix()
	tmpNode.timestamp = normalTimestamp


	testHeader := protos.BlockHeader{
		Version:    1,
		PrevBlock:  best.Hash,
		MerkleRoot: common.Hash{},
		Timestamp:  best.TimeStamp,
		SlotIndex:  0,
		Round:      1,
		Height:     best.Height+1,
		GasLimit:   30000000,
		StateRoot:  best.StateRoot,
	}

	//normal timestamp:
	validTimeOffsetSeconds = 5 * time.Second
	normalTimestamp = time.Now().Add(validTimeOffsetSeconds).Unix()

	validTimeOffsetSeconds = 300 * time.Second
	futureTimestamp := time.Now().Add(validTimeOffsetSeconds).Unix()

	tests := []struct {
		timeStamp 	int64
		slot 		uint16
		round 		uint32
		errStr		string
	} {
		//normal timeStamp header:
		{
			normalTimestamp,
			1,
			1,
			"",
		},
		//Timestamp in future:
		{
			futureTimestamp,
			1,
			1,
			"ErrTimeTooNew",
		},
		//test bad slot or round:
		{
			normalTimestamp,
			0,
			0,
			"ErrBadSlotOrRound",
		},
		//test two neighbor nodes time stamp:
		{
			normalTimestamp,
			2,
			2,
			"ErrBadSlotOrRound",
		},
	}
	chaincfg.Cfg.MaxTimeOffset = 30

	t.Logf("Running %d checkBlockHeaderSanity tests", len(tests))
	for i, test := range tests {
		t.Logf("=============the %d test start=============", i)
		testHeader.Timestamp = test.timeStamp
		testHeader.SlotIndex = test.slot
		testHeader.Round = test.round
		err = checkBlockHeaderSanity(&testHeader, tmpNode)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v, but got: %v",
						i, test.errStr, dbErr.ErrorCode.String())
				}
			}
		}
	}
}


// TestCheckBlockSanity tests the CheckBlockSanity function to ensure it works
// as expected.
func TestCheckBlockSanity(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e",  //privateKey0
	}
	_, _, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()

	privateKey := "0x4f0c9a1759894e2a2ff19afc9d1ff314ea68a54cb9ce5fc87fdccf63a2736ceb"
	tmpAcc, _ := crypto.NewAccount(privateKey)
	payAddrPkScript, _ := txscript.PayToAddrScript(tmpAcc.Address)

	var blocks []*asiutil.Block
	genesisBlk,_ := chain.BlockByHeight(0)
	blocks = append(blocks, genesisBlk)
	best := chain.BestSnapshot()
	bestNode0, _ := chain.GetNodeByHeight(best.Height)

	//timeStamp check test:
	errHeaderBlock, errHeaderErr := createTestBlock(chain, uint32(1), 0, 0, protos.Asset{0,0},
	500000000, tmpAcc.Address,nil,chain.bestChain.Tip())
	if errHeaderErr != nil {
		t.Errorf("create errHeaderBlock error %v", errHeaderErr)
	}
	futureTimestamp := time.Now().Unix() + 1000
	errHeaderBlock.MsgBlock().Header.Timestamp = futureTimestamp

	//emptyblock check test:
	var msgBlock protos.MsgBlock
	msgBlock.Header = protos.BlockHeader{
		Version:       1,
		PrevBlock:     best.Hash,
		MerkleRoot:    common.Hash{0x00,},
		Timestamp:     123456789,
		Height:        best.Height+1,
		StateRoot:     common.Hash{0x00,},
		GasLimit:      470778607,
		GasUsed:       9000000,
		Round:		   1,
		SlotIndex:     0,
		CoinBase:      [21]byte{0x66},
		SigData:       [65]byte{0x63, 0xe3, 0x83, 0x03, 0xb3, 0xf3, 0xb3, 0x73, 0x83},
	}
	emptyBlock := asiutil.NewBlock(&msgBlock)

	//first tx check test:
	errCoinBaseBlock, errCoinbase := createTestBlock(chain, uint32(1), 0, 0, protos.Asset{0,0},
	500000000, tmpAcc.Address,nil, chain.bestChain.Tip())
	if errCoinbase != nil {
		t.Errorf("create errCoinBaseBlock error %v", errCoinbase)
	}
	coinBaseTx := errCoinBaseBlock.Transactions()[0]
	coinBaseTx.MsgTx().TxIn[0].PreviousOutPoint.Index = math.MaxUint32 - 2

	//PoaHash check test:
	poaHashBlock, poaHashBlockErr := createTestBlock(chain, uint32(1), 0, 0,
		protos.Asset{0,0},500000000, tmpAcc.Address,nil, chain.bestChain.Tip())
	if poaHashBlockErr != nil {
		t.Errorf("create poaHashBlockErr error %v", poaHashBlockErr)
	}

	preBlockSign := make([]*protos.MsgBlockSign, 0, 1)
	for i:=0; i<2; i++ {
		var tmpPreSign protos.MsgBlockSign
		//tmpPreSign.round = 1
		//tmpPreSign.Index = uint16(i)
		preBlockSign = append(preBlockSign, &tmpPreSign)
	}
	poaHashBlock.MsgBlock().PreBlockSigs = preBlockSign
	poaHashBlock.MsgBlock().Header.PoaHash = common.Hash{0} //set poaHash to {0}


	//block include more than one coinbase tx
	extraNonce := uint64(0)
	coinbaseScript, err := StandardCoinbaseScript(best.Height + 1, extraNonce)
	coinBaseTxTmp, _, _ := createTestCoinbaseTx(chaincfg.ActiveNetParams.Params,
		coinbaseScript, best.Height + 1, tmpAcc.Address, protos.Asset{0,0},50000, nil)
	coinBaseTxList := make([]*asiutil.Tx, 0)
	coinBaseTxList = append(coinBaseTxList,coinBaseTxTmp)
	dupCoinBaseBlock, dupCoinBaseBlockErr := createTestBlock(chain, uint32(1), 0, 0,
		protos.Asset{0,0},500000000,
		tmpAcc.Address,coinBaseTxList, chain.bestChain.Tip())
	if dupCoinBaseBlockErr != nil {
		t.Errorf("create dupCoinBaseBlockErr error %v", dupCoinBaseBlockErr)
	}

	//check MaxBlockSize test:
	txLists := make([]*asiutil.Tx, 0)
	var maxSizeBlock *asiutil.Block
	//create txs for maxSizeBlock test:
	for m:=0; m<10000; m++ {
		inputAmountList := []int64{100000000}
		inputAssetList := []*protos.Asset{{0,0}}
		outputAmountList := []int64{20000000}
		outputAssetsList := []*protos.Asset{{0,0}}
		tmpTx,_,_, tmpTxErr := createTestTx(privateKey, payAddrPkScript, payAddrPkScript,100000,
			inputAmountList,inputAssetList,outputAmountList,outputAssetsList)
		if tmpTxErr != nil {
			t.Errorf("create tmpTx error %v", tmpTxErr)
		}
		maxTx := asiutil.NewTx(tmpTx)
		txLists = append(txLists,maxTx)
	}
	var maxSizeBlockErr error
	maxSizeBlock, maxSizeBlockErr = createTestBlock(chain, uint32(1), 0, 0, protos.Asset{0,0},
	0, tmpAcc.Address,txLists, chain.bestChain.Tip())
	if maxSizeBlockErr != nil {
		t.Errorf("create maxSizeBlockErr error %v", maxSizeBlockErr)
	}

	for _, tx := range txLists {
		maxSizeBlock.MsgBlock().AddTransaction(tx.MsgTx())
	}

	//create block:
	block, err := createTestBlock(chain, uint32(1), uint16(0), 0, protos.Asset{0,0},
	0, tmpAcc.Address,nil, chain.bestChain.Tip())
	if err != nil {
		t.Errorf("createTestBlock error %v", err)
	}
	block.MsgBlock().Header.Timestamp = time.Now().Unix() + 1
	stateRoot,gasUsed := getGasUsedAndStateRoot(chain, block, bestNode0)
	block.MsgBlock().Header.GasUsed = gasUsed
	block.MsgBlock().Header.StateRoot = *stateRoot
	blockHash := block.Hash()
	signature, signErr := crypto.Sign(blockHash[:], (*ecdsa.PrivateKey)(&tmpAcc.PrivateKey))
	if err != nil {
		t.Errorf("crypto.Sign error %v", signErr)
	}
	copy(block.MsgBlock().Header.SigData[:], signature)

	//更新最新链相关信息:
	nodeErr := chain.addBlockNode(block)
	if nodeErr != nil {
		t.Errorf("tests height %d error %v", block.Height(), nodeErr)
	}
	blocks = append(blocks,block)


	//dup tx test:
	var dupTxBlock *asiutil.Block
	inputAmountList := []int64{100000000}
	inputAssetList := []*protos.Asset{{0,0}}
	outputAmountList := []int64{1000000}
	outputAssetsList := []*protos.Asset{{0,0}}
	normalTx, _, _, err := createTestTx(privateKey, payAddrPkScript, payAddrPkScript,100000,
		inputAmountList,inputAssetList,outputAmountList,outputAssetsList)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}
	tx := asiutil.NewTx(normalTx)

	//merkles error test：
	merkleTxList := make([]*asiutil.Tx, 0)
	merkleTxList = append(merkleTxList,tx)
	merklesErrBlock, merklesErr := createTestBlock(chain, uint32(1), 0, 0,
		protos.Asset{0,0},500000000, tmpAcc.Address, merkleTxList,
		chain.bestChain.Tip())
	if merklesErr != nil {
		t.Errorf("create merklesErrBlock error %v", merklesErr)
	}
	var tmpMerkle []byte
	MerkleRoot := merklesErrBlock.MsgBlock().Header.MerkleRoot
	tmpMerkle = append(MerkleRoot[1:],MerkleRoot[:1]...)
	copy(merklesErrBlock.MsgBlock().Header.MerkleRoot[:],tmpMerkle[:])

	//duplicate transaction test:
	dupTxList := make([]*asiutil.Tx, 0)
	dupTxList = append(dupTxList,tx)

	var dupTxBlockErr error
	dupTxBlock, dupTxBlockErr = createTestBlock(chain, uint32(1), 2, 0, protos.Asset{0,0},
	0, tmpAcc.Address, dupTxList, chain.bestChain.Tip())
	if dupTxBlockErr != nil {
		t.Errorf("create dupTxBlockErr error %v", dupTxBlockErr)
	}

	coinbaseIdx := len(dupTxBlock.MsgBlock().Transactions) - 1
	coinbaseTx := dupTxBlock.MsgBlock().Transactions[coinbaseIdx]
	dupTxBlock.MsgBlock().Transactions = append(dupTxBlock.MsgBlock().Transactions[:coinbaseIdx], tx.MsgTx(), tx.MsgTx(), coinbaseTx)

	tmpTransactions := make([]*asiutil.Tx, len(dupTxBlock.MsgBlock().Transactions))
	for i, tx := range tmpTransactions {
		if tx == nil {
			newTx := asiutil.NewTx(dupTxBlock.MsgBlock().Transactions[i])
			newTx.SetIndex(i)
			tmpTransactions[i] = newTx
		}
	}
	merkles := BuildMerkleTreeStore(tmpTransactions)
	calculatedMerkleRoot := merkles[len(merkles)-1]
	dupTxBlock.MsgBlock().Header.MerkleRoot = *calculatedMerkleRoot

	tests := []struct {
		block 		*asiutil.Block
		errStr		string
	} {
		//test0:block with err header timestamp
		{
			errHeaderBlock,
			"ErrTimeTooNew",
		},
		//test1:block with no tx
		{
			emptyBlock,
			"ErrNoTransactions",
		},
		//test2:block with errCoinbaseTx
		{
			errCoinBaseBlock,
			"ErrLastTxNotCoinbase",
		},
		//test4: errPoaHash test:
		{
			poaHashBlock,
			"ErrBadPoaHash",
		},
		//test5: block include two poaTx test:
		{
			dupCoinBaseBlock,
			"ErrMultipleCoinbases",
		},
		//test6: maxSize block test:
		{
			maxSizeBlock,
			"ErrBlockTooBig",
		},

		//test7: common block
		{
			blocks[1],
			"",
		},

		//test8: err merkleRoot test:
		{
			merklesErrBlock,
			"ErrBadMerkleRoot",
		},

		//test9: dupTxBlock test:
		{
			dupTxBlock,
			"ErrDuplicateTx",
		},
	}

	chaincfg.Cfg.MaxTimeOffset = 30

	t.Logf("Running %d TestCheckBlockSanity tests", len(tests))
	for i, test := range tests {
		err = CheckBlockSanity(test.block, bestNode0)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
				t.Errorf("test %d error: errCode mismatch: want:%v, but got %v",
					i, test.errStr, dbErr.ErrorCode.String())
			}
		}
	}
}

// TestCheckSerializedHeight tests the checkSerializedHeight function with
// various serialized heights and also does negative tests to ensure errors
// and handled properly.
func TestCheckSerializedHeight(t *testing.T) {
	// Create an empty coinbase template to be used in the tests below.
	coinbaseOutpoint := protos.NewOutPoint(&common.Hash{}, math.MaxUint32)
	coinbaseTx := protos.NewMsgTx(1)

	coinbaseTx.AddTxIn(protos.NewTxIn(coinbaseOutpoint, nil))

	// Expected rule errors.
	missingHeightError := RuleError{
		ErrorCode: ErrMissingCoinbaseHeight,
	}
	badHeightError := RuleError{
		ErrorCode: ErrBadCoinbaseHeight,
	}

	tests := []struct {
		sigScript  []byte // Serialized data
		wantHeight int32  // Expected height
		err        error  // Expected error type
	}{
		// No serialized height length.
		{[]byte{}, 0, missingHeightError},
		// Serialized height length with no height bytes.
		{[]byte{0x02}, 0, missingHeightError},
		// Serialized height length with too few height bytes.
		{[]byte{0x02, 0x4a}, 0, missingHeightError},
		// Serialized height that needs 2 bytes to encode.
		{[]byte{0x02, 0x4a, 0x52}, 21066, nil},
		// Serialized height that needs 2 bytes to encode, but backwards
		// endianness.
		{[]byte{0x02, 0x4a, 0x52}, 19026, badHeightError},
		// Serialized height that needs 3 bytes to encode.
		{[]byte{0x03, 0x40, 0x0d, 0x03}, 200000, nil},
		// Serialized height that needs 3 bytes to encode, but backwards
		// endianness.
		{[]byte{0x03, 0x40, 0x0d, 0x03}, 1074594560, badHeightError},
	}

	t.Logf("Running %d TestCheckSerializeHeight tests", len(tests))
	for i, test := range tests {
		msgTx := coinbaseTx.Copy()
		msgTx.TxIn[0].SignatureScript = test.sigScript
		tx := asiutil.NewTx(msgTx)

		err := checkSerializedHeight(tx, test.wantHeight)
		if reflect.TypeOf(err) != reflect.TypeOf(test.err) {
			t.Errorf("checkSerializedHeight #%d wrong error type got: %v <%T>, want: %T", i, err, err, test.err)
			continue
		}

		if rerr, ok := err.(RuleError); ok {
			trerr := test.err.(RuleError)
			if rerr.ErrorCode != trerr.ErrorCode {
				t.Errorf("checkSerializedHeight #%d wrong error code got: %v, want: %v",
					i, rerr.ErrorCode, trerr.ErrorCode)
				continue
			}
		}
	}
}

var FlowAsset = protos.Asset{
	0,0,
}

var DivisibleAsset1 = protos.Asset{
	0,1,
}
var DivisibleAsset2 = protos.Asset{
	0,2,
}
var DivisibleAsset3 = protos.Asset{
	0,3,
}

var DivisibleAsset4 = protos.Asset{
	0,4,
}

var UnDivisibleAsset1 = protos.Asset{
	1,1,
}
var UnDivisibleAsset2 = protos.Asset{
	1,2,
}
var UnDivisibleAsset3 = protos.Asset{
	1,3,
}

var UnDivisibleAsset4 = protos.Asset{
	1,4,
}

//生成可分割资产错误交易，utxo already has been spent
func createSpentUtxoErrTx(
	privString		string,
	payAddrPkScript	[]byte,
	receiverPkScript []byte) (*protos.MsgTx, map[protos.Asset]int64, *txo.UtxoViewpoint, error) {
	var err error
	var ErrMultTx2 *protos.MsgTx
	ErrFees2 := make(map[protos.Asset]int64)
	ErrView2 := txo.NewUtxoViewpoint()
	{
		//input list:
		utxo1 := txo.NewUtxoEntry(10000000+int64(1)*10, payAddrPkScript,1,false, &DivisibleAsset1, nil)
		utxo1.Spend()
		var utxoList = []*txo.UtxoEntry{utxo1}

		//output list:
		outputList := make([]protos.TxOut, 0)
		txOut := protos.NewContractTxOut(10000000+int64(1)*10, receiverPkScript, DivisibleAsset1, nil)
		outputList = append(outputList, *txOut)

		preOutPoints := make([]protos.OutPoint, 0)
		createUtxoView(utxoList, ErrView2, &preOutPoints)
		ErrMultTx2, err = createTxByParams(privString, 100000, &preOutPoints, &outputList)
	}
	return ErrMultTx2, ErrFees2, ErrView2, err
}

func createUnMaturityTx(
	privString		string,
	payAddrPkScript	[]byte,
	receiverPkScript []byte) (*protos.MsgTx, map[protos.Asset]int64, *txo.UtxoViewpoint, error) {

	var err error
	var ErrMultTx2 *protos.MsgTx
	ErrFees2 := make(map[protos.Asset]int64)
	ErrView2 := txo.NewUtxoViewpoint()
	{
		//input list:
		utxo1 := txo.NewUtxoEntry(10000000+int64(1)*10, payAddrPkScript,1,true, &DivisibleAsset1, nil)
		var utxoList = []*txo.UtxoEntry{utxo1}

		//output list:
		outputList := make([]protos.TxOut, 0)
		txOut := protos.NewContractTxOut(10000000+int64(1)*10, receiverPkScript, DivisibleAsset1, nil)
		outputList = append(outputList, *txOut)

		preOutPoints := make([]protos.OutPoint, 0)
		createUtxoView(utxoList, ErrView2, &preOutPoints)
		ErrMultTx2, err = createTxByParams(privString, 100000, &preOutPoints, &outputList)
	}
	return ErrMultTx2, ErrFees2, ErrView2, err
}


//the output with opcode is not the first output:
func createErrorOpcodeTx(
	privString string,
	payAddrPkScript []byte,
	receiverPkScript []byte) (*protos.MsgTx, map[protos.Asset]int64, *txo.UtxoViewpoint, error) {

	var err error
	var normalTx1 *protos.MsgTx
	resultFees1 := make(map[protos.Asset]int64)
	utxoViewPoint1 := txo.NewUtxoViewpoint()
	{
		transferAmount := []int64{10000000,9000000}
		totalOutPut := transferAmount[0] + transferAmount[1]
		utxoList := make([]*txo.UtxoEntry, 0)
		totalInput := int64(0)
		//input utxo list:
		for i := 0; i < 2; i++ {
			utxo := txo.NewUtxoEntry(10000000+int64(i)*10, payAddrPkScript,1,
				false, &asiutil.AsimovAsset, nil)
			utxoList = append(utxoList, utxo)
			totalInput += utxo.Amount()
		}
		//output list:
		outputList := make([]protos.TxOut, 0)
		txOut := protos.NewContractTxOut(transferAmount[0], receiverPkScript, asiutil.AsimovAsset, nil)

		tmpAcc, _ := crypto.NewAccount(privString)
		tmpAcc.Address[0] = 99
		contractScript, _ := txscript.PayToContractHash(tmpAcc.Address[:])

		txOut1 := protos.NewContractTxOut(transferAmount[1], contractScript, asiutil.AsimovAsset, nil)
		outputList = append(outputList, *txOut)
		outputList = append(outputList, *txOut1)

		preOutPoints := make([]protos.OutPoint, 0)
		createUtxoView(utxoList, utxoViewPoint1, &preOutPoints)
		normalTx1, err = createTxByParams(privString, 100000, &preOutPoints, &outputList)

		resultFees1[asiutil.AsimovAsset] = totalInput - totalOutPut
	}
	return normalTx1, resultFees1, utxoViewPoint1, err
}


func TestCheckTransactionInputs(t *testing.T) {
	privString := "0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e"
	chain, teardownFunc, err := newFakeChain(&chaincfg.MainNetParams)
	if err != nil ||chain == nil {
		t.Errorf("newFakeChain error %v", err)
	}
	defer teardownFunc()

	acc, _ := crypto.NewAccount(privString)
	payAddrPkScript, _ := txscript.PayToAddrScript(acc.Address)

	//output priv:
	receiverPrivString := "0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2"
	recAcc, _ := crypto.NewAccount(receiverPrivString)
	receiverPkScript, _ := txscript.PayToAddrScript(recAcc.Address)

	//normal tx test://////////////////////////////////////////////////////////////////////////////////
	//Test0: gen coinbase:---------
	extraNonce := uint64(0)
	best := chain.BestSnapshot()
	nextBlkHeight := best.Height + 1
	coinbaseScript, err := StandardCoinbaseScript(nextBlkHeight, extraNonce)
	coinBaseTx, _, _ := createTestCoinbaseTx(chaincfg.ActiveNetParams.Params,
		coinbaseScript, nextBlkHeight, acc.Address, protos.Asset{0,1},0, nil)

	//Test1:gen VTX tx:-----------
	vtx := protos.NewMsgTx(protos.TxVersion)
	var testTxIn protos.TxIn
	testTxIn.SignatureScript = []byte{0xc3,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x00,0x00,}
	vtx.TxIn = append(vtx.TxIn, &testTxIn)
	vtxMsg := asiutil.NewTx(vtx)
	vtxMsg.Type()

	//----------------------------test normal tx of Divisible asset:---------------------------------------------------
	//Test2: gen tx with only one kind of asset: two input，two output: input > output
	inputNormalTx1 := []int64{100000010,100000020}
	outputNormalTx1 := []int64{10000000,9000000}
	assetListNormalTx1 := []*protos.Asset{&FlowAsset,&FlowAsset}
	normalTx1, resultFees1, utxoViewPoint1, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputNormalTx1,assetListNormalTx1,outputNormalTx1,assetListNormalTx1)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//Test3:gen tx with only one kind of asset: two input，one output: input = output
	inputNormalTx2 := []int64{20000010,20000020}
	outputNormalTx2 := []int64{20000010,20000020}
	inputAssetListTx2 := []*protos.Asset{&FlowAsset, &FlowAsset}
	outputAssetListTx2 := inputAssetListTx2
	normalTx2, resultFees2, utxoViewPoint2, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputNormalTx2,inputAssetListTx2,outputNormalTx2,outputAssetListTx2)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//Test4:gen tx with only one kind of asset: two input，no output:
	inputNormalTx3 := []int64{20000010,20000020}
	outputNormalTx3 := []int64{}
	inputAssetListTx3 := []*protos.Asset{&FlowAsset, &FlowAsset}
	outputAssetListTx3 := []*protos.Asset{}
	normalTx3, resultFees3, utxoViewPoint3, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputNormalTx3,inputAssetListTx3,outputNormalTx3,outputAssetListTx3)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//Test5: gen tx with multi asset: three input，three output: input > output
	inputMultTx4 := []int64{100000010, 200000020, 300000030}
	outputMultTx4 := []int64{90000000, 19000000, 29000000}
	inputAssetMultTx4 := []*protos.Asset{&DivisibleAsset1,&DivisibleAsset2,&DivisibleAsset3}
	outputAssetMultTx4 := []*protos.Asset{&DivisibleAsset1,&DivisibleAsset2,&DivisibleAsset3}
	multTx4, resultFees4, utxoViewPoint4, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputMultTx4,inputAssetMultTx4,outputMultTx4,outputAssetMultTx4)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//----------------------------test normal tx with unDivisible asset:
	//gen tx with multi asset:
	inputUndMultTx := []int64{9000000, 19000000, 29000000, 39000000}
	outputUndMultTx := []int64{9000000, 19000000, 29000000, 39000000}
	inputAssetUndMultTx := []*protos.Asset{&UnDivisibleAsset1,&UnDivisibleAsset2,&UnDivisibleAsset3,&UnDivisibleAsset4}
	outputAssetUndMultTx := inputAssetUndMultTx
	UndMultTx1, UndFees1, UndView1, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputUndMultTx,inputAssetUndMultTx,outputUndMultTx,outputAssetUndMultTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//----------------------------test multi asset tx-----------------------------------------------------
	//gen multi asset tx:
	inputMixTx1 := []int64{10000010, 20000020, 29000000, 39000000}
	outputMixTx1 := []int64{9000000, 18000000, 29000000,39000000}
	inputAssetMixTx1 := []*protos.Asset{&DivisibleAsset1,&DivisibleAsset2,&UnDivisibleAsset2,&UnDivisibleAsset3}
	outputAssetMixTx1 := inputAssetMixTx1
	MixTx1, MixFees1, MixView1, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputMixTx1,inputAssetMixTx1,outputMixTx1,outputAssetMixTx1)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//----------------------------test err tx with unDivisible asset:---------------------------------------------------
	//生成错误交易，input.amount < output.amount
	inputErrMultTx1 := []int64{10000010}
	outputErrMultTx1 := []int64{20000010}
	inputAssetErrMultTx1 := []*protos.Asset{&UnDivisibleAsset1}
	outputAssetErrMultTx1 := inputAssetErrMultTx1
	ErrMultTx1, ErrFees1, ErrView1, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputErrMultTx1,inputAssetErrMultTx1,outputErrMultTx1,outputAssetErrMultTx1)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//gen err tx: input.asset != output.asset
	inputErrMultTx2 := []int64{10000010}
	outputErrMultTx2 := []int64{10000010}
	inputAssetErrMultTx2 := []*protos.Asset{&UnDivisibleAsset1}
	outputAssetErrMultTx2 := []*protos.Asset{&UnDivisibleAsset2}
	ErrMultTx2, ErrFees2, ErrView2, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputErrMultTx2,inputAssetErrMultTx2,outputErrMultTx2,outputAssetErrMultTx2)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//----------------------------test err tx with Divisible asset:-----------------------------------------------------
	//gen err tx: input.amount < output.amount
	inputErrMultTx3 := []int64{10000010}
	outputErrMultTx3 := []int64{20000010}
	assetListErrMultTx3 := []*protos.Asset{&DivisibleAsset1}
	ErrMultTx3, ErrFees3, ErrView3, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputErrMultTx3,assetListErrMultTx3,outputErrMultTx3,assetListErrMultTx3)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//gen err tx: input.asset != output.asset
	inputErrMultTx4 := []int64{10000010}
	outputErrMultTx4 := []int64{10000010}
	inputAssetListErrMultTx4 := []*protos.Asset{&DivisibleAsset1}
	outputAssetListErrMultTx4 := []*protos.Asset{&DivisibleAsset2}
	ErrMultTx4, ErrFees4, ErrView4, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputErrMultTx4,inputAssetListErrMultTx4,outputErrMultTx4,outputAssetListErrMultTx4)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//err tx test://////////////////////////////////////////////////////////////////////////////////
	//utxo already spent:
	spentTx, spentUtxoFees, spentViews, err := createSpentUtxoErrTx(privString, payAddrPkScript, receiverPkScript)

	//utxo come from coinbase and UnMaturity:
	unMaturityTx, unMaturityFees, unMaturityViews, err := createUnMaturityTx(privString, payAddrPkScript, receiverPkScript)

	//utxo.amount = 0:
	inputNegativeUtxoTx := []int64{0}
	outputNegativeUtxoTx := []int64{10000010}
	inputAssetNegativeUtxoTx := []*protos.Asset{&DivisibleAsset1}
	outputAssetNegativeUtxoTx := inputAssetNegativeUtxoTx
	negativeUtxoTx, negativeUtxoFees, negativeUtxoViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputNegativeUtxoTx,inputAssetNegativeUtxoTx,outputNegativeUtxoTx,outputAssetNegativeUtxoTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// bigger than MaxXingUtxo:
	amount := common.MaxXing
	inputMaxUtxoTx := []int64{int64(amount + 1000)}
	outputMaxUtxoTx := []int64{2}
	inputAssetMaxUtxoTx := []*protos.Asset{&DivisibleAsset1}
	outputAssetMaxUtxoTx := inputAssetMaxUtxoTx
	MaxUtxoTx, MaxUtxoFees, MaxUtxoViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputMaxUtxoTx,inputAssetMaxUtxoTx,outputMaxUtxoTx,outputAssetMaxUtxoTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// Indivisible utxo duplicate spent: have the same input:
	inputDuplicateIndividTx := []int64{10000010,10000010}
	outputDuplicateIndividTx := []int64{10000010}
	inputAssetDuplicateIndividTx := []*protos.Asset{&UnDivisibleAsset1,&UnDivisibleAsset1}
	outputAssetDuplicateIndividTx := []*protos.Asset{&UnDivisibleAsset1}
	duplicateIndividTx, duplicateIndividFees, duplicateIndividUtxoViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputDuplicateIndividTx,inputAssetDuplicateIndividTx,outputDuplicateIndividTx,outputAssetDuplicateIndividTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// Indivisible utxo duplicate spent: have the same output:
	inputDuplicateIndividOutTx := []int64{10000010,10000020}
	outputDuplicateIndividOutTx := []int64{10000010,10000010}
	inputAssetDuplicateIndividOutTx := []*protos.Asset{&UnDivisibleAsset1,&UnDivisibleAsset1}
	outputAssetDuplicateIndividOutTx := []*protos.Asset{&UnDivisibleAsset1,&UnDivisibleAsset1}
	duplicateIndividOutTx, duplicateIndividOutFees, duplicateIndividOutUtxoViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputDuplicateIndividOutTx,inputAssetDuplicateIndividOutTx,outputDuplicateIndividOutTx,outputAssetDuplicateIndividOutTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// Indivisible : the number of asset do not equal:
	inputIndividNoEqualTx := []int64{10000010,10000020,10000030}
	outputIndividNoEqualTx := []int64{10000010,10000020}
	inputAssetIndividNoEqualTx := []*protos.Asset{&DivisibleAsset1,&UnDivisibleAsset2,&UnDivisibleAsset3}
	outputAssetIndividNoEqualTx := []*protos.Asset{&DivisibleAsset1,&UnDivisibleAsset2}
	individNoEqualTx, individNoEqualFees, individNoEqualViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputIndividNoEqualTx,inputAssetIndividNoEqualTx,outputIndividNoEqualTx,outputAssetIndividNoEqualTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// Indivisible : the number amounts with same asset do not equal:
	inputIndividNoEqualAmountTx := []int64{10000010,20000010,10000020}
	outputIndividNoEqualAmountTx := []int64{10000010,10000020}
	inputAssetIndividNoEqualAmountTx := []*protos.Asset{&UnDivisibleAsset1,&UnDivisibleAsset1,&UnDivisibleAsset2}
	outputAssetIndividNoEqualAmountTx := []*protos.Asset{&UnDivisibleAsset1,&UnDivisibleAsset2}
	individNoEqualAmountTx, individNoEqualAmountFees, individNoEqualAmountViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,100000, inputIndividNoEqualAmountTx,inputAssetIndividNoEqualAmountTx,outputIndividNoEqualAmountTx,outputAssetIndividNoEqualAmountTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//the output with opcode is not the first output
	errorOpcodeTx, errorOpcodeFees, errorOpcodeViews, err := createErrorOpcodeTx(privString, payAddrPkScript, receiverPkScript)

	tests := []struct {
		tx                 *asiutil.Tx
		utxoView           *txo.UtxoViewpoint
		expectedResultFees map[protos.Asset]int64
		testErr            string
	}{
		//normal tx test://////////////////////////////////////////////////////////
		//test0:
		{
			coinBaseTx,
			nil,
			nil,
			"",
		},
		//test1:
		{
			vtxMsg,
			nil,
			nil,
			"",
		},
		//tx with divisible asset:
		//Test2:
		{
			asiutil.NewTx(normalTx1),
			utxoViewPoint1,
			resultFees1,
			"",
		},

		//Test3:
		{
			asiutil.NewTx(normalTx2),
			utxoViewPoint2,
			resultFees2,
			"",
		},

		//Test4:
		{
			asiutil.NewTx(normalTx3),
			utxoViewPoint3,
			resultFees3,
			"",
		},

		//Test5:
		{
			asiutil.NewTx(multTx4),
			utxoViewPoint4,
			resultFees4,
			"",
		},

		{
			asiutil.NewTx(UndMultTx1),
			UndView1,
			UndFees1,
			"",
		},

		{
			asiutil.NewTx(MixTx1),
			MixView1,
			MixFees1,
			"",
		},

		/////////////////////////////////////////////////////////////////
		//err tx with Undivisible asset: input.amount != output.amount
		{
			asiutil.NewTx(ErrMultTx1),
			ErrView1,
			ErrFees1,
			"ErrNoTxInputs",
		},

		//input.asset != output.asset
		{
			asiutil.NewTx(ErrMultTx2),
			ErrView2,
			ErrFees2,
			"ErrNoTxInputs",
		},

		//input.amount != output.amount
		{
			asiutil.NewTx(ErrMultTx3),
			ErrView3,
			ErrFees3,
			"ErrSpendTooHigh",
		},

		//input.asset != output.asset
		{
			asiutil.NewTx(ErrMultTx4),
			ErrView4,
			ErrFees4,
			"ErrAssetsNotEqual",
		},
		//utxo has already been spent
		{
			asiutil.NewTx(spentTx),
			spentViews,
			spentUtxoFees,
			"ErrMissingTxOut",
		},
		//use coinbase utxo: not Maturity
		{
			asiutil.NewTx(unMaturityTx),
			unMaturityViews,
			unMaturityFees,
			"ErrImmatureSpend",
		},
		//utxo.amount < 0
		{
			asiutil.NewTx(negativeUtxoTx),
			negativeUtxoViews,
			negativeUtxoFees,
			"ErrBadTxOutValue",
		},

		//utxo.amount > MaxXingUtxo
		{
			asiutil.NewTx(MaxUtxoTx),
			MaxUtxoViews,
			MaxUtxoFees,
			"ErrBadTxOutValue",
		},

		//Duplicate spent: the same input:
		{
			asiutil.NewTx(duplicateIndividTx),
			duplicateIndividUtxoViews,
			duplicateIndividFees,
			"ErrDuplicateTxInputs",
		},

		//Duplicate spent: the same output
		{
			asiutil.NewTx(duplicateIndividOutTx),
			duplicateIndividOutUtxoViews,
			duplicateIndividOutFees,
			"ErrDuplicateTxInputs",
		},

		//input do not equal to output
		{
			asiutil.NewTx(individNoEqualTx),
			individNoEqualViews,
			individNoEqualFees,
			"ErrAssetsNotEqual",
		},

		//the amount numbers with same asset  of input and output are not equal
		{
			asiutil.NewTx(individNoEqualAmountTx),
			individNoEqualAmountViews,
			individNoEqualAmountFees,
			"ErrAssetsNotEqual",
		},

		//the output with opcode is not the first output
		{
			asiutil.NewTx(errorOpcodeTx),
			errorOpcodeViews,
			errorOpcodeFees,
			"ErrInvalidOutput",
		},
	}

	t.Logf("Running %d TestCheckTransactionInputs tests", len(tests))
	for i, test := range tests {
		_, fees, err := CheckTransactionInputs(test.tx, nextBlkHeight, test.utxoView, chain)
		if err == nil {
			if fees != nil {
				for k, v := range *fees {
					if _, ok := test.expectedResultFees[k]; ok {
						if test.expectedResultFees[k] != v {
							t.Errorf("the result feeList[%v] = %v, but expected feeList[%v] = %v",
								k, v, k, test.expectedResultFees[k])
						}
					} else {
						t.Errorf("the %d test error: the result feeList[%v] = %v, " +
							"but expected feeList[%v] is not exist", i, k, v, k)
					}
					delete(test.expectedResultFees, k)
				}
			}
		}
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.testErr {
				t.Errorf("the %d test error: errCode mismatch: want:%v,but got %v",
					i, test.testErr, dbErr.ErrorCode.String())
			}
		}
		continue
	}
}


func TestCheckVTransactionInputs(t *testing.T) {
	privString := "0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e"
	acc, _ := crypto.NewAccount(privString)
	payAddrPkScript, _ := txscript.PayToAddrScript(acc.Address)

	//output priv:
	receiverPrivString := "0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2"
	recAcc, _ := crypto.NewAccount(receiverPrivString)
	receiverPkScript, _ := txscript.PayToAddrScript(recAcc.Address)

	//the txIn IsMintOrCreateInput:
	tx := protos.NewMsgTx(protos.TxVersion)
	tx.AddTxIn(&protos.TxIn{
		PreviousOutPoint: *protos.NewOutPoint(&common.Hash{}, asiutil.TransferCreationIdx),
		SignatureScript:  nil,
		Sequence:         protos.MaxTxInSequenceNum,
	})
	normalTx := asiutil.NewTx(tx)

	//test1: utxo已经被花费:
	spentTxTmp, spentUtxoFees, spentViews, _ := createSpentUtxoErrTx(privString, payAddrPkScript, receiverPkScript)
	spentTx := asiutil.NewTx(spentTxTmp)
	testScript := spentTx.MsgTx().TxIn[0].SignatureScript
	testScript[0] = 0xc3
	spentTx.MsgTx().TxIn[0].SignatureScript = testScript
	spentTx.Type()

	//utxo.amount < 0:
	inputNegativeUtxoTx := []int64{-100000000}
	outputNegativeUtxoTx := []int64{10000010}
	inputAssetNegativeUtxoTx := []*protos.Asset{&DivisibleAsset1}
	outputAssetNegativeUtxoTx := inputAssetNegativeUtxoTx
	negativeUtxoTx, negativeUtxoFees, negativeUtxoViews, err := createTestTx(privString, payAddrPkScript,
		payAddrPkScript,100000, inputNegativeUtxoTx, inputAssetNegativeUtxoTx,
		outputNegativeUtxoTx, outputAssetNegativeUtxoTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// input amount is bigger than MaxXingUtxo:
	amount := common.MaxXing
	inputMaxUtxoTx := []int64{int64(amount + 1000)}
	outputMaxUtxoTx := []int64{2}
	inputAssetMaxUtxoTx := []*protos.Asset{&DivisibleAsset1}
	outputAssetMaxUtxoTx := inputAssetMaxUtxoTx
	MaxUtxoTx, MaxUtxoFees, MaxUtxoViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,
		100000, inputMaxUtxoTx,inputAssetMaxUtxoTx,outputMaxUtxoTx,outputAssetMaxUtxoTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	//txOut.value < 0:
	inputNegativeOutTx := []int64{10000000000}
	outputNegativeOutTx := []int64{-100000000}
	inputAssetNegativeOutTx := []*protos.Asset{&DivisibleAsset1}
	outputAssetNegativeOutTx := inputAssetNegativeOutTx
	negativeOutTx, negativeOutFees, negativeOutViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,
		100000, inputNegativeOutTx,inputAssetNegativeOutTx,outputNegativeOutTx,outputAssetNegativeOutTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	// txOut value is bigger than MaxXing:
	inputMaxOutTx := []int64{10000000000}
	outputMaxOutTx := []int64{int64(amount + 1000)}
	inputAssetMaxOutTx := []*protos.Asset{&DivisibleAsset1}
	outputAssetMaxOutTx := inputAssetMaxOutTx
	MaxOutTx, MaxOutFees, MaxOutViews, err := createTestTx(privString, payAddrPkScript, payAddrPkScript,
		100000, inputMaxOutTx,inputAssetMaxOutTx,outputMaxOutTx,outputAssetMaxOutTx)
	if err != nil {
		t.Errorf("create createNormalTx error %v", err)
	}

	tests := []struct {
		tx                 *asiutil.Tx
		utxoView           *txo.UtxoViewpoint
		expectedResultFees map[protos.Asset]int64
		errCodeStr         string
	}{
		//test0: txIn is IsMintOrCreateInput
		{
			normalTx,
			nil,
			nil,
			"",
		},

		//test1: utxo has already been spent
		{
			spentTx,
			spentViews,
			spentUtxoFees,
			"ErrBadTxInput",
		},

		//test2: utxo.amount < 0
		{
			asiutil.NewTx(negativeUtxoTx),
			negativeUtxoViews,
			negativeUtxoFees,
			"ErrBadTxInput",
		},

		//test3: utxo.amount > MaxXingUtxo
		{
			asiutil.NewTx(MaxUtxoTx),
			MaxUtxoViews,
			MaxUtxoFees,
			"ErrBadTxInput",
		},

		//test4: TxOut.value < 0
		{
			asiutil.NewTx(negativeOutTx),
			negativeOutViews,
			negativeOutFees,
			"ErrBadTxOutValue",
		},

		//test5: txOut.value > MaxXing
		{
			asiutil.NewTx(MaxOutTx),
			MaxOutViews,
			MaxOutFees,
			"ErrBadTxOutValue",
		},

	}

	t.Logf("Running %d TestCheckVTransactionInputs tests", len(tests))
	for i, test := range tests {
		err := CheckVTransactionInputs(test.tx, test.utxoView)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errCodeStr {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v, but got: %v",
						i, test.errCodeStr, dbErr.ErrorCode.String())
				}
			}
		}
		continue
	}
}

func TestCheckConnectBlock(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e",  //privateKey0
	}
	accList, netParam, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, 10)
	if err != nil || chain == nil {
		t.Errorf("create block error %v", err)
	}
	defer teardownFunc()

	genesisNode := chain.bestChain.tip()
	validators, filters, _ := chain.GetValidatorsByNode(1,genesisNode)
	genesisBlk,_ := chain.BlockByHeight(0)
	genesisHash := genesisBlk.MsgBlock().Header.BlockHash()
	txGasLimit := uint32(6000000)

	//create 1 mainChainblocks:-----------------------------------------------
	blockList := make([]*asiutil.Block,0)
	nodeList := make([]*blockNode,0)
	tmpValidator := validators
	tmpFilters := filters
	tmpEpoch := uint32(1)
	tmpSlotIndex := uint16(0)
	for i:=0; i<7; i++ {
		normalTxList := make([]*asiutil.Tx, 0)
		if i != 0 {
			if i == int(netParam.RoundSize) {
				tmpSlotIndex = 0
				tmpEpoch += 1
				tmpValidator, tmpFilters, _ = chain.GetValidatorsByNode(tmpEpoch,chain.GetTip())
				log.Infof("validators2 = %v",tmpValidator)
			} else {
				tmpSlotIndex ++
			}
		}
		//test block with tx:
		if i == 5 || i == 6 {
			normalTx,_ := chain.createNormalTx(parivateKeyList[0], protos.Asset{0,0} ,
			accList[0].Address.StandardAddress(), 20000000, 500000, txGasLimit, nil)
			log.Infof("height = %d, txIn.PreviousOutPoint = %v",i+1,normalTx.MsgTx().TxIn[0].PreviousOutPoint)
			normalTxList = append(normalTxList,normalTx)
		}
		block, newNode, err := createAndSignBlock(netParam, accList, tmpValidator, tmpFilters, chain, tmpEpoch,
			tmpSlotIndex, int32(i), protos.Asset{0,0}, 0,
			tmpValidator[tmpSlotIndex],normalTxList,0, chain.GetTip())
		if err != nil {
			t.Errorf("create block error %v", err)
		}

		if i != 6 {
			isMain,isOrphan,checkErr := chain.ProcessBlock(block,nil, nil, nil, 0)
			if checkErr != nil {
				t.Errorf("ProcessBlock error %v", checkErr)
			}
			log.Infof("isMain = %v, isOrphan = %v",isMain, isOrphan)
		}
		blockList = append(blockList,block)
		nodeList = append(nodeList, newNode)
	}

	//test view hash is error:
	viewErrHash := txo.NewUtxoViewpoint()
	viewErrHash.SetBestHash(&genesisHash)

	//test gasLimit error:
	gasLimitErrBlk := blockList[3]
	gasLimitErrBlk.MsgBlock().Header.GasLimit = uint64(12)
	gasLimitErrNode := nodeList[3]
	log.Infof("gasLimitErrNode = %v",gasLimitErrNode)

	//test gasUsed error:
	gasUsedErrBlk := blockList[4]
	gasUsedErrBlk.MsgBlock().Header.GasUsed = uint64(123456)
	gasUsedErrNode := nodeList[4]
	log.Infof("gasUsedErrNode = %v",gasUsedErrNode)

	tests := []struct {
		block			*asiutil.Block
		node      		*blockNode
		view      		*txo.UtxoViewpoint
		errCodeString	string
	} {
		//block with only coinbaseTx:
		{
			blockList[0],
			nodeList[0],
			nil,
			"",
		},
		//test view hash is error:
		{
			blockList[2],
			nodeList[2],
			viewErrHash,
			"ErrHashMismatch",
		},
		{
			gasLimitErrBlk,
			gasLimitErrNode,
			nil,
			"ErrGasLimitOverFlow",
		},
		//total gas used mismatch:
		{
			gasUsedErrBlk,
			gasUsedErrNode,
			nil,
			"ErrGasMismatch",
		},
		//test block feespool error:
		{
			blockList[6],
			nodeList[6],
			nil,
			"ErrForbiddenAsset",
		},
	}

	t.Logf("Running %d TestCheckConnectBlock tests", len(tests))
	for i, test := range tests {
		header := test.block.MsgBlock().Header
		preNode := chain.index.LookupNode(&header.PrevBlock)
		if preNode == nil {
			t.Errorf("LookupNode error")
		}

		view := txo.NewUtxoViewpoint()
		view.SetBestHash(&preNode.hash)
		if test.view != nil {
			view = test.view
		}

		var receipts types.Receipts
		var allLogs []*types.Log
		var msgvblock protos.MsgVBlock
		receipts, allLogs, err = chain.checkConnectBlock(test.node, test.block, view,
			nil, nil)
		if err != nil {
			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errCodeString {
				if !ok {
					t.Errorf("the %d test error: can not get ErrorCode: %v", i, err)
				} else {
					t.Errorf("the %d test error: errCode mismatch: want:%v,but got %v",
						i, test.errCodeString, dbErr.ErrorCode.String())
					t.Errorf("did not received expected error in the %d test: " +
						"errCode mismatch: receipts = %v, allLogs = %v, msgvblock = %v",
						i,receipts,allLogs,msgvblock)
					log.Infof("error = %v",err)
				}
			}
		}
	}
}
