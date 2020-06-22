// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package satoshiplus

import (
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/crypto"
	"testing"
	"time"
)

func TestSlotControl(t *testing.T) {
	privString := "0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25"
	netParam := chaincfg.MainNetParams
	netParam.GenesisCandidates = []common.Address{
		{0x66, 0xe3, 0x05, 0x4b, 0x41, 0x10, 0x51, 0xda, 0x54, 0x92, 0xae, 0xc7, 0xa8, 0x23, 0xb0, 0x0c, 0xb3, 0xad, 0xd7, 0x72, 0xd7,}, //address by privateKey0
		{0x66, 0x3c, 0xf8, 0xb8, 0x65, 0xf2, 0xf7, 0xe5, 0x22, 0xff, 0x63, 0x90, 0x59, 0xe0, 0xa4, 0x37, 0xc8, 0x49, 0xee, 0x5a, 0xb0,}, //address by privateKey1
		{0x66, 0x94, 0xc9, 0x33, 0x03, 0xac, 0x66, 0x05, 0x90, 0x1e, 0x08, 0xb0, 0x33, 0xd1, 0x30, 0xe0, 0x01, 0xbd, 0xeb, 0x91, 0x57,}, //address by privateKey2
	}

	SlotControlConsensusConfig, teardownFunc, err := createPoaConfig(privString, &netParam)
	if err != nil {
		t.Errorf("createPoaConfig error %v", err)
	}
	defer teardownFunc()
	validatorAccounts := make(map[common.Address]*crypto.Account)
	acc0, _ := crypto.NewAccount("0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e")
	validatorAccounts[ chaincfg.ActiveNetParams.GenesisCandidates[0]] = acc0
	acc1, _ := crypto.NewAccount("0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2")
	validatorAccounts[ chaincfg.ActiveNetParams.GenesisCandidates[1]] = acc1
	acc2, _ := crypto.NewAccount("0x77366e621236e71a77236e0858cd652e92c3af0908527b5bd1542992c4d7cace")
	validatorAccounts[ chaincfg.ActiveNetParams.GenesisCandidates[2]] = acc2

	tests := []struct {
		roundChange  bool
		isBookkeeper bool
		wantRound    int64
		wantSolt     int64
	}{
		//测试周期切换情况(round += 1):--------------------------
		{
			true,
			true, //当前peer在bookkeepers列表中
			1,
			0,
		},
		{
			false,
			false, //当前peer不在bookkeepers列表中
			1,
			1,
		},
		{
			false,
			true, //当前peer不bookkeepers列表中
			1,
			2,
		},
	}

	t.Logf("Running %d tests", len(tests))
	consensusCfg := *SlotControlConsensusConfig
	//创建poaServer:
	ps, err := NewSatoshiPlusService(&consensusCfg)
	if err != nil {
		t.Errorf("tests error %v", err)
	}
	ps.context.Round = 0
	ps.context.Slot = 0
	acc := ps.config.Account

	for i, test := range tests {
		if test.wantRound > 0 {
			validators, _, err := ps.getValidators(uint32(test.wantRound), false)
			if err != nil {
				log.Errorf("tests #%d [getValidators] %v", i, err.Error())
			}
			validator := validators[test.wantSolt]

			if test.isBookkeeper {
				ps.config.Account = validatorAccounts[*validator]
			} else {
				ps.config.Account = acc
			}
		}
		if test.roundChange {
			ps.context.Slot = int64(chaincfg.ActiveNetParams.RoundSize) - 1
		}

		turn := ps.checkTurn(test.wantSolt, test.wantRound, false)
		if turn != test.isBookkeeper {
			t.Errorf("tests #%d error,get turn: %v, but want %v", i, turn, test.isBookkeeper)
		}
	}
}

func TestGenblock(t *testing.T) {
	privString := "0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25"
	netParam := chaincfg.MainNetParams
	netParam.GenesisCandidates = []common.Address{
		{0x66, 0xe3, 0x05, 0x4b, 0x41, 0x10, 0x51, 0xda, 0x54, 0x92, 0xae, 0xc7, 0xa8, 0x23, 0xb0, 0x0c, 0xb3, 0xad, 0xd7, 0x72, 0xd7,}, //address by privateKey0
		{0x66, 0x3c, 0xf8, 0xb8, 0x65, 0xf2, 0xf7, 0xe5, 0x22, 0xff, 0x63, 0x90, 0x59, 0xe0, 0xa4, 0x37, 0xc8, 0x49, 0xee, 0x5a, 0xb0,}, //address by privateKey1
		{0x66, 0x94, 0xc9, 0x33, 0x03, 0xac, 0x66, 0x05, 0x90, 0x1e, 0x08, 0xb0, 0x33, 0xd1, 0x30, 0xe0, 0x01, 0xbd, 0xeb, 0x91, 0x57,}, //address by privateKey2
	}
	consensusConfig, teardownFunc, err := createPoaConfig(privString, &netParam)
	if err != nil {
		t.Errorf("createPoaConfig error %v", err)
	}
	defer teardownFunc()
	validatorAccounts := make(map[common.Address]*crypto.Account)
	acc0, _ := crypto.NewAccount("0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e")
	validatorAccounts[ chaincfg.ActiveNetParams.GenesisCandidates[0]] = acc0
	acc1, _ := crypto.NewAccount("0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2")
	validatorAccounts[ chaincfg.ActiveNetParams.GenesisCandidates[1]] = acc1
	acc2, _ := crypto.NewAccount("0x77366e621236e71a77236e0858cd652e92c3af0908527b5bd1542992c4d7cace")
	validatorAccounts[ chaincfg.ActiveNetParams.GenesisCandidates[2]] = acc2

	tests := []struct {
		isBookkeeper bool
		wantRound    uint32
		wantSolt     uint16
		wantErr      bool
	}{
		{
			true, //当前peer在bookkeepers列表中
			1,
			0,
			false,
		},
		{
			false, //当前peer不在bookkeepers列表中
			1,
			0,
			false,
		},
	}

	//创建poaServer:
	ps, err := NewSatoshiPlusService(consensusConfig)
	if err != nil {
		t.Errorf("tests NewSatoshiPlusService error %v", err)
	}
	ps.blockTimer = time.NewTimer(time.Hour)
	ps.context.Round = 0
	ps.context.Slot = int64(chaincfg.ActiveNetParams.RoundSize) - 1
	acc := ps.config.Account

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		//设置curpeer地址为非bookkeeper地址:
		if test.wantRound > 0 {
			validators, _, err := ps.getValidators(test.wantRound, false)
			if err != nil {
				log.Errorf("tests #%d [getValidators] %v", i, err.Error())
			}
			validator := validators[test.wantSolt]

			if test.isBookkeeper {
				ps.config.Account = validatorAccounts[*validator]
			} else {
				ps.config.Account = acc
			}
		}

		oldContest := ps.context
		ps.handleBlockTimeout()
		ps.context = oldContest
		continue
	}
}

func TestGetRoundInfo(t *testing.T) {
	privString := "0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25"
	consensusConfig, teardownFunc, err := createPoaConfig(privString, &chaincfg.DevelopNetParams)
	if err != nil {
		t.Errorf("createPoaConfig error %v", err)
	}
	defer teardownFunc()

	ps, err := NewSatoshiPlusService(consensusConfig)
	if err != nil {
		t.Errorf("tests NewSatoshiPlusService error %v", err)
	}

	blockTime := time.Now().Unix()
	blockTime = 0
	roundSizei64 := int64(chaincfg.ActiveNetParams.RoundSize)
	tests := []struct {
		blockRound int64
		targetTime int64
		wantRound  int64
		wantSlot   int64
		wantTime   int64
		wantErr    bool
	}{
		{
			1,
			blockTime + common.DefaultBlockInterval*51 - 1,
			1,
			50,
			blockTime,
			false,
		}, {
			1,
			blockTime,
			1,
			0,
			blockTime,
			false,
		}, {
			2,
			blockTime,
			2,
			0,
			blockTime,
			false,
		}, {
			1,
			blockTime + 1,
			1,
			0,
			blockTime,
			false,
		}, {
			2,
			blockTime + 1,
			2,
			0,
			blockTime,
			false,
		}, {
			1,
			blockTime + common.DefaultBlockInterval,
			1,
			1,
			blockTime,
			false,
		}, {
			1,
			blockTime + common.DefaultBlockInterval * roundSizei64,
			2,
			0,
			blockTime + common.DefaultBlockInterval * roundSizei64,
			false,
		}, {
			2,
			blockTime + common.DefaultBlockInterval,
			2,
			1,
			blockTime,
			false,
		}, {
			1,
			blockTime + common.DefaultBlockInterval*(roundSizei64-1),
			1,
			roundSizei64 - 1,
			blockTime,
			false,
		}, {
			1,
			blockTime + common.DefaultBlockInterval*(roundSizei64-1) + 1,
			1,
			roundSizei64 - 1,
			blockTime,
			false,
		}, {
			1,
			blockTime + 5337,
			12,
			10,
			blockTime + 5280,
			false,
		}, {
			2,
			blockTime + 76665,
			64,
			roundSizei64 - 1,
			blockTime + 74880,
			false,
		}, {
			2,
			blockTime + 76665 + common.MaxBlockInterval - 1,
			64,
			roundSizei64 - 1,
			blockTime + 74880,
			false,
		}, {
			64,
			blockTime - 76665,
			0,
			0,
			0,
			true,
		}, {
			2,
			blockTime + 76665 + common.MaxBlockInterval,
			65,
			0,
			blockTime + 76665 + common.MaxBlockInterval,
			false,
		}, {
			2,
			blockTime + 76665 + common.MaxBlockInterval + 1,
			0,
			0,
			0,
			true, //test only 64 round
		},
	}
	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		ps.context.RoundStartTime = blockTime
		ps.context.RoundInterval = ps.config.RoundManager.GetRoundInterval(test.blockRound)
		ps.context.Round = test.blockRound
		round, slot, roundStartTime, err := ps.getRoundInfo(test.targetTime)
		if test.wantErr != (err != nil) {
			t.Errorf("tests #%d error: %v", i, err)
			continue
		}
		if round != test.wantRound || slot != test.wantSlot || roundStartTime != test.wantTime {
			t.Errorf("tests #%d fail, get round:%v, slot:%v, roundtime:%v;" +
				" want round:%v, slot:%v, roundtime:%v", i, round, slot, roundStartTime,
				test.wantRound, test.wantSlot, test.wantTime)
		}
	}
}

func TestGetRoundIntervalByRound(t *testing.T) {
	privString := "0x224828e95689e30a8e668418f968260edc6aa78ae03eed607f49288d99123c25"
	consensusConfig, teardownFunc, err := createPoaConfig(privString, &chaincfg.MainNetParams)
	if err != nil {
		t.Errorf("createPoaConfig error %v", err)
	}
	defer teardownFunc()

	ps, err := NewSatoshiPlusService(consensusConfig)
	if err != nil {
		t.Errorf("tests NewSatoshiPlusService error %v", err)
	}

	tests := []struct {
		round        int64
		wantInterval int64
	}{
		{
			0, common.DefaultBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		}, {
			1, common.DefaultBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		}, {
			2, common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		}, {
			10, common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		}, {
			30, common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		}, {
			50, common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		}, {
			60, common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize),
		},
	}

	for i, test := range tests {
		interval := ps.config.RoundManager.GetRoundInterval(test.round)
		if interval != test.wantInterval {
			t.Errorf("TestGetRoundIntervalByRound #%d want %v, interval: %v",
				i, test.wantInterval, interval)
		}
	}
}
