// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package syscontract

import (
	"encoding/json"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"sync"
)

// Manager defines an contract manager that manages multiple system contracts and
// implements the blockchain.ContractManager interface so it can be seamlessly
// plugged into normal chain processing.
type Manager struct {
	chain fvm.ChainContext
	// genesis transaction data cache
	genesisDataCache map[common.Address][]chaincfg.ContractInfo

	// unrestricted assets cache
	assetsUnrestrictedMtx   sync.Mutex
	assetsUnrestrictedCache map[protos.Asset]struct{}
	checkLimit  func(block *asiutil.Block, stateDB vm.StateDB, asset *protos.Asset) int
}

// Init manager by genesis data.
func (m *Manager) Init(chain fvm.ChainContext, dataBytes [] byte) error {
	var cMap map[string][]chaincfg.ContractInfo
	err := json.Unmarshal(dataBytes, &cMap)
	if err != nil {
		return err
	}
	m.chain = chain
	m.genesisDataCache = make(map[common.Address][]chaincfg.ContractInfo)
	for k, v := range cMap {
		m.genesisDataCache[common.HexToAddress(k)] = v
	}
	m.assetsUnrestrictedCache = make(map[protos.Asset]struct{})
	m.checkLimit = m.isLimit
	return nil
}

func (m *Manager) IsLimitInCache(asset *protos.Asset) bool {
	m.assetsUnrestrictedMtx.Lock()
	defer m.assetsUnrestrictedMtx.Unlock()
	if _, ok := m.assetsUnrestrictedCache[*asset]; ok {
		return true
	}
	return false
}

// Get latest contract by height.
func (m *Manager) GetActiveContractByHeight(height int32, delegateAddr common.Address) *chaincfg.ContractInfo {
	contracts, ok := m.genesisDataCache[delegateAddr]
	if !ok {
		return nil
	}
	for i := len(contracts) - 1; i >= 0; i-- {
		if height >= contracts[i].BlockHeight {
			return &contracts[i]
		}
	}
	return nil
}

// NewContractManager returns an empty struct of Manager
func NewContractManager() ainterface.ContractManager {
    return &Manager {}
}

// Ensure the Manager type implements the blockchain.ContractManager interface.
var _ ainterface.ContractManager = (*Manager)(nil)