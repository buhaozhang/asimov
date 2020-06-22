// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package syscontract

import (
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math/big"
)

// GetFees returns an asset list and their valid heights
// asset in list are used as transaction fees
// heights describe the assets formally effective
// the asset list is submitted by members of validator committee
func (m *Manager) GetFees(
	block *asiutil.Block,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig) (map[protos.Asset]int32, error) {

	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), common.ValidatorCommittee)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %s, %d", common.ValidatorCommittee, block.Height())
		log.Error(errStr)
		panic(errStr)
	}

	feeListFunc := common.ContractValidatorCommittee_GetAssetFeeListFunction()

	runCode, err := fvm.PackFunctionArgs(contract.AbiInfo, feeListFunc)
	result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		common.SystemContractReadOnlyGas, common.ValidatorCommittee, runCode)
	if err != nil {
		log.Errorf("Get contract templates failed, error: %s", err)
		return nil, err
	}
	assets := make([]*big.Int, 0)
	height := make([]*big.Int, 0)
	outData := []interface{}{
		&assets,
		&height,
	}
	err = fvm.UnPackFunctionResult(contract.AbiInfo, &outData, feeListFunc, result)
	if err != nil {
		log.Errorf("Get fee list failed, error: %s", err)
		return nil, err
	}
	if len(assets) != len(height) {
		errStr := "get fee list failed, length of assets does not match length of height"
		log.Errorf(errStr)
		return nil, errors.New(errStr)
	}

	fees := make(map[protos.Asset]int32)
	for i := 0; i < len(assets); i++ {
		asset := protos.AssetFromBytes(assets[i].Bytes())
		fees[*asset] = int32(height[i].Int64())
	}

	return fees, nil
}
