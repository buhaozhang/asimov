// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package syscontract

import (
	"encoding/binary"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math/big"
)

// GetContractAddressByAsset returns organization addresses by calling system contract of registry
// according to parameter assets
func (m *Manager) GetContractAddressByAsset(
	gas uint64,
	block *asiutil.Block,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	assets []string) ([]common.Address, bool, uint64) {

	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), common.RegistryCenter)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %s, %d", common.RegistryCenter, block.Height())
		log.Error(errStr)
		panic(errStr)
	}

	// function name of contract registry
	funcName := common.ContractRegistryCenter_GetOrganizationAddressesByAssetsFunction()
	assetIds := make([]uint64, 0, 1)
	for _, asset := range assets {
		assetBytes := common.Hex2Bytes(asset)
		assetIds = append(assetIds, binary.BigEndian.Uint64(assetBytes[4:]))
	}

	// pack function params, ready for calling
	runCode, err := fvm.PackFunctionArgs(contract.AbiInfo, funcName, assetIds)

	// call function
	result, leftOverGas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		gas, common.RegistryCenter, runCode)
	if err != nil {
		log.Errorf("Get  contract address of asset failed, error: %s", err)
		return nil, false, leftOverGas
	}

	var outType []common.Address

	// unpack results
	err = fvm.UnPackFunctionResult(contract.AbiInfo, &outType, funcName, result)
	if err != nil {
		log.Errorf("Get contract address of asset failed, error: %s", err)
		return nil, false, leftOverGas
	}

	return outType, true, leftOverGas
}

// GetAssetInfoByAssetId returns asset information by parameter asset id
// asset id is consist of organization id and asset index
func (m *Manager) GetAssetInfoByAssetId(
	block *asiutil.Block,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	assets []string) ([]ainterface.AssetInfo, error) {

	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), common.RegistryCenter)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %s, %d", common.RegistryCenter, block.Height())
		log.Error(errStr)
		panic(errStr)
	}

	// function name of contract registry
	funcName := common.ContractRegistryCenter_GetAssetInfoByAssetIdFunction()

	results := make([]ainterface.AssetInfo, 0)
	var err error
	for _, asset := range assets {
		assetBytes := common.Hex2Bytes(asset)
		_, orgId, assetIndex := protos.AssetDetailFromBytes(assetBytes)

		runCode, err := fvm.PackFunctionArgs(contract.AbiInfo, funcName, orgId, assetIndex)
		result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
			common.SystemContractReadOnlyGas, common.RegistryCenter, runCode)
		if err != nil {
			log.Errorf("Get  asset inf failed, error: %s", err)
		}

		var outType = &[]interface{}{new(bool), new(string), new(string), new(string), new(*big.Int), new([]*big.Int)}

		err = fvm.UnPackFunctionResult(contract.AbiInfo, outType, funcName, result)
		if err != nil {
			log.Errorf("Unpack asset info result failed, error: %s", err)
		}

		ret := ainterface.AssetInfo{
			Exist:       *((*outType)[0]).(*bool),
			Name:        *((*outType)[1]).(*string),
			Symbol:      *((*outType)[2]).(*string),
			Description: *((*outType)[3]).(*string),
			Total:       (*((*outType)[4]).(**big.Int)).Uint64(),
		}
		history := *((*outType)[5]).(*[]*big.Int)

		for _, h := range history {
			ret.History = append(ret.History, h.Uint64())
		}
		results = append(results, ret)
	}

	return results, err
}

// IsLimit returns a number of int type by find in memory or calling system
// contract of registry the number represents if an asset is restricted
func (m *Manager) IsLimit(block *asiutil.Block,
	stateDB vm.StateDB, asset *protos.Asset) int {

	if *asset == asiutil.AsimovAsset {
		return -1
	}

	if m.IsLimitInCache(asset) {
		return 0
	}
	limit := m.checkLimit(block, stateDB, asset)

	if limit == 0 && block.IsSigned() {
		m.assetsUnrestrictedMtx.Lock()
		defer m.assetsUnrestrictedMtx.Unlock()
		m.assetsUnrestrictedCache[*asset] = struct{}{}
	}

	return limit
}

// isLimit returns a number of int type by calling system contract of registry
// the number represents if an asset is restricted
func (m *Manager) isLimit(block *asiutil.Block,
	stateDB vm.StateDB, asset *protos.Asset) int {
	officialAddr := chaincfg.OfficialAddress
	_, organizationId, assetIndex := asset.AssetFields()

	input := common.PackIsRestrictedAssetInput(organizationId, assetIndex)
	result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain,
		stateDB, chaincfg.ActiveNetParams.FvmParam,
		common.ReadOnlyGas, common.RegistryCenter, input)
	if err != nil {
		log.Error(err)
		return -1
	}

	existed, limit, err := common.UnPackIsRestrictedAssetResult(result)
	if err != nil {
		log.Error(err)
		return -1
	}

	if !existed {
		return -1
	}
	if limit {
		return 1
	}
	return 0
}

// IsSupport returns a bool result, which represents if a restricted asset can be transfer
func (m *Manager) IsSupport(block *asiutil.Block,
	stateDB vm.StateDB, gasLimit uint64, asset *protos.Asset, address []byte) (bool, uint64) {

	if gasLimit < common.SupportCheckGas {
		return false, 0
	}

	// step1: prepare parameters for calling system contract to get organization address
	_, organizationId, assetIndex := asset.AssetFields()
	caller := chaincfg.OfficialAddress

	input := common.PackGetOrganizationAddressByIdInput(organizationId, assetIndex)
	result, leftOverGas, err := fvm.CallReadOnlyFunction(caller, block, m.chain,
		stateDB, chaincfg.ActiveNetParams.FvmParam,
		common.SupportCheckGas, common.RegistryCenter, input)
	if err != nil {
		log.Error(err)
		return false, gasLimit - common.SupportCheckGas + leftOverGas
	}

	// check if return valid organization address
	organizationAddress := common.BytesToAddress(result)
	if common.EmptyAddressValue == organizationAddress.String() {
		return false, gasLimit - common.SupportCheckGas + leftOverGas
	}

	// step2: call canTransfer method to check if the asset can be transfer
	transferInput := common.PackCanTransferInput(address, assetIndex)

	result2, leftOverGas2, _ := fvm.CallReadOnlyFunction(caller, block, m.chain,
		stateDB, chaincfg.ActiveNetParams.FvmParam,
		common.ReadOnlyGas, organizationAddress, transferInput)

	support, err := common.UnPackBoolResult(result2)
	if err != nil {
		log.Error(err)
	}

	return support, gasLimit - common.SupportCheckGas + leftOverGas + leftOverGas2 - common.ReadOnlyGas
}

//DisconnectBlock delete unrestricted asset of cache which is in the block.
func (m *Manager) DisconnectBlock(block *asiutil.Block) {
	m.assetsUnrestrictedMtx.Lock()
	defer m.assetsUnrestrictedMtx.Unlock()
	m.assetsUnrestrictedCache = make(map[protos.Asset]struct{})
}