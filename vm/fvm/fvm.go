// Copyright (c) 2018-2020 The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package fvm

import (
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/virtualtx"
	fvm "github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math/big"
)

// ChainContext supports retrieving headers and consensus parameters from the
// current blockchain, which is used during transaction processing.
type ChainContext interface {
	CalculateBalance(view *txo.UtxoViewpoint, block *asiutil.Block, address common.Address, assets *protos.Asset, voucherId int64) (int64, error)
	GetVmConfig() *fvm.Config
	GetTemplateWarehouseInfo() (common.Address, string)
	GetSystemContractInfo(delegateAddr common.Address) (common.Address, []byte, string)
	GetTemplateInfo(contractAddr []byte, gas uint64, block *asiutil.Block, stateDB fvm.StateDB, chainConfig *params.ChainConfig)(uint16, string, uint64)
	FetchTemplate(view *txo.UtxoViewpoint, hash *common.Hash) (uint16, []byte, []byte, []byte, []byte, error)
	BlockHashByHeight(int32) (*common.Hash, error)
}

// NewFVMContext creates a new context of FVM.
func NewFVMContext(from common.Address, gasPrice *big.Int, block *asiutil.Block, chain ChainContext,
	view *txo.UtxoViewpoint,
	voteValue fvm.VoteValueFunc) fvm.Context {

	return fvm.Context{
		CanTransfer:              CanTransfer,
		Transfer:                 Transfer,
		GetHash:                  GetHashFn(chain),
		CalculateBalance:         chain.CalculateBalance,
		GetTemplateWarehouseInfo: chain.GetTemplateWarehouseInfo,
		PackFunctionArgs:         PackFunctionArgs,
		UnPackFunctionResult:     UnPackFunctionResult,
		GetSystemContractInfo:    chain.GetSystemContractInfo,
		FetchTemplate:            chain.FetchTemplate,
		VoteValue:                voteValue,
		Origin:                   from,
		BlockNumber: 			  new(big.Int).SetInt64(int64(block.Height())),
		Round:       			  new(big.Int).SetInt64(int64(block.Round())),
		Time:        			  new(big.Int).SetInt64(block.MsgBlock().Header.Timestamp),
		Coinbase:	 			  block.MsgBlock().Header.CoinBase,
		Difficulty: 			  common.Big0,
		GasLimit: 				  block.MsgBlock().Header.GasLimit,
		GasPrice: 				  new(big.Int).Set(gasPrice),
		Block:    				  block,
		View:   				  view,
	}
}

// CanTransfer checks whether there are enough funds in the address' account to make a transfer.
// This does not take the necessary gas in to account to make the transfer valid.
func CanTransfer(view *txo.UtxoViewpoint, block *asiutil.Block, db fvm.StateDB, addr common.Address,
	amount *big.Int, vtx *virtualtx.VirtualTransaction, calculateBalanceFunc fvm.CalculateBalanceFunc, assets *protos.Asset) bool {
	if amount.Cmp(common.Big0) == 0 {
		return true
	}
	if assets == nil {
		return false
	}
	if assets.IsIndivisible() {
		if amount.Cmp(common.Big0) < 0 || amount.Cmp(common.BigMaxint64) > 0 {
			return false
		}
		total := vtx.GetIncoming(addr, assets, amount.Int64())
		if total.Cmp(amount) == 0 {
			return true
		}
		balance, _ := calculateBalanceFunc(view, block, addr, assets, amount.Int64())
		return amount.Cmp(big.NewInt(balance)) == 0
	} else {
		if amount.Cmp(common.Big0) < 0 || amount.Cmp(common.BigMaxxing) > 0 {
			return false
		}

		//check if there's incoming in the previous transfers from the same contract call.
		total := vtx.GetIncoming(addr, assets, amount.Int64())
		if total.Cmp(amount) >= 0 {
			return true
		}

		//now check the balance.
		balance, err := calculateBalanceFunc(view, block, addr, assets, amount.Int64())
		if err != nil {
			return false
		}

		total.Add(total, big.NewInt(balance))
		return total.Cmp(amount) >= 0
	}
}

// Transfer subtracts amount from sender and adds amount to recipient using the given Db
func Transfer(db fvm.StateDB, sender, recipient common.Address, amount *big.Int, vtx *virtualtx.VirtualTransaction, assets *protos.Asset) {
	// append virtual transaction
	if assets != nil && amount.Int64() > 0 {
		vtx.AppendVTransfer(sender, recipient, amount, assets)
	}
}

// GetHashFn returns a GetHashFunc which retrieves header hashes by number
func GetHashFn(chain ChainContext) func(n uint64) common.Hash {
	return func(n uint64) common.Hash {
		blockHeight := int32(n)
		if uint64(blockHeight) != n {
			return common.Hash{}
		}
		if hash, _ := chain.BlockHashByHeight(blockHeight); hash != nil {
			return *hash
		}
		return common.Hash{}
	}
}
