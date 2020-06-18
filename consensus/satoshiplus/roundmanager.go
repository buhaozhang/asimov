// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package satoshiplus

import (
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil/vrf"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/consensus/satoshiplus/minersync"
	"github.com/AsimovNetwork/asimov/database"
	"math"
	"sync"
)

type HistoryFlag byte
const (
	// CacheSize represents the size of cache for round validators.
    CacheSize = 8

	ExistFlag HistoryFlag = 1
	SaveFlag  HistoryFlag = 2
	NoneFlag  HistoryFlag = 0
)
var (
	validatorBucketName = []byte("validatorIdx")
)

type RoundValidators struct {
	round      uint32
	blockHash  common.Hash
	validators []*common.Address
	weightmap  map[common.Address]uint16
}

type RoundMinerInfo struct {
	round      uint32
	lastTime   uint32
	miners     map[string]float64
	mapping    map[string]*ainterface.ValidatorInfo
}

type RoundManager struct {
	minerCollector *minersync.BtcPowerCollector
	db             database.Transactor

	// Expected count of seconds per round.
	roundInterval int64

	// round cache
	vLock                sync.RWMutex
    roundValidatorsCache []*RoundValidators

	// validatorMap and is related to handling of quick validating blocks. They are
	// protected by a lock.
	validatorLock sync.RWMutex
	validatorMap  map[common.Address]HistoryFlag

    rmLock  sync.RWMutex
	rmCache []*RoundMinerInfo
}

// return true if the given validator may be valid, other wise false.
func (m* RoundManager) HasValidator(validator common.Address) bool {
	m.validatorLock.RLock()
	v, ok := m.validatorMap[validator]
	m.validatorLock.RUnlock()
	if ok {
		return (v & ExistFlag) == ExistFlag
	}

	exist := false
	_ = m.db.View(func(dbTx database.Tx) error {
		bucket := dbTx.Metadata().Bucket(validatorBucketName)
		serializedBytes := bucket.Get(validator[:])
		if len(serializedBytes) > 0 {
			exist = true
			m.validatorLock.Lock()
			m.validatorMap[validator] = ExistFlag
			m.validatorLock.Unlock()
		}
		return nil
	})
	return exist
}

// Get validators for special round.
func (m *RoundManager) GetValidators(blockHash common.Hash, round uint32, fn ainterface.GetValidatorsCallBack) (
	[]*common.Address, map[common.Address]uint16, error) {

	m.vLock.Lock()
	defer m.vLock.Unlock()

	for i := 0; i < CacheSize; i++ {
		cache := m.roundValidatorsCache[i]
		if cache == nil {
			break
		}
		if cache.round == round && cache.blockHash == blockHash {
			return cache.validators, cache.weightmap, nil
		}
	}

	miners, err := m.GetMinersByRound(round)
	if err != nil {
		return nil, nil, err
	}

	mineraddrs := make([]string, 0, len(miners))
	for miner, _ := range miners {
		mineraddrs = append(mineraddrs, miner)
	}
	candidates := make(map[common.Address]float64)
	totalWeight := float64(0)
	if fn != nil {
		signupValidators, filters, err := fn(mineraddrs)
		if err != nil {
			return nil, nil, err
		}
		for i, sv := range signupValidators {
			if filters[i] < 0 {
				continue
			}
			if filters[i] > 0 {
				candidates[sv] += miners[mineraddrs[i]]
				totalWeight += candidates[sv]
			}
		}
	}

	if len(candidates) <= 2 {
		candidatesLength := len(chaincfg.ActiveNetParams.GenesisCandidates)
		if math.Abs(totalWeight) < 1e-8 {
			totalWeight = float64(candidatesLength)
		}
		totalWeight *= 2
		for _, candidate := range chaincfg.ActiveNetParams.GenesisCandidates {
			candidates[candidate] = totalWeight/float64(candidatesLength)
		}
	}

	validators := vrf.SelectValidators(candidates, *chaincfg.ActiveNetParams.GenesisHash, round, chaincfg.ActiveNetParams.RoundSize)

	weightmap := m.setValidators(round, blockHash, validators)
	return validators, weightmap, nil
}

// set validators into the cache
func (m *RoundManager) setValidators(round uint32,blockHash common.Hash, validators []*common.Address) map[common.Address]uint16 {
	weightmap := make(map[common.Address]uint16)
	for _, validator := range validators {
		weightmap[*validator]++
	}
	newRb := &RoundValidators{
		round:      round,
		blockHash:  blockHash,
		validators: validators,
		weightmap:  weightmap,
	}
	for i := 0; i < CacheSize; i++ {
		m.roundValidatorsCache[i], newRb = newRb, m.roundValidatorsCache[i]
	}

	return weightmap
}

// get round miner from cache
func (m *RoundManager) getRoundMinerFromCache(round uint32) *RoundMinerInfo {
	for i := 0; i < CacheSize; i++ {
		cache := m.rmCache[i]
		if cache == nil {
			break
		}
		if cache.round == round {
			return cache
		}
	}
	return nil
}

// set round miner info into the cache.
func (m *RoundManager) setRoundMiner(roundMiner *RoundMinerInfo) {
	newRoundMiner := roundMiner
	for i := 0; i < CacheSize; i++ {
		m.rmCache[i], newRoundMiner = newRoundMiner, m.rmCache[i]
	}
}

// get miner info of round
func (m *RoundManager) getRoundMiner(round uint32) (*RoundMinerInfo, error) {
	m.rmLock.Lock()
	defer m.rmLock.Unlock()
	rm := m.getRoundMinerFromCache(round)
	if rm != nil {
		return rm, nil
	}

	height := int32(round) * int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)

	size := chaincfg.ActiveNetParams.CollectInterval
	minerInfos, err := m.minerCollector.GetBitcoinMiner(height, size)
	if err != nil {
		return nil, err
	}
	if int32(len(minerInfos)) != size {
		return nil, errors.New(fmt.Sprintf("Not enough miner found, round = %d", round))
	}

	miners := make(map[string]float64)
	registerMapping := make(map[string]*ainterface.ValidatorInfo)

	times := make([]int, size)
	for i, miner := range minerInfos {
		miners[miner.Address()]++
		times[i] = int(miner.Time())
		if round == 0 || i + int(chaincfg.ActiveNetParams.BtcBlocksPerRound) >= int(size) {
			validatorTxs := miner.ValidatorTxs()
			if len(validatorTxs) > 0 {
				for _, tx := range validatorTxs {
					for _, txin := range tx.Vin {
						minername := m.minerCollector.GetMinerName(txin)
						if len(minername) > 0 {
							registerMapping[txin] = &ainterface.ValidatorInfo{tx.OutAddress, minername,}
						}
					}
				}
			}
		}
	}
	lastTime := times[len(times) - 1]
	newRoundMiner := &RoundMinerInfo {
		round:   round,
		lastTime: uint32(lastTime),
		miners:  miners,
		mapping: registerMapping,
	}
	m.setRoundMiner(newRoundMiner)
	return newRoundMiner, nil
}

// get miners of round
// the return value is a map (key (miner) -> value (count per collect interval))
func (m *RoundManager) GetMinersByRound(round uint32) (map[string]float64, error) {
	rm, err := m.getRoundMiner(round)
	if err != nil {
		return nil, err
	}
	return rm.miners, nil
}

func (m *RoundManager) GetHsMappingByRound(round uint32) (map[string]*ainterface.ValidatorInfo, error) {
	rm, err := m.getRoundMiner(round)
	if err != nil {
		return nil, err
	}
	return rm.mapping, nil
}

// get special round interval
func (m *RoundManager) GetRoundInterval(round int64) int64 {
	if round <= 1 {
		return m.roundInterval
	}

	roundMiner1, err := m.getRoundMiner(uint32(round) - 1)
	if err != nil {
		log.Error("Get round middle time failed", err)
		return 0
	}
	roundMiner2, err := m.getRoundMiner(uint32(round))
	if err != nil {
		return 0
	}
	interval := int64(roundMiner2.lastTime-roundMiner1.lastTime)
	if interval < common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize) {
		interval = common.MinBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize)
	}
	if interval > common.MaxBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize) {
		interval = common.MaxBlockInterval * int64(chaincfg.ActiveNetParams.RoundSize)
	}
	return interval
}

// return the next round.
func (m *RoundManager) GetNextRound(round *ainterface.Round) (*ainterface.Round, error) {
	interval := m.GetRoundInterval(int64(round.Round) + 1)
	if interval == 0 {
		errStr := fmt.Sprintf("get next round error, round=%d", round.Round + 1)
		return nil, errors.New(errStr)
	}

	newRound := &ainterface.Round{
		Round:          round.Round + 1,
		RoundStartUnix: round.RoundStartUnix + round.Duration,
		Duration:       int64(interval),
	}
	return newRound, nil
}

// initialize round manager.
func (m *RoundManager) Init(round uint32, db database.Transactor, c ainterface.IBtcClient) error {
	m.db = db
	m.validatorMap = make(map[common.Address]HistoryFlag)

	bpc := minersync.NewBtcPowerCollector(c, db)

	lastround := int32(round) - 1
	if lastround < 0 {
		lastround ++
	}
	height := lastround * int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)
	count := chaincfg.ActiveNetParams.CollectInterval + int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)
	_, err := bpc.Init(height, count)
	if err != nil {
		return err
	}

	m.minerCollector = bpc
	m.roundValidatorsCache = make([]*RoundValidators, CacheSize)
	m.rmCache = make([]*RoundMinerInfo, CacheSize)

	return nil
}

func (m *RoundManager) Start() {
	err := m.minerCollector.Start()
	if err != nil {
		log.Critical("Start miner collector failed", err.Error())
		panic(err)
	}
}

func (m *RoundManager) Halt() {
	_ = m.minerCollector.Halt()
}

// return unique contract code which represents some consensus
func (m *RoundManager) GetContract() common.Address {
	return common.ConsensusSatoshiPlus
}

// create a new round maganger
func NewRoundManager() *RoundManager {
	return &RoundManager{
		roundInterval: int64(chaincfg.ActiveNetParams.RoundSize) * common.DefaultBlockInterval,
	}
}
