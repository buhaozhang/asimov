// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package poa

import (
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
)

//manage how validators will create blocks within a round.
//validators may change between different round.
type RoundManager struct {
}

func (m *RoundManager) HasValidator(validator common.Address) bool {
	return true
}

func (m *RoundManager) GetValidators(blockHash common.Hash, round uint32, fn ainterface.GetValidatorsCallBack) (
	[]*common.Address, map[common.Address]uint16, error) {
	if fn == nil {
		return nil, nil, nil
	}
	signupValidators, _, err := fn(nil)
	if err != nil {
		return nil, nil, err
	}
	validators := make([]*common.Address, chaincfg.ActiveNetParams.RoundSize)
	weightmap := make(map[common.Address]uint16)
	for _, v := range signupValidators {
		weightmap[v] = 1
	}
	l := len(signupValidators)
	for i := 0; i < int(chaincfg.ActiveNetParams.RoundSize); i++ {
		validators[i] = &signupValidators[i%l]
	}
	return validators, weightmap, nil
}

func (m *RoundManager) GetHsMappingByRound(round uint32) (map[string]*ainterface.ValidatorInfo, error) {
	return nil, nil
}

func (m *RoundManager) GetNextRound(round *ainterface.Round) (*ainterface.Round, error) {
	newRound := &ainterface.Round{
		Round:          round.Round + 1,
		RoundStartUnix: round.RoundStartUnix + round.Duration,
		Duration:       common.DefaultBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
	}

	return newRound, nil
}

func (m *RoundManager) GetRoundInterval(round int64) int64 {
	return common.DefaultBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize)
}

func (m *RoundManager) Init(round uint32, db database.Transactor, c ainterface.IBtcClient) error {
	return nil
}

func (m *RoundManager) Start() {
}

func (m *RoundManager) Halt() {
}

func (m *RoundManager) GetContract() common.Address {
	return common.ConsensusPOA
}

func NewRoundManager() *RoundManager {
	return &RoundManager{}
}
