// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"testing"
)

func TestCalculateBalance(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e", //privateKey0
	}
	accList, _, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, chaincfg.MainNetParams.RoundSize)
	defer teardownFunc()
	acc := accList[0]

	var testBookkeepers = []common.Address {
		{0x66,0xe3,0x05,0x4b,0x41,0x10,0x51,0xda,0x54,0x92,0xae,0xc7,0xa8,0x23,0xb0,0x0c,0xb3,0xad,0xd7,0x72,0xd7,}, //address by privateKey0
		{0x66,0x3c,0xf8,0xb8,0x65,0xf2,0xf7,0xe5,0x22,0xff,0x63,0x90,0x59,0xe0,0xa4,0x37,0xc8,0x49,0xee,0x5a,0xb0,},
	}

	var testAsset = []protos.Asset{
		{0,0},
		{1,1},
	}
	var testAmount = []int64{
		10000000000,
		111111111,
		222222222,
		-2,
	}
	var resultAmount = []int64{
		int64(float64(testAmount[0]) * (1-common.CoreTeamPercent)),
		111111111,
		0,
		1,
	}

	var block *asiutil.Block
	//add 2 block to bestChain:
	for i:=0; i<2; i++ {
		block, err = createTestBlock(chain, 1, uint16(i), chain.bestChain.height(), testAsset[i], testAmount[i],
			acc.Address,nil, chain.bestChain.tip())
		if err != nil {
			t.Errorf("create block error %v", err)
		}
		view := txo.NewUtxoViewpoint()
		txNums := len(block.MsgBlock().Transactions)
		for k:=0; k<txNums; k++ {
			tx := asiutil.NewTx(block.MsgBlock().Transactions[k])
			_ = view.AddTxOuts(tx.Hash(), tx.MsgTx(), k+1 == txNums, block.Height())
		}

		err = chain.db.Update(func(dbTx database.Tx) error {
			// Update the balance using the state of the utxo view.
			err = dbPutBalance(dbTx, view)
			if err != nil {
				return err
			}
			err = dbPutUtxoView(dbTx, view)
			if err != nil {
				return err
			}
			return nil
		})

		//update bestchain info:
		nodebest := chain.bestChain.tip()
		if nodebest == nil {
			t.Errorf("tests  error %v", err)
		}
		nodeErr := chain.addBlockNode(block)
		if nodeErr != nil {
			t.Errorf("tests error %v", err)
		}
	}

	tests := []struct {
		addr          common.Address
		asset         *protos.Asset
		voucherId     int64
		ErrStr        string
		resultBalance int64
	} {
		//input asset is nil
		{
			testBookkeepers[0],
			nil,
			0,
			"",
			0,
		},
		//input Divisible asset
		{
			testBookkeepers[0],
			&testAsset[0],
			0,
			"",
			resultAmount[0],
		},
		//input inDivisible asset
		{
			testBookkeepers[0],
			&testAsset[1],
			testAmount[1],
			"",
			resultAmount[1],
		},
		//input inDivisible asset
		{
			testBookkeepers[0],
			&testAsset[1],
			testAmount[2],
			"",
			resultAmount[2],
		},
		//input inDivisible asset
		{
			testBookkeepers[0],
			&testAsset[1],
			testAmount[3],
			"",
			resultAmount[3],
		},
		//input addr with no utxo
		{
			testBookkeepers[1],
			&testAsset[1],
			0,
			"",
			0,
		},
	}

	for i, input := range tests {
		t.Logf("==========test case %d==========", i)
		balance, err := chain.CalculateBalance(nil, block, input.addr, input.asset, input.voucherId)
		if err != nil {
			t.Errorf("tests #%d error: %v", i, err)
		} else {
			if balance != input.resultBalance {
				t.Errorf("tests #%d error: balance is not correct: want balance is %v, but got is %v",
				    i, input.resultBalance, balance)
			}
		}
	}
	t.Log("TestCalculateBalance finish")
}
