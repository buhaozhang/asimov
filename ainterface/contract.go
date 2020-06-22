// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package ainterface

import (
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
)

type TemplateWarehouseContent struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	CreateTime   int64  `json:"createTime"`
	ApproveCount uint8  `json:"approveCount"`
	RejectCount  uint8  `json:"rejectCount"`
	Reviewers    uint8  `json:"reviewers"`
	Status       uint8  `json:"status"`
}

type Page struct {
	TotalLength int `json:"totalLength"`
	TemplateInfo []TemplateWarehouseContent
}

type RegistryCenterContent struct {
	Name       string `json:"name"`
	ByteCode   string `json:"byteCode"`
	CreateTime int64  `json:"createTime"`
	Status     uint8  `json:"status"`
}

type AssetInfo struct {
	Exist       bool     `json:"exist"`
	Name        string   `json:"name"`
	Symbol      string   `json:"symbol"`
	Description string   `json:"description"`
	Total       uint64   `json:"total"`
	History     []uint64 `json:"history"`
}

// ContractManager provides a generic interface that the is called when system contract state
// need to be validated and each round started from the tip of the main chain for the
// purpose of supporting system contracts.
type ContractManager interface {
	// Init manager by genesis data.
	Init(chain fvm.ChainContext, genesisCoinbaseData[] byte) error

	// Get latest contract by height.
	GetActiveContractByHeight(height int32, contractAddr common.Address) *chaincfg.ContractInfo

	GetContractAddressByAsset(
		gas uint64,
		block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		assets []string) ([]common.Address, bool, uint64)

	GetAssetInfoByAssetId(
		block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		assets []string) ([]AssetInfo, error)

	// Get fees from state
	GetFees(block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig) (map[protos.Asset]int32, error)

	// Get template from state
	GetTemplate(block *asiutil.Block,
		gas uint64,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		category uint16,
		name string) (TemplateWarehouseContent, bool, uint64)

	GetTemplates(block *asiutil.Block,
		gas uint64,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		getCountFunc string,
		getTemplatesFunc string,
		category uint16,
		pageNo int,
		pageSize int) (int, []TemplateWarehouseContent, error, uint64)

	// Get signed up validators
	GetSignedUpValidators(
		consensus common.Address,
		block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		miners []string) ([]common.Address, []uint32, error)

	IsLimit(block *asiutil.Block,
		stateDB vm.StateDB, asset *protos.Asset) int

	IsSupport(block *asiutil.Block,
		stateDB vm.StateDB, gasLimit uint64, asset *protos.Asset, address []byte) (bool, uint64)

	DisconnectBlock(block *asiutil.Block)
}
