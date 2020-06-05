package vm

import (
	"errors"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/virtualtx"
	"math/big"
)

func FlowCreateAsset(amount *big.Int, contract *Contract, fvm *FVM, assetType uint32, coinIndex uint32) error {
	// current contract address
	orgAddr := *contract.CodeAddr

	registryCenterAddr, _, registryCenterABI := fvm.GetSystemContractInfo(common.RegistryCenter)
	createFunc := common.ContractRegistryCenter_CreateFunction()
	out, err := fvm.PackFunctionArgs(registryCenterABI, createFunc, orgAddr, assetType, coinIndex, amount, true)
	if err != nil {
		return errors.New("error packing function arguments for `CreateAsset`")
	}
	ret, leftOverGas, _, err := fvm.Call(AccountRef(registryCenterAddr), registryCenterAddr, out, contract.Gas, common.Big0, nil, true)
	contract.Gas = leftOverGas
	if err != nil {
		return err
	}
	var outType bool
	err = fvm.UnPackFunctionResult(registryCenterABI, &outType, createFunc, ret)
	if err != nil {
		return errors.New("error unpacking function result for `CreateAsset`")
	}
	if !outType {
		return errors.New("not authorized to create asset")
	}

	// find organization ID
	orgId, leftOverGas, err := GetOrganizationId(fvm, orgAddr, contract.Gas)
	contract.Gas = leftOverGas
	if err != nil {
		return err
	}

	// create asset and append the result to VTX
	assets := protos.NewAsset(assetType, orgId, coinIndex)
	fvm.Vtx.AppendVCreation(orgAddr, amount, assets, virtualtx.VTransferTypeCreation)

	return nil
}

func FlowMintAsset(amount *big.Int, contract *Contract, fvm *FVM, coinIndex uint32) error {
	// current contract address
	orgAddr := *contract.CodeAddr

	registryCenterAddr, _, registryCenterABI := fvm.GetSystemContractInfo(common.RegistryCenter)
	mintAssetFunction := common.ContractRegistryCenter_MintAssetFunction()
	getAssetInfoByAssetId := common.ContractRegistryCenter_GetAssetInfoByAssetIdFunction()
	mintInput, err := fvm.PackFunctionArgs(registryCenterABI, mintAssetFunction, orgAddr, coinIndex, amount)
	if err != nil {
		return errors.New("error packing function arguments for `MintAsset`")
	}

	_, leftOverGas, _, err := fvm.Call(AccountRef(registryCenterAddr), registryCenterAddr, mintInput, contract.Gas, common.Big0, nil, true)
	contract.Gas = leftOverGas
	if err != nil {
		return err
	}

	// find asset information
	coinType, orgId, leftOverGas, err := FindAsset(fvm, coinIndex, orgAddr, contract.Gas)
	contract.Gas = leftOverGas
	if err != nil {
		return err
	}
	if coinType & protos.InDivisibleAsset == protos.InDivisibleAsset {
		if amount.Cmp(common.BigMaxint64) > 0 || amount.Cmp(common.Big0) <= 0 {
			return errors.New("mint indivisible asset, amount out of bounds (0, 2^63)")
		}
	} else {
		if amount.Cmp(common.BigMaxxing) > 0 || amount.Cmp(common.Big0) <= 0 {
			return errors.New("mint divisible asset, amount out of bounds (0, bigMaxxing 1e18]")
		}
	}

	// check total asset amount
	getAssetInfoByAssetIdInput, err := fvm.PackFunctionArgs(registryCenterABI, getAssetInfoByAssetId, orgId, coinIndex)
	if err != nil {
		return errors.New("error packing function arguments for `getAssetInfoByAssetId`")
	}
	getAssetInfoByAssetIdResult, leftOverGas, _, err := fvm.Call(AccountRef(orgAddr), registryCenterAddr, getAssetInfoByAssetIdInput, contract.Gas, common.Big0, nil, true)
	if err != nil {
		return err
	}
	contract.Gas = leftOverGas
	var getAssetInfoByAssetIdOutType = &[]interface{}{new(bool), new(string), new(string), new(string), new(*big.Int), new([]*big.Int)}
	err = fvm.UnPackFunctionResult(registryCenterABI, getAssetInfoByAssetIdOutType, getAssetInfoByAssetId, getAssetInfoByAssetIdResult)
	if err != nil {
		return errors.New("error unpacking function result for `getAssetInfoByAssetId`")
	}
	if coinType & protos.InDivisibleAsset == protos.InDivisibleAsset {
		historyIssued := *((*getAssetInfoByAssetIdOutType)[5]).(*[]*big.Int)
		historyIssued = historyIssued[:len(historyIssued)-1]
		for _, issued := range historyIssued {
			if issued.Cmp(amount) == 0 {
				return errors.New("duplicated voucher id of indivisible asset")
			}
		}
	} else {
		totalIssued := *((*getAssetInfoByAssetIdOutType)[4]).(**big.Int)
		if totalIssued.Cmp(common.BigMaxxing) > 0 || totalIssued.Cmp(common.Big0) <= 0 {
			return errors.New("mint divisible asset, total amount out of bounds (0, bigMaxxing 1e18]")
		}
	}

	// mint asset and append the result to VTX
	assets := protos.NewAsset(coinType, orgId, coinIndex)
	fvm.Vtx.AppendVCreation(orgAddr, amount, assets, virtualtx.VTransferTypeMint)

	return nil
}

func FlowDeployContract(contract *Contract, fvm *FVM, category uint16, templateName string, args []byte) (common.Address, error) {
	// current contract address
	orgAddr := *contract.CodeAddr

	// get template information
	templateWarehouseAddr, templateWarehouseABI := fvm.GetTemplateWarehouseInfo()
	getTemplate := common.ContractTemplateWarehouse_GetTemplateFunction()
	runCode, err := fvm.PackFunctionArgs(templateWarehouseABI, getTemplate, category, templateName)
	if err != nil {
		return common.Address{}, err
	}
	ret, _, _, err := fvm.Call(AccountRef(orgAddr), templateWarehouseAddr, runCode, contract.Gas, common.Big0, nil, true)
	if err != nil {
		return common.Address{}, err
	}
	cTime := new(big.Int)
	var byteKey [32]byte
	outType := &[]interface{}{new(string), &byteKey, &cTime, new(uint8), new(uint8), new(uint8), new(uint8)}

	err = fvm.UnPackFunctionResult(templateWarehouseABI, outType, getTemplate, ret)
	if err != nil {
		return common.Address{}, err
	}

	// Get byte code by key
	keyHash := common.Hash(byteKey)
	_, _, byteCode, _, _, err := fvm.Context.FetchTemplate(fvm.View, &keyHash)
	if err != nil {
		return common.Address{}, err
	}

	status := *((*outType)[6]).(*uint8)
	if status != 1 {
		return common.Address{}, errors.New("contract template status is not approved")
	}

	// create contract
	createCode := byteCode
	if args != nil &&
		len(args) > 0 &&
		!isWasmCode(byteCode) {
		createCode = append(byteCode, args...)
		args = nil
	}

	_, contractAddr, _, _, err := fvm.Create(AccountRef(orgAddr), createCode, contract.Gas, common.Big0, nil, nil, args, true)
	if err != nil {
		return common.Address{}, err
	}

	// initialize contract instance
	initRunCode, err := fvm.PackFunctionArgs(common.TemplateABI, common.InitTemplateFunc, category, templateName)
	if err != nil {
		return common.Address{}, err
	}
	officialAddr := chaincfg.OfficialAddress
	_, _, _, err = fvm.Call(AccountRef(officialAddr), contractAddr, initRunCode, contract.Gas, common.Big0, nil, true)
	if err != nil {
		return common.Address{}, err
	}

	return contractAddr, nil
}
