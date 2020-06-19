// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/asiutil/vrf"
	"github.com/AsimovNetwork/asimov/blockchain/syscontract"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/consensus/satoshiplus/minersync"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/database/dbdriver"
	"github.com/AsimovNetwork/asimov/database/dbimpl/ethdb"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/rpcs/rpc"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// testDbType is the database backend type to use for the tests.
	testDbType = "ffldb"
	// blockDbNamePrefix is the prefix for the block database name.  The
	// database type is appended to this value to form the full block
	// database name.
	blockDbNamePrefix = "blocks"
	CoinbaseFlags = "/P2SH/asimovd/"
)

var (
	defaultHomeDir     = asiutil.AppDataDir("asimovd", false)
)

////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Manager defines an contract manager that manages multiple system contracts and
// implements the blockchain.ContractManager interface so it can be seamlessly
// plugged into normal chain processing.
type ManagerTmp struct {
	chain fvm.ChainContext
	//  genesis transaction data cache
	genesisDataCache map[common.Address][]chaincfg.ContractInfo
	// unrestricted assets cache
	assetsUnrestrictedCache map[protos.Asset]struct{}
}

// Init manager by genesis data.
func (m *ManagerTmp) Init(chain fvm.ChainContext, dataBytes [] byte) error {
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
	return nil
}

// Get latest contract by height.
func (m *ManagerTmp) GetActiveContractByHeight(height int32, delegateAddr common.Address) *chaincfg.ContractInfo {
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

func (m *ManagerTmp) DisconnectBlock(block *asiutil.Block) {
	m.assetsUnrestrictedCache = make(map[protos.Asset]struct{})
}

func NewContractManagerTmp() ainterface.ContractManager {
	return &ManagerTmp {}
}

// Ensure the Manager type implements the blockchain.ContractManager interface.
var _ ainterface.ContractManager = (*ManagerTmp)(nil)

func (m *ManagerTmp) GetSignedUpValidators(
	consensus common.Address,
	block *asiutil.Block,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	miners []string) ([]common.Address, []uint32, error){

	gas := uint64(common.SystemContractReadOnlyGas)
	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), consensus)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %s, %d", consensus, block.Height())
		log.Error(errStr)
		panic(errStr)
	}
	proxyAddr, abi := consensus, contract.AbiInfo

	funcName := common.ContractConsensusSatoshiPlus_GetSignupValidatorsFunction()
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


func (m *ManagerTmp) GetContractAddressByAsset(
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
	proxyAddr, abi := common.RegistryCenter, contract.AbiInfo

	//获取发币合约地址
	funcName := common.ContractRegistryCenter_GetOrganizationAddressesByAssetsFunction()
	assetId := make([]uint64, 0, 1)
	for _, asset := range assets {
		assetBytes := common.Hex2Bytes(asset)
		assetId = append(assetId, binary.BigEndian.Uint64(assetBytes[4:]))

	}
	runCode, err := fvm.PackFunctionArgs(abi, funcName, assetId)

	result, leftOvergas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		gas, proxyAddr, runCode)
	if err != nil {
		log.Errorf("Get  contract address of asset failed, error: %s", err)
		return nil, false, leftOvergas
	}

	var outType []common.Address
	err = fvm.UnPackFunctionResult(abi, &outType, funcName, result)
	if err != nil {
		log.Errorf("Get contract address of asset failed, error: %s", err)
		return nil, false, leftOvergas
	}

	return outType, true, leftOvergas
}

func (m *ManagerTmp) GetAssetInfoByAssetId(
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
	proxyAddr, abi := common.RegistryCenter, contract.AbiInfo

	//获取发币合约地址
	funcName := common.ContractRegistryCenter_GetAssetInfoByAssetIdFunction()
	//
	results := make([]ainterface.AssetInfo, 0)
	var err error
	for _, asset := range assets {
		assetBytes := common.Hex2Bytes(asset)
		orgIdBytes := assetBytes[4:8]
		assetIndexBytes := assetBytes[8:]

		var orgId = binary.BigEndian.Uint32(orgIdBytes)
		var assetIndex = binary.BigEndian.Uint32(assetIndexBytes)
		runCode, err := fvm.PackFunctionArgs(abi, funcName, orgId, assetIndex)
		result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
			common.SystemContractReadOnlyGas, proxyAddr, runCode)
		if err != nil {
			log.Errorf("Get  asset inf failed, error: %s", err)
		}

		var outType = &[]interface{}{new(bool), new(string), new(string), new(string), new(*big.Int), new([]*big.Int)}
		err = fvm.UnPackFunctionResult(abi, outType, funcName, result)
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

func (m *ManagerTmp) GetTemplates(
	block *asiutil.Block,
	gas uint64,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	getCountFunc string,
	getTemplatesFunc string,
	category uint16,
	pageNo int,
	pageSize int) (int, []ainterface.TemplateWarehouseContent, error, uint64) {

	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), common.TemplateWarehouse)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %s, %d", common.TemplateWarehouse, block.Height())
		log.Error(errStr)
		panic(errStr)
	}
	proxyAddr, abi := common.TemplateWarehouse, contract.AbiInfo

	// 1、获取模板数量
	getTemplatesCount := getCountFunc
	runCode, err := fvm.PackFunctionArgs(abi, getTemplatesCount, category)
	result, leftOvergas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig, gas,
		proxyAddr, runCode)
	if err != nil {
		log.Errorf("Get contract templates failed, error: %s", err)
		return 0, nil, err, leftOvergas
	}

	var outInt *big.Int
	err = fvm.UnPackFunctionResult(abi, &outInt, getTemplatesCount, result)
	if err != nil {
		log.Errorf("Get contract templates failed, error: %s", err)
		return 0, nil, err, leftOvergas
	}
	if outInt.Int64() == 0 {
		return 0, nil, nil, leftOvergas
	}

	// 2、获取模板信息
	template := make([]ainterface.TemplateWarehouseContent, 0)
	getTemplate := getTemplatesFunc

	// settings of Pagination
	fromIndex := int(outInt.Int64())-pageNo*pageSize-1
	if fromIndex < 0 {
		return 0, nil, nil, leftOvergas
	}
	endIndex := fromIndex+1-pageSize
	if endIndex < 0 {
		endIndex = 0
	}

	for i := fromIndex; i >= endIndex; i-- {
		runCode, err := fvm.PackFunctionArgs(abi, getTemplate, category, big.NewInt(int64(i)))
		if err != nil {
			log.Errorf("Get contract templates failed, error: %s", err)
			return 0, nil, err, leftOvergas
		}
		ret, leftOvergas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
			leftOvergas, proxyAddr, runCode)
		if err != nil {
			log.Errorf("Get contract templates failed, error: %s", err)
			return 0, nil, err, leftOvergas
		}

		cTime := new(big.Int)
		var keyType [32]byte
		outType := &[]interface{}{new(string), &keyType, &cTime, new(uint8), new(uint8), new(uint8), new(uint8)}
		err = fvm.UnPackFunctionResult(abi, outType, getTemplate, ret)
		if err != nil {
			log.Errorf("Get contract template failed, index is %d, error: %s", i, err)
			continue
		}

		name := *((*outType)[0]).(*string)
		key := common.Bytes2Hex(keyType[:])
		createTime := cTime.Int64()
		approveCount := *((*outType)[3]).(*uint8)
		rejectCount := *((*outType)[4]).(*uint8)
		reviewers := *((*outType)[5]).(*uint8)
		status := *((*outType)[6]).(*uint8)
		if status != syscontract.TEMPLATE_STATUS_NOTEXIST {
			template = append(template, ainterface.TemplateWarehouseContent{name, key, createTime,
			approveCount, rejectCount, reviewers, status})
		}
	}

	return int(outInt.Int64()), template, nil, leftOvergas
}

func (m *ManagerTmp) GetTemplate(
	block *asiutil.Block,
	gas uint64,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	category uint16,
	name string) (ainterface.TemplateWarehouseContent, bool, uint64) {
	officialAddr := chaincfg.OfficialAddress
	contract := m.GetActiveContractByHeight(block.Height(), common.TemplateWarehouse)
	if contract == nil {
		errStr := fmt.Sprintf("Failed to get active contract %s, %d", common.TemplateWarehouse, block.Height())
		log.Error(errStr)
		panic(errStr)
	}
	proxyAddr, abi := common.TemplateWarehouse, contract.AbiInfo
	getTemplate := common.ContractTemplateWarehouse_GetTemplateFunction()
	runCode, err := fvm.PackFunctionArgs(abi, getTemplate, category, name)
	if err != nil {
		log.Errorf("Get contract template failed, error: %s", err)
		return ainterface.TemplateWarehouseContent{}, false, gas
	}
	ret, leftOvergas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		gas, proxyAddr, runCode)
	if err != nil {
		log.Errorf("Get contract template failed, error: %s", err)
		return ainterface.TemplateWarehouseContent{}, false, leftOvergas
	}

	cTime := new(big.Int)
	var keyType [32]byte
	outType := &[]interface{}{new(string), &keyType, &cTime, new(uint8), new(uint8), new(uint8), new(uint8)}

	err = fvm.UnPackFunctionResult(abi, outType, getTemplate, ret)
	if err != nil {
		log.Errorf("Get contract template failed, error: %s", err)
		return ainterface.TemplateWarehouseContent{}, false, leftOvergas
	}

	key := common.Bytes2Hex(keyType[:])
	createTime := cTime.Int64()
	approveCount := *((*outType)[3]).(*uint8)
	rejectCount := *((*outType)[4]).(*uint8)
	reviewers := *((*outType)[5]).(*uint8)
	status := *((*outType)[6]).(*uint8)
	if status == syscontract.TEMPLATE_STATUS_NOTEXIST {
		return ainterface.TemplateWarehouseContent{}, false, leftOvergas
	}
	return ainterface.TemplateWarehouseContent{name, key,
		createTime, approveCount,
		rejectCount, reviewers,
		status},
		true, leftOvergas
}


func (m *ManagerTmp) GetFees(
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
	proxyAddr, abi := common.ValidatorCommittee, contract.AbiInfo

	// feelist func
	feelistFunc := common.ContractValidatorCommittee_GetAssetFeeListFunction()
	runCode, err := fvm.PackFunctionArgs(abi, feelistFunc)
	result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		common.SystemContractReadOnlyGas,
		proxyAddr, runCode)
	if err != nil {
		log.Errorf("Get contract templates failed, error: %s", err)
		return nil, err
	}
	assets := make([]*big.Int,0)
	height := make([]*big.Int,0)
	outData := []interface {}{
		&assets,
		&height,
	}
	err = fvm.UnPackFunctionResult(abi, &outData, feelistFunc, result)
	if err != nil {
		log.Errorf("Get fee list failed, error: %s", err)
		return nil, err
	}
	if len(assets) != len(height) {
		errStr := "Get fee list failed, length of asset does not match length of height"
		log.Errorf(errStr)
		return nil, errors.New(errStr)
	}

	fees := make(map[protos.Asset]int32)
	for i := 0; i < len(assets); i++ {
		pAssets := protos.AssetFromBytes(assets[i].Bytes())
		fees[*pAssets] = int32(height[i].Int64())
	}

	return fees, nil
}

// IsLimit returns a number of int type by find in memory or calling system
// contract of registry the number represents if an asset is restricted
func (m *ManagerTmp) IsLimit(block *asiutil.Block,
	stateDB vm.StateDB, asset *protos.Asset) int {
	if _, ok := m.assetsUnrestrictedCache[*asset]; ok {
		return 0
	}
	limit := m.isLimit(block, stateDB, asset)

	if limit == 0 {
		m.assetsUnrestrictedCache[*asset] = struct{}{}
	}

	return limit
}

// isLimit returns a number of int type by calling system contract of registry
// the number represents if an asset is restricted
func (m *ManagerTmp) isLimit(block *asiutil.Block,
	stateDB vm.StateDB, asset *protos.Asset) int {

	officialAddr := chaincfg.OfficialAddress
	_, organizationId, assetIndex := asset.AssetFields()
	registryCenterAddress := common.RegistryCenter

	input := common.PackIsRestrictedAssetInput(organizationId, assetIndex)
	result, _, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain,
		stateDB, chaincfg.ActiveNetParams.FvmParam,
		common.ReadOnlyGas, registryCenterAddress, input)
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


func (m *ManagerTmp) IsSupport(block *asiutil.Block,
	stateDB vm.StateDB, gasLimit uint64, asset *protos.Asset, address []byte) (bool, uint64) {

	if gasLimit < common.SupportCheckGas {
		return false, 0
	}

	// step1: prepare parameters for calling system contract to get organization address
	_, organizationId, assetIndex := asset.AssetFields()
	caller := chaincfg.OfficialAddress
	registryCenterAddress := common.RegistryCenter



	input := common.PackGetOrganizationAddressByIdInput(organizationId, assetIndex)
	result, leftOverGas, err := fvm.CallReadOnlyFunction(caller, block, m.chain,
		stateDB, chaincfg.ActiveNetParams.FvmParam,
		common.SupportCheckGas, registryCenterAddress, input)
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
/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////


/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
type HistoryFlag byte
const (
	// CacheSize represents the size of cache for round validators.
	CacheSize = 8
	ExistFlag HistoryFlag = 1
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
				candidates[sv] = miners[mineraddrs[i]]
				totalWeight += candidates[sv]
			}
		}
	}

	if len(candidates) <= 2 {
		candidatesLength := len(chaincfg.ActiveNetParams.GenesisCandidates) + len(candidates)
		if math.Abs(totalWeight) < 1e-8 {
			totalWeight = float64(candidatesLength)
		}
		for _, candidate := range chaincfg.ActiveNetParams.GenesisCandidates {
			w, ok := candidates[candidate]
			if ok {
				candidates[candidate] = w + totalWeight/float64(candidatesLength)
			} else {
				candidates[candidate] = totalWeight/float64(candidatesLength)
			}
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
/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
type FakeBtcClient struct {
	minerInfo []minersync.GetBitcoinBlockMinerInfoResult
	chainInfo *minersync.GetBlockChainInfoResult
}

func NewFakeBtcClient(height, count int32) *FakeBtcClient {
	minerInfo := make([]minersync.GetBitcoinBlockMinerInfoResult, 0, count)
	c := &FakeBtcClient{
		minerInfo: minerInfo,
	}
	c.push(height,count)
	return c
}

func (c *FakeBtcClient) Push(count int32) {
	height := c.minerInfo[len(c.minerInfo)-1].Height + 1
	c.push(height, count)
}

func (c *FakeBtcClient) push(height, count int32) {

	for i := int32(0); i < count; i++ {
		c.minerInfo = append(c.minerInfo, minersync.GetBitcoinBlockMinerInfoResult{
			Height:  height + i,
			Pool:    "",
			Hash:    strconv.Itoa(int(height + i)),
			Address: "11",
			Time:    600 * uint32(i),
		})
	}

	c.chainInfo = &minersync.GetBlockChainInfoResult{
		Blocks: height + count - 1,
	}
}

func (c *FakeBtcClient) GetBitcoinMinerInfo(result interface{}, height, count int32) error {
	begin := c.minerInfo[0].Height
	last := c.minerInfo[len(c.minerInfo)-1].Height
	if height < begin || height+count-1 > last {
		return errors.New("miners not find")
	}
	miners := result.(*[]minersync.GetBitcoinBlockMinerInfoResult)
	*miners = c.minerInfo[height-begin : height-begin+count]
	return nil
}

func (c *FakeBtcClient) GetBitcoinBlockChainInfo(result interface{}) error {
	chaininfo := result.(*minersync.GetBlockChainInfoResult)
	*chaininfo = *c.chainInfo
	return nil
}
/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (b *BlockChain) addBlockNode(block *asiutil.Block) error {
	prevHash := &block.MsgBlock().Header.PrevBlock
	prevNode := b.index.LookupNode(prevHash)
	if prevNode == nil {
		str := fmt.Sprintf("previous block %s is unknown", prevHash)
		return ruleError(ErrPreviousBlockUnknown, str)
	} else if b.index.NodeStatus(prevNode).KnownInvalid() {
		str := fmt.Sprintf("previous block %s is known to be invalid", prevHash)
		return ruleError(ErrInvalidAncestorBlock, str)
	}
	var err error
	blockHeader := &block.MsgBlock().Header
	round := prevNode.round
	if round.Round != blockHeader.Round {
		round, err = b.roundManager.GetNextRound(round)
		if err != nil {
			return err
		}
	}
	newNode := newBlockNode(round, blockHeader, prevNode)
	newNode.status = statusDataStored
	b.index.AddNode(newNode)
	b.bestChain.SetTip(newNode)

	//update stateSnapshot:
	b.stateLock.RLock()
	curTotalTxns := b.stateSnapshot.TotalTxns
	b.stateLock.RUnlock()
	numTxns := uint64(len(block.MsgBlock().Transactions))
	blockSize := uint64(block.MsgBlock().SerializeSize())
	stateSnapshot := newBestState(newNode, blockSize, numTxns,
		curTotalTxns+numTxns, block.MsgBlock().Header.Timestamp)

	b.stateLock.Lock()
	b.stateSnapshot = stateSnapshot
	b.stateLock.Unlock()

	return nil
}

func cleanAndExpandPath(path string) string {
	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		homeDir := filepath.Dir(defaultHomeDir)
		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but they variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

func newFakeChain(paramstmp *chaincfg.Params) (*BlockChain, func(), error) {
	// Create a genesis block node and block index index populated with it
	// for use when creating the fake chain below.
	if paramstmp.ChainStartTime == 0 {
		paramstmp.ChainStartTime = time.Now().Unix()
	}

	cfg := &chaincfg.FConfig{
		ConfigFile:           chaincfg.DefaultConfigFile,
		DebugLevel:           chaincfg.DefaultLogLevel,
		MaxPeers:             chaincfg.DefaultMaxPeers,
		BanDuration:          chaincfg.DefaultBanDuration,
		BanThreshold:         chaincfg.DefaultBanThreshold,
		RPCMaxClients:        chaincfg.DefaultMaxRPCClients,
		RPCMaxWebsockets:     chaincfg.DefaultMaxRPCWebsockets,
		RPCMaxConcurrentReqs: chaincfg.DefaultMaxRPCConcurrentReqs,
		DataDir:              chaincfg.DefaultDataDir,
		LogDir:               chaincfg.DefaultLogDir,
		StateDir:             chaincfg.DefaultStateDir,
		RPCKey:               chaincfg.DefaultRPCKeyFile,
		RPCCert:              chaincfg.DefaultRPCCertFile,
		MinTxPrice:           chaincfg.DefaultMinTxPrice,
		MaxOrphanTxs:         chaincfg.DefaultMaxOrphanTransactions,
		MaxOrphanTxSize:      chaincfg.DefaultMaxOrphanTxSize,
		HTTPEndpoint:         chaincfg.DefaultHTTPEndPoint,
		HTTPModules:          chaincfg.DefaultHttpModules,
		HTTPVirtualHosts:     chaincfg.DefaultHTTPVirtualHosts,
		HTTPTimeouts:         rpc.DefaultHTTPTimeouts,
		WSEndpoint:           chaincfg.DefaultWSEndPoint,
		WSModules:            chaincfg.DefaultWSModules,
		DevelopNet:           true,
		Consensustype:        "satoshiplus",
		MaxTimeOffset:        30,
	}

	consensus := common.GetConsensus(cfg.Consensustype)
	if consensus < 0 {
		log.Errorf("The consensus type is %v, which is not support", consensus)
		return nil, nil, nil
	}
	chaincfg.ActiveNetParams.Params = paramstmp

	var teardown func()
	cfg.DataDir = cleanAndExpandPath(cfg.DataDir)
	cfg.DataDir = filepath.Join(cfg.DataDir, "UnitTest")

	// Append the network type to the logger directory so it is "namespaced"
	// per network in the same fashion as the data directory.
	cfg.LogDir = cleanAndExpandPath(cfg.LogDir)
	cfg.LogDir = filepath.Join(cfg.LogDir, "UnitTest")

	cfg.StateDir = cleanAndExpandPath(cfg.StateDir)
	cfg.StateDir = filepath.Join(cfg.StateDir, "UnitTest")

	// Load the block database.
	db, err := loadBlockDB(cfg)
	if err != nil {
		return nil,nil,err
	}

	// Load StateDB
	stateDB, stateDbErr := ethdb.NewLDBDatabase(cfg.StateDir, 768, 1024)
	if stateDbErr != nil {
		teardown()
		log.Errorf("%v", stateDbErr)
		return nil,nil,stateDbErr
	}

	// Create a btc rpc client
	btcClient := NewFakeBtcClient(chaincfg.ActiveNetParams.CollectHeight,
		chaincfg.ActiveNetParams.CollectInterval + int32(chaincfg.ActiveNetParams.BtcBlocksPerRound) * 24)

	// Create a round manager.
	roundManger := NewRoundManager()

	// Create a contract manager.
	contractManager := NewContractManagerTmp()

	dir, err := os.Getwd()
	genesisBlock, err := asiutil.LoadBlockFromFile("../genesisbin/mainnet.block")
	if err != nil {
		strErr := "Load genesis block error, " + err.Error() + dir
		return nil, nil, errors.New(strErr)
	}
	genesisHash := asiutil.NewBlock(genesisBlock).Hash()
	if *chaincfg.ActiveNetParams.GenesisHash != *genesisHash {
		strErr := fmt.Sprintf("Load genesis block genesis hash mismatch expected %s, but %s",
			chaincfg.ActiveNetParams.GenesisHash.String(), genesisHash.String())
		return nil, nil, errors.New(strErr)
	}
	chaincfg.ActiveNetParams.GenesisBlock = genesisBlock

	chain, err := New(&Config{
		DB:            db,
		ChainParams:   paramstmp,
		TimeSource:    NewMedianTime(),
		StateDB:       stateDB,
		BtcClient:     btcClient,
		RoundManager:  roundManger,
		ContractManager: contractManager,
	}, nil)
	if err != nil {
		teardown = func() {
			db.Close()
			stateDB.Close()
			os.RemoveAll(cfg.DataDir)
			os.RemoveAll(cfg.LogDir)
			os.RemoveAll(cfg.StateDir)
		}
		return nil,nil,err
	}
	if err := contractManager.Init(chain, chaincfg.ActiveNetParams.GenesisBlock.Transactions[0].TxOut[0].Data); err != nil {
		return nil, teardown, err
	}

	teardown = func() {
		db.Close()
		stateDB.Close()
		os.RemoveAll(cfg.DataDir)
		os.RemoveAll(cfg.LogDir)
		os.RemoveAll(cfg.StateDir)
	}

	chaincfg.Cfg = cfg
	return chain,teardown,nil
}


func blockDbPath(dataDir, dbType string) string {
	// The database name is based on the database type.
	dbName := blockDbNamePrefix + "_" + dbType
	if dbType == "sqlite" {
		dbName = dbName + ".db"
	}
	dbPath := filepath.Join(dataDir, dbName)
	return dbPath
}

func loadBlockDB(cfg *chaincfg.FConfig) (database.Database, error) {
	// The database name is based on the database type.
	dbPath := blockDbPath(cfg.DataDir, testDbType)
	db, err := dbdriver.Open(testDbType, dbPath, chaincfg.ActiveNetParams.Params.Net)
	if err != nil {
		// Return the error if it's not because the database doesn't
		// exist.
		if dbErr, ok := err.(database.Error); !ok || dbErr.ErrorCode !=
			database.ErrDbDoesNotExist {

			return nil, err
		}
		// Create the db if it does not exist.
		err = os.MkdirAll(cfg.DataDir, 0700)
		if err != nil {
			return nil, err
		}
		db, err = dbdriver.Create(testDbType, dbPath, chaincfg.ActiveNetParams.Params.Net)
		if err != nil {
			return nil, err
		}
	}

	return db, nil
}

func StandardCoinbaseScript(nextBlockHeight int32, extraNonce uint64) ([]byte, error) {
	return txscript.NewScriptBuilder().AddInt64(int64(nextBlockHeight)).
		AddInt64(int64(extraNonce)).AddData([]byte(CoinbaseFlags)).
		Script()
}

func createTestCoinbaseTx(params *chaincfg.Params, coinbaseScript []byte, nextBlockHeight int32,
	addr common.IAddress, asset protos.Asset, amount int64, contractOut *protos.TxOut) (*asiutil.Tx, *protos.TxOut, error) {

	var pkScript []byte
	if addr != nil {
		var err error
		pkScript, err = txscript.PayToAddrScript(addr)
		if err != nil {
			return nil, nil, err
		}
	} else {
		var err error
		scriptBuilder := txscript.NewScriptBuilder()
		pkScript, err = scriptBuilder.AddOp(txscript.OP_TRUE).Script()
		if err != nil {
			return nil, nil, err
		}
	}

	tx := protos.NewMsgTx(protos.TxVersion)
	tx.AddTxIn(&protos.TxIn{
		// Coinbase transactions have no inputs, so previous outpoint is
		// zero hash and max index.
		PreviousOutPoint: *protos.NewOutPoint(&common.Hash{}, protos.MaxPrevOutIndex),
		SignatureScript:  coinbaseScript,
		Sequence:         protos.MaxTxInSequenceNum,
	})

	if contractOut != nil {
		tx.AddTxOut(contractOut)
	}

	value := int64(0)
	if amount == 0 {
		value = CalcBlockSubsidy(nextBlockHeight, params)
	} else {
		value = amount
	}

	stdTxOut := &protos.TxOut{
		Value:    value,
		PkScript: pkScript,
		Asset:    asset,
	}
	tx.AddTxOut(stdTxOut)
	tx.TxContract.GasLimit = common.CoinbaseTxGas
	return asiutil.NewTx(tx), stdTxOut, nil
}

//fetch utxo from blockchain or input preOut param:
func (b *BlockChain) createNormalTx(
	privateKey string,
	asset protos.Asset,
	outputAddr common.Address,
	amount int64,
	fees int64,
	gasLimit uint32,
	usePreOut *protos.OutPoint) (*asiutil.Tx, error ) {

	acc, _ := crypto.NewAccount(privateKey)
	inputPkScript, err := txscript.PayToAddrScript(acc.Address)
	if err != nil {
		log.Errorf("createSignUpTx: payPkScript error")
		return nil,err
	}

	outputPkScript, err := txscript.PayToAddrScript(&outputAddr)
	if err != nil {
		log.Errorf("createSignUpTx: payPkScript error")
		return nil,err
	}

	view := txo.NewUtxoViewpoint()
	var prevOuts *[]protos.OutPoint
	prevOuts, err = b.FetchUtxoViewByAddressAndAsset(view, acc.Address.ScriptAddress(), &asset)
	if err != nil {
		log.Errorf("createSignUpTx: FetchUtxoViewByAddress error: %v",err)
		return nil, err
	}

	preOutsNums := len(*prevOuts)
	if preOutsNums == 0 {
		log.Warnf("createSignUpTx: no preOuts, can not createSignUpTx!")
		return nil,nil
	}

	txMsg := protos.NewMsgTx(protos.TxVersion)
	balance := int64(0)

	if usePreOut != nil {
		entry := view.LookupEntry(*usePreOut)
		if entry == nil || entry.IsSpent() {
			return nil,nil
		}
		txMsg.AddTxIn(&protos.TxIn{
			PreviousOutPoint: *usePreOut,
			SignatureScript:  nil,
			Sequence:         protos.MaxTxInSequenceNum,
		})
		balance += entry.Amount()

		if balance >= (amount+fees) {
			txMsg.AddTxOut(&protos.TxOut{
				Value:    amount,
				PkScript: outputPkScript,
				Asset:    *entry.Asset(),
				Data:     nil,
			})

			changeValue := balance - amount -fees
			if changeValue > 0 {
				txMsg.AddTxOut(&protos.TxOut{
					Value:    changeValue,
					PkScript: inputPkScript,
					Asset:    *entry.Asset(),
					Data:     nil,
				})
			}

			txMsg.TxContract.GasLimit = gasLimit

			// Sign the txMsg transaction.
			lookupKey := func(a common.IAddress) (*crypto.PrivateKey, bool, error) {
				return &acc.PrivateKey, true, nil
			}
			for i := 0; i < len(txMsg.TxIn); i++ {
				sigScript, err := txscript.SignTxOutput(txMsg, i,
					view.LookupEntry(*usePreOut).PkScript(), txscript.SigHashAll,
					txscript.KeyClosure(lookupKey), nil, nil)
				if err != nil {
					log.Errorf("createSignUpTx error:",err)
					return nil, err
				}
				txMsg.TxIn[i].SignatureScript = sigScript
			}
		}
	} else {
		for _, prevOut := range *prevOuts {
			entry := view.LookupEntry(prevOut)
			if entry == nil || entry.IsSpent() {
				continue
			}

			txMsg.AddTxIn(&protos.TxIn{
				PreviousOutPoint: prevOut,
				SignatureScript:  nil,
				Sequence:         protos.MaxTxInSequenceNum,
			})
			balance += entry.Amount()

			if balance >= (amount+fees) {
				txMsg.AddTxOut(&protos.TxOut{
					Value:    amount,
					PkScript: outputPkScript,
					Asset:    *entry.Asset(),
					Data:     nil,
				})

				changeValue := balance - amount -fees
				txMsg.AddTxOut(&protos.TxOut{
					Value:    changeValue,
					PkScript: inputPkScript,
					Asset:    *entry.Asset(),
					Data:     nil,
				})

				txMsg.TxContract.GasLimit = gasLimit

				// Sign the txMsg transaction.
				lookupKey := func(a common.IAddress) (*crypto.PrivateKey, bool, error) {
					return &acc.PrivateKey, true, nil
				}
				for i := 0; i < len(txMsg.TxIn); i++ {
					sigScript, err := txscript.SignTxOutput(txMsg, i,
						view.LookupEntry(prevOut).PkScript(), txscript.SigHashAll,
						txscript.KeyClosure(lookupKey), nil, nil)
					if err != nil {
						log.Errorf("createSignUpTx error:",err)
						return nil, err
					}
					txMsg.TxIn[i].SignatureScript = sigScript
				}
				break
			} else {
				continue
			}
		}
	}

	tx := asiutil.NewTx(txMsg)

	return tx, nil
}


func IntToByte(num int64) []byte {
	var buffer bytes.Buffer
	_ = binary.Write(&buffer, binary.BigEndian, num)
	return buffer.Bytes()
}

func createUtxoView(utxoList []*txo.UtxoEntry, view *txo.UtxoViewpoint, prevOutList *[]protos.OutPoint) error {
	if view == nil {
		return nil
	}
	for i := 0; i < len(utxoList); i++ {
		var testPoint protos.OutPoint
		data := int64(utxoList[i].Asset().Id + uint64(utxoList[i].Asset().Property) + uint64(utxoList[i].Amount()))
		assetsByte := IntToByte(data)
		testPoint.Hash = common.DoubleHashH(assetsByte)
		testPoint.Index = uint32(i+1)
		view.AddEntry(testPoint, utxoList[i])
		*prevOutList = append(*prevOutList, testPoint)
	}
	return nil
}

func createTxByParams(
	privateKey string,
	gaslimit uint32,
	prevOuts *[]protos.OutPoint,
	txOutList *[]protos.TxOut) (*protos.MsgTx, error) {

	acc, _ := crypto.NewAccount(privateKey)
	payAddrPkScript, _ := txscript.PayToAddrScript(acc.Address)
	txMsg := protos.NewMsgTx(protos.TxVersion)
	txMsg.TxContract.GasLimit = gaslimit
	if prevOuts != nil && len(*prevOuts) > 0 {
		for _, prevOut := range *prevOuts {
			txMsg.AddTxIn(&protos.TxIn{
				PreviousOutPoint: prevOut,
				SignatureScript:  nil,
				Sequence:         protos.MaxTxInSequenceNum,
			})
		}
	}

	if txOutList != nil && len(*txOutList) > 0 {
		for i:=0; i<len(*txOutList); i++ {
			tmp := (*txOutList)[i]
			txMsg.AddTxOut(&tmp)
		}
	}

	for i := 0; i < len(txMsg.TxIn); i++ {
		// Sign the txMsg transaction.
		lookupKey := func(a common.IAddress) (*crypto.PrivateKey, bool, error) {
			return &acc.PrivateKey, true, nil
		}
		sigScript, err := txscript.SignTxOutput(
			txMsg, i, payAddrPkScript, txscript.SigHashAll,
			txscript.KeyClosure(lookupKey), nil, nil)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		txMsg.TxIn[i].SignatureScript = sigScript
	}

	return txMsg, nil
}


//createTestTx:create normal tx by input param, not care whether the input utxo is validate:
func createTestTx(privString string, payAddrPkScript []byte, receiverPkScript []byte, gaslimit uint32,
	inputAmountList []int64, inputAssetsList []*protos.Asset,
	outputAmountList []int64, outputAssetsList []*protos.Asset) (*protos.MsgTx, map[protos.Asset]int64, *txo.UtxoViewpoint, error) {
	var err error
	var normalTx *protos.MsgTx
	fees := make(map[protos.Asset]int64)
	totalInCoin := make(map[protos.Asset]int64)
	totalOutCoin := make(map[protos.Asset]int64)
	utxoViewPoint := txo.NewUtxoViewpoint()
	{
		//create utxo list:
		utxoList := make([]*txo.UtxoEntry, 0)
		for i:=0; i < len(inputAmountList); i++ {
			utxo := txo.NewUtxoEntry(inputAmountList[i], payAddrPkScript,1,false, inputAssetsList[i], nil)
			utxoList = append(utxoList, utxo)
		}

		//output list:
		outputList := make([]protos.TxOut, 0)
		for i:=0; i<len(outputAmountList); i++ {
			txOut:=protos.NewContractTxOut(outputAmountList[i], receiverPkScript, *outputAssetsList[i], nil)
			outputList = append(outputList, *txOut)
		}

		preOutPoints := make([]protos.OutPoint, 0)
		createUtxoView(utxoList, utxoViewPoint, &preOutPoints)
		normalTx, err = createTxByParams(privString, gaslimit, &preOutPoints, &outputList)

		//input amount:
		for i:=0; i < len(inputAssetsList); i++ {
			inputAsset := inputAssetsList[i]
			if !inputAsset.IsIndivisible(){
				if _, ok := totalInCoin[*inputAsset]; ok {
					totalInCoin[*inputAsset] += inputAmountList[i]
				} else {
					totalInCoin[*inputAsset] = inputAmountList[i]
				}
			}
		}
		//output amount:
		for i:=0; i < len(outputAssetsList); i++ {
			outputAsset := outputAssetsList[i]
			if !outputAsset.IsIndivisible(){
				if _, ok := totalOutCoin[*outputAsset]; ok {
					totalOutCoin[*outputAsset] += outputAmountList[i]
				} else {
					totalOutCoin[*outputAsset] = outputAmountList[i]
				}
			}
		}
		for k, out := range totalOutCoin {
			in, ok := totalInCoin[k]
			if ok {
				if in > out {
					fees[k] = in - out
				}
				delete(totalInCoin, k)
			}
		}

		for k, v := range totalInCoin {
			fees[k] = v
		}

	}
	return normalTx, fees, utxoViewPoint, err
}

func MinimumTime(chainState *BestState) int64 {
	return chainState.TimeStamp + 1
}

func medianAdjustedTime(chainState *BestState, timeSource MedianTimeSource) int64 {
	// The timestamp for the block must not be before the median timestamp
	// of the last several blocks.  Thus, choose the maximum between the
	// current time and one second after the past median time.  The current
	// timestamp is truncated to a second boundary before comparison since a
	// block timestamp does not supported a precision greater than one
	// second.
	newTimestamp := timeSource.AdjustedTime()
	minTimestamp := MinimumTime(chainState)
	if newTimestamp < minTimestamp {
		newTimestamp = minTimestamp
	}

	return newTimestamp
}


func mergeUtxoView(viewA *txo.UtxoViewpoint, viewB *txo.UtxoViewpoint) {
	viewAEntries := viewA.Entries()
	for outpoint, entryB := range viewB.Entries() {
		if entryA, exists := viewAEntries[outpoint]; !exists ||
			entryA == nil || entryA.IsSpent() {

			viewAEntries[outpoint] = entryB
		}
	}
}

func spendTransaction(utxoView *txo.UtxoViewpoint, tx *asiutil.Tx, height int32) error {
	for _, txIn := range tx.MsgTx().TxIn {
		entry := utxoView.LookupEntry(txIn.PreviousOutPoint)
		if entry != nil {
			entry.Spend()
		}
	}

	utxoView.AddTxOuts(tx.Hash(), tx.MsgTx(), false, height)
	return nil
}

func rebuildFunder(tx *asiutil.Tx, stdTxout *protos.TxOut, fees *map[protos.Asset]int64) {
	for asset, value := range *fees {
		if value <= 0 {
			continue
		}

		if asset.IsIndivisible() {
			continue
		}
		if asset.Equal(&asiutil.AsimovAsset) {
			stdTxout.Value += value
		} else {
			tx.MsgTx().AddTxOut(protos.NewTxOut(value, stdTxout.PkScript, asset))
		}
	}
}

//create TestBlock:
func createTestBlock(
	b *BlockChain,
	round uint32,
	slot uint16,
	preHeight int32,
	asset protos.Asset,
	amount int64,
	payAddress *common.Address,
	txList []*asiutil.Tx,
	preNode *blockNode) (*asiutil.Block, error) {

	best := b.GetTip()
	var nextBlockHeight int32
	if preHeight == -1 {
		nextBlockHeight = best.height + 1
	} else {
		nextBlockHeight = preHeight + 1
	}
	ts := time.Now().Unix()

	blockTxns := make([]*asiutil.Tx, 0)
	blockUtxos := txo.NewUtxoViewpoint()
	allFees := make(map[protos.Asset]int64)

	//add txlist to block:
	for i:=0; i<len(txList); i++ {
		utxos, err := b.FetchUtxoView(txList[i],true)
		if err != nil {
			log.Warnf("Unable to fetch utxo view for tx %s: %v",
				txList[i].Hash(), err)
			continue
		}
		mergeUtxoView(blockUtxos, utxos)
	}

	for j:=0; j<len(txList); j++ {
		_, feeList, err := CheckTransactionInputs(txList[j], nextBlockHeight, blockUtxos, b)
		if err != nil {
			continue
		}
		spendTransaction(blockUtxos, txList[j], nextBlockHeight)
		MergeFees(&allFees, feeList)
		blockTxns = append(blockTxns, txList[j])
	}


	extraNonce := uint64(0)
	coinbaseScript, err := StandardCoinbaseScript(nextBlockHeight, extraNonce)
	if err != nil {
		return nil, err
	}

	//coinbaseTx:
	var contractOut *protos.TxOut
	if round > preNode.Round() {
		contractOut, err = b.createCoinbaseContractOut(preNode.Round(), preNode)
		if err != nil {
			return nil, err
		}
	}
	coinbaseTx, stdTxout, err := createTestCoinbaseTx(chaincfg.ActiveNetParams.Params,
		coinbaseScript, nextBlockHeight, payAddress, asset, amount, contractOut)
	if err != nil {
		return nil, err
	}
	rebuildFunder(coinbaseTx, stdTxout, &allFees)

	// flag whether core team take reward
	coreTeamRewardFlag := nextBlockHeight <= chaincfg.ActiveNetParams.Params.SubsidyReductionInterval ||
		ts - chaincfg.ActiveNetParams.Params.GenesisBlock.Header.Timestamp < 86400*(365*4+1)

	if coreTeamRewardFlag {
		fundationAddr := common.GenesisOrganization
		pkScript, _ := txscript.PayToAddrScript(&fundationAddr)
		txoutLen := len(coinbaseTx.MsgTx().TxOut)
		for i := 0; i < txoutLen; i++ {
			txOutAsset := coinbaseTx.MsgTx().TxOut[i].Asset
			if !txOutAsset.IsIndivisible() {
				value := coinbaseTx.MsgTx().TxOut[i].Value
				coreTeamValue := int64(float64(value) * common.CoreTeamPercent)
				if coreTeamValue > 0 {
					coinbaseTx.MsgTx().TxOut[i].Value = value - coreTeamValue
					coinbaseTx.MsgTx().AddTxOut(&protos.TxOut{
						Value:    coreTeamValue,
						PkScript: pkScript,
						Asset:    coinbaseTx.MsgTx().TxOut[i].Asset,
					})
				}
			}
		}
	}
	blockTxns = append(blockTxns, coinbaseTx)

	var merkles []*common.Hash
	if len(blockTxns) > 0 {
		merkles = BuildMerkleTreeStore(blockTxns)
	}
	var merkleRoot *common.Hash
	if len(merkles) > 0 {
		merkleRoot = merkles[len(merkles)-1]
	} else {
		merkleRoot = &common.Hash{}
	}

	var msgBlock protos.MsgBlock
	msgBlock.Header = protos.BlockHeader{
		Version:    1,
		PrevBlock:  best.hash,
		MerkleRoot: *merkleRoot,
		Timestamp:  ts,
		SlotIndex:  slot,
		Round:      round,
		Height:     nextBlockHeight,
		GasLimit:   CalcGasLimit(best.gasUsed, best.gasLimit, common.GasFloor, common.GasCeil),
		StateRoot:  best.stateRoot,
		CoinBase:   payAddress.StandardAddress(),
	}
	for _, tx := range blockTxns {
		msgBlock.AddTransaction(tx.MsgTx())
	}

	msgBlock.Header.PoaHash = msgBlock.CalculatePoaHash()
	block := asiutil.NewBlock(&msgBlock)

	return block, nil
}


func createAndSignBlock(paramstmp chaincfg.Params,
	accList []crypto.Account,
	validators []*common.Address,
	filters map[common.Address]uint16,
	chain *BlockChain,
	epoch uint32,
	slot uint16,
	preHeight int32,
	asset protos.Asset,
	amount int64,
	payAddress *common.Address,
	txList []*asiutil.Tx,
	timeAddCnt int32,
	preNode *blockNode) (*asiutil.Block, *blockNode, error) {

	block, err := createTestBlock(chain, epoch, slot, preHeight, asset, amount, payAddress, txList, preNode)
	if err != nil {
		return nil, nil, err
	}
	blockWeight := uint16(0)
	for k, v := range filters {
		if k == *validators[slot] {
			blockWeight += v
		}
	}
	block.MsgBlock().Header.Weight = blockWeight
	if preNode != nil {
		gasLimit := CalcGasLimit(preNode.GasUsed(), preNode.GasLimit(), common.GasFloor, common.GasCeil)
		block.MsgBlock().Header.GasLimit = gasLimit
		block.MsgBlock().Header.PrevBlock = preNode.hash
		stateRoot,gasUsed := getGasUsedAndStateRoot(chain, block,preNode)
		block.MsgBlock().Header.GasUsed = gasUsed
		block.MsgBlock().Header.StateRoot = *stateRoot
	}
	block.MsgBlock().Header.PoaHash = block.MsgBlock().CalculatePoaHash()
	if timeAddCnt > 0 {
		multTime := int64(timeAddCnt)*5134567889 / int64(time.Second)
		block.MsgBlock().Header.Timestamp = block.MsgBlock().Header.Timestamp + multTime
	}
	hash1 := block.MsgBlock().Header.BlockHash()
	index := 0
	for k:=0; k<len(paramstmp.GenesisCandidates); k++ {
		if paramstmp.GenesisCandidates[k] == block.MsgBlock().Header.CoinBase {
			index = k
			break
		}
	}
	signature, signErr := crypto.Sign(hash1[:], (*ecdsa.PrivateKey)(&accList[index].PrivateKey))
	if signErr != nil {
		return nil, nil, signErr
	}
	copy(block.MsgBlock().Header.SigData[:], signature)

	newBestNode := preNode
	if preNode != nil {
		round := preNode.round
		if round.Round != block.MsgBlock().Header.Round {
			round, err = chain.roundManager.GetNextRound(round)
			if err != nil {
				return nil, nil, err
			}
		}
		newBestNode = newBlockNode(round, &block.MsgBlock().Header,preNode)
	}

	return block, newBestNode, nil
}

func (b *BlockChain) calcGasUsed(node *blockNode, block *asiutil.Block, view *txo.UtxoViewpoint,
	stxos *[]txo.SpentTxOut, msgvblock *protos.MsgVBlock, statedb *state.StateDB) (types.Receipts, uint64, error) {

	// These utxo entries are needed for verification of things such as
	// transaction inputs, counting pay-to-script-hashes, and scripts.
	err := fetchInputUtxos(view, b.db, block)
	if err != nil {
		return nil, 0, err
	}

	var totalGasUsed uint64
	allFees := make(map[protos.Asset]int64)
	var (
		receipts types.Receipts
	)
	for i, tx := range block.Transactions() {
		_, feeList, err := CheckTransactionInputs(tx, node.height, view, b)
		if err != nil {
			return nil, 0, err
		}

		if feeList != nil {
			err = MergeFees(&allFees, feeList)
			if err != nil {
				return nil, 0, err
			}
		}

		// When we add gas used, blockhash maybe changed
		statedb.Prepare(*tx.Hash(), common.Hash{}, i)
		receipt, err, gasUsed, vtx, _ := b.ConnectTransaction(block, i, view, tx, stxos, statedb, 0)
		if receipt != nil {
			receipts = append(receipts, receipt)
		}
		if err != nil {
			return nil, 0, err
		}
		totalGasUsed += gasUsed
		if vtx != nil && msgvblock != nil {
			msgvblock.AddTransaction(vtx)
		}
	}

	// Update the best hash for view to include this block since all of its
	// transactions have been connected.
	view.SetBestHash(&node.hash)

	return receipts, totalGasUsed, nil
}


func getGasUsedAndStateRoot(b *BlockChain, block *asiutil.Block, preNode *blockNode) (*common.Hash, uint64){

	view := txo.NewUtxoViewpoint()
	view.SetBestHash(&preNode.hash)

	var err error
	blockHeader := &block.MsgBlock().Header
	round := preNode.round
	if round.Round != blockHeader.Round {
		round, err = b.roundManager.GetNextRound(round)
		if err != nil {
			return nil, 0
		}
	}
	newNode := newBlockNode(round, &block.MsgBlock().Header, preNode)
	stateDB, err := state.New(common.Hash(preNode.stateRoot), b.stateCache)
	if err != nil {
		return nil,0
	}
	_, gasUsed, err := b.calcGasUsed(newNode, block, view, nil, nil, stateDB)
	if err != nil {
		return nil,0
	}
	stateRoot, err := stateDB.Commit(true)
	if err != nil {
		return nil,0
	}

	block.MsgBlock().Header.GasUsed = gasUsed
	block.MsgBlock().Header.StateRoot = stateRoot
	newNode.gasUsed = gasUsed
	newNode.stateRoot = stateRoot

	return &stateRoot,gasUsed
}

//fetch utxo from blockchain or input preOut param:
func (b *BlockChain) createSignUpTx(privateKey string, asset protos.Asset) (*asiutil.Tx, error ) {
	fees := int64(chaincfg.DefaultAutoSignUpGasLimit)
	if fees < 0 {
		errStr := "createSignUpTx: fees is smaller than 0, can not gen signUpTx!"
		return nil,errors.New(errStr)
	}

	acc, _ := crypto.NewAccount(privateKey)
	payPkScript, err := txscript.PayToAddrScript(acc.Address)
	if err != nil {
		return nil,err
	}

	contractAddr, _, abi := b.GetSystemContractInfo(b.roundManager.GetContract())
	contractPkScript, err := txscript.PayToContractHash(contractAddr[:])
	if err != nil {
		return nil,err
	}
	signUpFunc := common.ContractConsensusSatoshiPlus_SignupFunction()

	view := txo.NewUtxoViewpoint()
	var prevOuts *[]protos.OutPoint
	prevOuts, err = b.FetchUtxoViewByAddressAndAsset(view, acc.Address.ScriptAddress(), &asset)
	if err != nil {
		log.Errorf("createSignUpTx: FetchUtxoViewByAddress error: %v",err)
		return nil, err
	}

	preOutsNums := len(*prevOuts)
	if preOutsNums == 0 {
		log.Warnf("createSignUpTx: no preOuts, can not createSignUpTx!")
		return nil,nil
	}
	txMsg := protos.NewMsgTx(protos.TxVersion)
	preOut := (*prevOuts)[0]
	entry := view.LookupEntry(preOut)
	if entry == nil || entry.IsSpent() {
		return nil,nil
	}

	txMsg.AddTxIn(&protos.TxIn{
		PreviousOutPoint: preOut,
		SignatureScript:  nil,
		Sequence:         protos.MaxTxInSequenceNum,
	})
	Data, DataErr := fvm.PackFunctionArgs(abi, signUpFunc)
	if DataErr != nil {
		return nil, DataErr
	}
	txMsg.AddTxOut(&protos.TxOut{
		Value:    int64(0),
		PkScript: contractPkScript,
		Asset:    asiutil.AsimovAsset,
		Data:     Data,
	})

	txMsg.AddTxOut(&protos.TxOut{
		Value:    entry.Amount(),
		PkScript: payPkScript,
		Asset:    asiutil.AsimovAsset,
		Data:     nil,
	})

	txMsg.TxContract.GasLimit = chaincfg.DefaultAutoSignUpGasLimit

	// Sign the txMsg transaction.
	lookupKey := func(a common.IAddress) (*crypto.PrivateKey, bool, error) {
		return &acc.PrivateKey, true, nil
	}
	for i := 0; i < len(txMsg.TxIn); i++ {
		sigScript, err := txscript.SignTxOutput(txMsg, i,
			payPkScript, txscript.SigHashAll,
			txscript.KeyClosure(lookupKey), nil, nil)
		if err != nil {
			return nil,err
		}
		txMsg.TxIn[i].SignatureScript = sigScript
	}
	tx := asiutil.NewTx(txMsg)
	return tx, nil
}

func createFakeChainByPrivateKeys(
	parivateKeyList []string,
	roundSize uint16) ([]crypto.Account, chaincfg.Params, *BlockChain, func(), error) {

	accList := make([]crypto.Account, 0)
	addressList := make([]common.Address, 0)
	for i := 0; i < len(parivateKeyList); i++ {
		acc, _ := crypto.NewAccount(parivateKeyList[i])
		accList = append(accList, *acc)
		addressList = append(addressList, acc.Address.StandardAddress())
	}

	netParam := chaincfg.MainNetParams
	if roundSize > 0 {
		netParam.RoundSize = roundSize
	}
	netParam.GenesisCandidates = addressList
	chain, teardownFunc, err := newFakeChain(&netParam)
	if err != nil || chain == nil {
		return nil, netParam, nil, nil, err
	}
	return accList, netParam, chain, teardownFunc, err

}
