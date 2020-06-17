// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package syscontract

import (
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
)

const (
	TEMPLATE_STATUS_SUBMIT   uint8 = 0
	TEMPLATE_STATUS_APPROVE  uint8 = 1
	TEMPLATE_STATUS_NOTEXIST uint8 = 2
	TEMPLATE_STATUS_DISABLE  uint8 = 3
)

// GetTemplates returns all submitted templates by calling system contract of template_warehouse
func (m *Manager) GetTemplates(
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
	countInput := common.PackGetTemplateCountInput(getCountFunc, category)

	result, leftOverGas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig, gas,
		common.TemplateWarehouse, countInput)
	if err != nil {
		log.Errorf("Get contract templates failed, error: %s", err)
		return 0, nil, err, leftOverGas
	}

	count, err := common.UnPackGetTemplatesCountResult(result)
	if err != nil {
		log.Error(err)
		return 0, nil, err, leftOverGas
	}
	if count == 0 {
		return 0, nil, nil, leftOverGas
	}

	// get information details
	template := make([]ainterface.TemplateWarehouseContent, 0)

	// settings of Pagination
	fromIndex := int(count) - pageNo*pageSize - 1
	if fromIndex < 0 {
		return 0, nil, nil, leftOverGas
	}
	endIndex := fromIndex + 1 - pageSize
	if endIndex < 0 {
		endIndex = 0
	}

	for i := fromIndex; i >= endIndex; i-- {
		detailInput := common.PackGetTemplateDetailInput(getTemplatesFunc, category, uint64(i))

		ret, leftOverGas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
			leftOverGas, common.TemplateWarehouse, detailInput)
		if err != nil {
			log.Errorf("Get contract templates failed, error: %s", err)
			return 0, nil, err, leftOverGas
		}

		name, key, createTime, approveCount, rejectCount, reviewers, status, err := common.UnPackGetTemplateDetailResult(ret)
		if err != nil {
			log.Error(err)
			return 0, nil, err, leftOverGas
		}
		if status != TEMPLATE_STATUS_NOTEXIST {
			template = append(template, ainterface.TemplateWarehouseContent{
				Name: name, Key: common.Bytes2Hex(key), CreateTime: createTime, ApproveCount: approveCount,
				RejectCount: rejectCount, Reviewers: reviewers, Status: status})
		}
	}

	return int(count), template, nil, leftOverGas
}

// GetTemplate returns template detail by its template name
func (m *Manager) GetTemplate(
	block *asiutil.Block,
	gas uint64,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	category uint16,
	name string) (ainterface.TemplateWarehouseContent, bool, uint64) {

	officialAddr := chaincfg.OfficialAddress
	input := common.PackGetTemplateInput(category, name)

	ret, leftOverGas, err := fvm.CallReadOnlyFunction(officialAddr, block, m.chain, stateDB, chainConfig,
		gas, common.TemplateWarehouse, input)
	if err != nil {
		log.Errorf("Get contract template failed, error: %s", err)
		return ainterface.TemplateWarehouseContent{}, false, leftOverGas
	}

	_, key, createTime, approveCount, rejectCount, reviewers, status, err := common.UnPackGetTemplateDetailResult(ret)
	if status == TEMPLATE_STATUS_NOTEXIST {
		return ainterface.TemplateWarehouseContent{}, false, leftOverGas
	}

	return ainterface.TemplateWarehouseContent{Name: name, Key: common.Bytes2Hex(key),
			CreateTime: createTime, ApproveCount: approveCount,
			RejectCount: rejectCount, Reviewers: reviewers,
			Status: status},
		true, leftOverGas
}