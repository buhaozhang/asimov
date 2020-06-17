// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package syscontract

import (
	"fmt"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math/big"
)

// GetSignedUpValidators returns validators and their rounds information
// by calling system contract of consensus_satoshiplus.
// some of these validators will mine next round by checking their rounds
// if match our mining rules.
// for rules, one validator may offline. he must sign up to update his round to newest.
// if not, he will lose the chance to mine for the chain
func (m *Manager) GetSignedUpValidators(
	proxyAddr common.Address,
	block *asiutil.Block,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	miners []string) ([]common.Address, []uint32, error) {

	gas := uint64(common.SystemContractReadOnlyGas)
	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), proxyAddr)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %v, %d", proxyAddr, block.Height())
		log.Error(errStr)
		panic(errStr)
	}
	abi := contract.AbiInfo

	// function name of contract consensus_satoshiplus
	funcName := common.ContractConsensusSatoshiPlus_GetSignupValidatorsFunction()

	// pack function params, ready for calling
	runCode, err := fvm.PackFunctionArgs(abi, funcName, miners)

	result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		gas, proxyAddr, runCode)
	if err != nil {
		log.Errorf("Get signed up validators failed, error: %s", err)
		return nil, nil, err
	}

	validators := make([]common.Address, 0)
	rounds := make([]*big.Int, 0)
	outData := []interface{}{
		&validators,
		&rounds,
	}

	// unpack results
	err = fvm.UnPackFunctionResult(abi, &outData, funcName, result)
	if err != nil {
		log.Errorf("Get signed up validators failed, error: %s", err)
		return nil, nil, err
	}

	if len(validators) != len(rounds) || (miners != nil && len(miners) != len(rounds)) {
		errStr := "get signed up validators failed, length of validators does not match length of height"
		log.Errorf(errStr)
		return nil, nil, common.AssertError(errStr)
	}

	round32 := make([]uint32, len(rounds))
	for i, h := range rounds {
		round := uint32(h.Uint64())
		round32[i] = round
	}

	return validators, round32, nil
}
