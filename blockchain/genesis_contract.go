// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"github.com/AsimovNetwork/asimov/common"
)


// GetSystemContractInfo returns delegate addresses, original addresses and abi information of system contracts
func (b *BlockChain) GetSystemContractInfo(delegateAddr common.Address) (common.Address, []byte, string) {
	snapshot := b.BestSnapshot()
	var blockHeight int32 = 0
	if snapshot != nil {
		blockHeight = snapshot.Height
	}

	contract := b.contractManager.GetActiveContractByHeight(blockHeight, delegateAddr)
	if contract == nil {
		return common.Address{}, nil, ""
	}

	return delegateAddr, contract.Address, contract.AbiInfo
}

// GetTemplateWarehouseInfo returns two values.
// @return templateWarehouseAddr the address of system contract named templateWarehouse
//         templateWarehouseAbi  abi of system contract named templateWarehouse
// This method is used in fvm
func (b *BlockChain) GetTemplateWarehouseInfo() (common.Address, string) {
	templateWarehouseAddr, _, templateWarehouseAbi := b.GetSystemContractInfo(common.TemplateWarehouse)
	return templateWarehouseAddr, templateWarehouseAbi
}

// GetConsensusMiningInfo returns consensus system contract's address and abi
func (b *BlockChain) GetConsensusMiningInfo() (common.Address, string) {
	proxyAddress, _, abi := b.GetSystemContractInfo(b.roundManager.GetContract())
	return proxyAddress, abi
}
