// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package satoshiplus

import (
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/consensus/satoshiplus/minersync"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestGetMinersByRound(t *testing.T) {
	cfg := &chaincfg.FConfig{
		DataDir: chaincfg.DefaultDataDir,
	}
	cfg.DataDir = cleanAndExpandPath(cfg.DataDir)
	cfg.DataDir = filepath.Join(cfg.DataDir, "devNetUnitTest")
	// Load the block database.
	db, err := loadBlockDB(cfg)
	if err != nil {
		t.Errorf("loadBlockDB err %v", err)
		return
	}

	defer os.RemoveAll(cfg.DataDir)
	defer db.Close()

	chaincfg.ActiveNetParams.Params = &chaincfg.DevelopNetParams
	rm := NewRoundManager()

	bpc := minersync.NewBtcPowerCollector(
		NewFakeBtcClient(chaincfg.ActiveNetParams.CollectHeight,
			int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)*(16)+chaincfg.ActiveNetParams.CollectInterval), db)
	_, err = bpc.Init(0, int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)*10+chaincfg.ActiveNetParams.CollectInterval)
	if err != nil {
		t.Errorf("BtcPowerCollector init error %v", err)
		return
	}
	rm.minerCollector = bpc
	rm.roundValidatorsCache = make([]*RoundValidators, CacheSize)
	rm.rmCache = make([]*RoundMinerInfo, CacheSize)
	tests := []struct {
		round   uint32
		wantErr bool
	}{
		{
			0,
			false,
		}, {
			1,
			false,
		}, {
			10,
			false,
		}, {
			11,
			true,
		}, {
			0xFFFFFFFF,
			true,
		},
	}

	for i, test := range tests {
		_, err := rm.getRoundMiner(test.round)
		if test.wantErr != (err != nil) {
			t.Errorf("TestGetMinersByRound test #%v error", i)
		}
	}
}

func TestValidators(t *testing.T) {
	cfg := &chaincfg.FConfig{
		DataDir: chaincfg.DefaultDataDir,
	}
	cfg.DataDir = cleanAndExpandPath(cfg.DataDir)
	cfg.DataDir = filepath.Join(cfg.DataDir, "devNetUnitTest")
	// Load the block database.
	db, err := loadBlockDB(cfg)
	if err != nil {
		t.Errorf("loadBlockDB err %v", err)
		return
	}

	defer os.RemoveAll(cfg.DataDir)
	defer db.Close()

	chaincfg.ActiveNetParams.Params = &chaincfg.MainNetParams
	rm := NewRoundManager()

	bpc := minersync.NewBtcPowerCollector(
		NewFakeBtcClient(chaincfg.ActiveNetParams.CollectHeight,
			int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)*(16)+chaincfg.ActiveNetParams.CollectInterval), db)
	_, err = bpc.Init(0, int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)*10+chaincfg.ActiveNetParams.CollectInterval)
	if err != nil {
		t.Errorf("BtcPowerCollector init error %v", err)
		return
	}
	rm.db = db
	rm.minerCollector = bpc
	rm.roundValidatorsCache = make([]*RoundValidators, CacheSize)
	rm.rmCache = make([]*RoundMinerInfo, CacheSize)
	rm.validatorMap = make(map[common.Address]HistoryFlag)

	validators1 := []*common.Address{}
	weights1 := make(map[common.Address]uint16)

	add1 := common.HexToAddress("0x11")
	add2 := common.HexToAddress("0x22")

	validators2 := []*common.Address{
		&add1,
	}
	weights2 := make(map[common.Address]uint16)
	weights2[add1] = 1

	validators3 := []*common.Address{
		&add1,
		&add2,
	}
	weights3 := make(map[common.Address]uint16)
	weights3[add1] = 1
	weights3[add2] = 1

	validators4 := []*common.Address{
		&add1,
		&add1,
		&add1,
	}
	weights4 := make(map[common.Address]uint16)
	weights4[add1] = 3

	validators5 := []*common.Address{
		&add1, &add2,
		&add1, &add2,
		&add1, &add2,
	}
	weights5 := make(map[common.Address]uint16)
	weights5[add1] = 3
	weights5[add2] = 3

	tests := []struct {
		hash                common.Hash
		validators          []*common.Address
		wantWeights         map[common.Address]uint16
		wantValidatorsCount uint32
	}{
		{
			common.HexToHash("0x11"),
			validators1,
			weights1,
			0,
		},
		{
			common.HexToHash("0x22"),
			validators2,
			weights2,
			1,
		},
		{
			common.HexToHash("0x33"),
			validators3,
			weights3,
			2,
		},
		{
			common.HexToHash("0x44"),
			validators4,
			weights4,
			2,
		},
		{
			common.HexToHash("0x55"),
			validators5,
			weights5,
			2,
		},
	}

	for i, test := range tests {
		w := rm.setValidators(uint32(i) + 1,test.hash, test.validators)
		for k, v := range w {
			if v != test.wantWeights[k] {
				t.Errorf("TestValidators SetRoundMiner test #%v error", i)
			}
		}
		validator, w, _ := rm.GetValidators(test.hash, uint32(i) + 1, nil)
		for k, v := range w {
			if v != test.wantWeights[k] {
				t.Errorf("TestValidators GetRoundMiner test #%v error", i)
			}
		}
		if len(validator) > 0 && unsafe.Pointer(&validator[0]) != unsafe.Pointer(&validator[0]) {
			t.Errorf("TestValidators GetRoundMiner test #%v cache error", i)
		}
	}
}
