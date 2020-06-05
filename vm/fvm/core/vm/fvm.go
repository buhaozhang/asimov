// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/virtualtx"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math/big"
	"sync/atomic"
	"time"
)

// emptyCodeHash is used by create to ensure deployment is disallowed to already
// deployed contract addresses (relevant after the account abstraction).
var emptyCodeHash = crypto.Keccak256Hash(nil)

type (
	// Check whether a transfer can happen
	CanTransferFunc func(*txo.UtxoViewpoint, *asiutil.Block, StateDB, common.Address, *big.Int, *virtualtx.VirtualTransaction, CalculateBalanceFunc, *protos.Asset) bool
	// Transfer asset
	TransferFunc func(StateDB, common.Address, common.Address, *big.Int, *virtualtx.VirtualTransaction, *protos.Asset)
	// GetHashFunc returns the nth block hash in the blockchain
	// and is used by the BLOCKHASH FVM op code.
	GetHashFunc func(uint64) common.Hash
	// Calculate balance
	CalculateBalanceFunc func(view *txo.UtxoViewpoint, block *asiutil.Block, address common.Address, assets *protos.Asset, voucherId int64) (int64, error)
	// Get template warehouse information
	GetTemplateWarehouseInfoFunc func() (common.Address, string)
	// Pack function arguments
	PackFunctionArgsFunc func(abiStr string, funcName string, args ...interface{}) ([]byte, error)
	// Unpack function results
	UnPackFunctionResultFunc func(abiStr string, v interface{}, funcName string, output []byte) error
	// Get system contract information
	GetSystemContractInfoFunc func(delegateAddr common.ContractCode) (common.Address, []byte, string)
	// Fetch a given template from template warehouse
	FetchTemplateFunc func(view *txo.UtxoViewpoint, hash *common.Hash) (uint16, []byte, []byte, []byte, []byte, error)
	// Get vote value
	VoteValueFunc func() int64
)

// run runs the given contract and takes care of running precompiles with a fallback to the byte code interpreter.
func run(fvm *FVM, contract *Contract, input []byte, readOnly bool) ([]byte, error) {
	if contract.CodeAddr != nil {
		precompiles := PrecompiledContractsHomestead

		if p := precompiles[*contract.CodeAddr]; p != nil {
			return RunPrecompiledContract(fvm, p, input, contract)
		}
	}
	for _, interpreter := range fvm.interpreters {
		if interpreter.CanRun(contract.Code) {
			if fvm.interpreter != interpreter {
				// Ensure that the interpreter pointer is set back
				// to its current value upon return.
				defer func(i Interpreter) {
					fvm.interpreter = i
				}(fvm.interpreter)
				fvm.interpreter = interpreter
			}

			return interpreter.Run(contract, input, readOnly)
		}
	}
	return nil, ErrNoCompatibleInterpreter
}

// Context provides the FVM with auxiliary information. Once provided
// it shouldn't be modified.
type Context struct {
	// CanTransfer returns whether the account contains
	// sufficient asset to transfer the value
	CanTransfer CanTransferFunc
	// Transfer transfers asset from one account to the other
	Transfer TransferFunc
	// GetHash returns the hash corresponding to n
	GetHash GetHashFunc

	CalculateBalance CalculateBalanceFunc
	GetTemplateWarehouseInfo GetTemplateWarehouseInfoFunc
	PackFunctionArgs PackFunctionArgsFunc
	UnPackFunctionResult UnPackFunctionResultFunc
	GetSystemContractInfo GetSystemContractInfoFunc
	FetchTemplate FetchTemplateFunc
	VoteValue VoteValueFunc

	// Message information
	Origin common.Address // Provides information for ORIGIN
	GasPrice *big.Int // Provides information for GASPRICE

	// Block information
	Coinbase    common.Address // Provides information for COINBASE
	GasLimit    uint64         // Provides information for GASLIMIT
	BlockNumber *big.Int       // Provides information for NUMBER
	Round       *big.Int       // Provides information for ROUND
	Time        *big.Int       // Provides information for TIME
	Difficulty  *big.Int       // Provides information for DIFFICULTY
	Block       *asiutil.Block // Block contains a balance cache which used in vm.
	View        *txo.UtxoViewpoint // Provides UtxoViewPoint
}

// FVM is the Asimov Virtual Machine base object and provides
// the necessary tools to run a contract on the given state with
// the provided context. It should be noted that any error
// generated through any of the calls should be considered a
// revert-state-and-consume-all-gas operation, no checks on
// specific errors should ever be performed. The interpreter makes
// sure that any errors generated are to be considered faulty code.

type FVM struct {
	// Context provides auxiliary blockchain related information
	Context
	// StateDB gives access to the underlying state
	StateDB StateDB
	// Depth is the current call stack
	depth int
	// chainConfig contains information about the current chain
	chainConfig *params.ChainConfig
	// virtual machine configuration options used to initialise the
	// fvm.
	vmConfig Config
	// global (to this context) asimov virtual machine
	// used throughout the execution of the tx.
	interpreters []Interpreter
	interpreter  Interpreter
	// abort is used to abort the FVM calling operations
	// NOTE: must be set atomically
	abort int32
	// callGasTemp holds the gas available for the current call. This is needed because the
	// available gas is calculated in gasCall* according to the 63/64 rule and later
	// applied in opCall*.
	callGasTemp uint64
	// Virtual Transaction
	Vtx *virtualtx.VirtualTransaction
}

// append wasm
func isWasmCode(code []byte) bool {
	wasmPreamble := []byte("\x00asm\x01\x00\x00\x00")
	return bytes.HasPrefix(code, wasmPreamble)
}

// append wasm
func NewFVM(ctx Context, statedb StateDB, chainConfig *params.ChainConfig, vmConfig Config) *FVM {
	return NewFVMWithVtx(ctx, statedb, chainConfig, vmConfig, nil)
}

func NewFVMWithVtx(ctx Context, statedb StateDB, chainConfig *params.ChainConfig, vmConfig Config,
	vtx *virtualtx.VirtualTransaction) *FVM {
	fvm := &FVM{
		Context:     ctx,
		StateDB:     statedb,
		vmConfig:    vmConfig,
		chainConfig: chainConfig,
		//chainRules:   chainConfig.Rules(ctx.BlockNumber),
		interpreters: make([]Interpreter, 0, 1),
	}

	// Keep the built-in FVM as the failover option.
	fvm.interpreters = append(fvm.interpreters, NewFVMInterpreter(fvm, vmConfig))
	if vtx == nil {
		fvm.Vtx = virtualtx.NewVirtualTransaction()
	} else {
		fvm.Vtx = vtx
	}

	return fvm
}

// Cancel cancels any running FVM operation. This may be called concurrently and
// it's safe to be called multiple times.
func (fvm *FVM) Cancel() {
	atomic.StoreInt32(&fvm.abort, 1)
}

// Interpreter returns the current interpreter
func (fvm *FVM) Interpreter() Interpreter {
	return fvm.interpreter
}

// Call executes the contract associated with the addr with the given input as
// parameters. It also handles any necessary value transfer required and takes
// the necessary steps to create accounts and reverses the state in case of an
// execution error or failed value transfer.
func (fvm *FVM) Call(caller ContractRef, addr common.Address, input []byte,
	gas uint64, value *big.Int, asset *protos.Asset,
	doTransfer bool) (ret []byte, leftOverGas uint64, snapshot int, err error) {
	if fvm.vmConfig.NoRecursion && fvm.depth > 0 {
		return nil, gas, -1, nil
	}
	// Fail if we're trying to execute above the call depth limit
	if fvm.depth > int(params.CallCreateDepth) {
		return nil, gas, -1, ErrDepth
	}

	leftOverGas = gas
	if doTransfer && !fvm.Context.CanTransfer(fvm.View, fvm.Block, fvm.StateDB, caller.Address(), value, fvm.Vtx, fvm.CalculateBalance, asset) {
		return nil, leftOverGas, -1, ErrInsufficientBalance
	}

	var to = AccountRef(addr)
	snapshot = fvm.StateDB.Snapshot()
	if !fvm.StateDB.Exist(addr) {
		precompiles := PrecompiledContractsHomestead
		if precompiles[addr] == nil && value.Sign() == 0 {
			// Calling a non existing account, don't do anything, but ping the tracer
			if fvm.vmConfig.Debug && fvm.depth == 0 {
				fvm.vmConfig.Tracer.CaptureStart(caller.Address(), addr, false, input, leftOverGas, value)
				fvm.vmConfig.Tracer.CaptureEnd(ret, 0, 0, nil)
			}
			return nil, leftOverGas, -1, nil
		}
		fvm.StateDB.CreateAccount(addr)
	}
	if doTransfer {
		fvm.Transfer(fvm.StateDB, caller.Address(), to.Address(), value, fvm.Vtx, asset)
	}

	// Initialise a new contract and set the code that is to be used by the FVM.
	// The contract is a scoped environment for this execution context only.
	contract := NewContract(caller, to, value, leftOverGas, asset)
	contract.SetCallCode(&addr, fvm.StateDB.GetCodeHash(addr), fvm.StateDB.GetCode(addr))

	start := time.Now()

	// Capture the tracer start/end events in debug mode
	if fvm.vmConfig.Debug && fvm.depth == 0 {
		fvm.vmConfig.Tracer.CaptureStart(caller.Address(), addr, false, input, leftOverGas, value)

		defer func() { // Lazy evaluation of the parameters
			fvm.vmConfig.Tracer.CaptureEnd(ret, leftOverGas-contract.Gas, time.Since(start), err)
		}()
	}
	ret, err = run(fvm, contract, input, false)

	// When an error was returned by the FVM or when setting the creation code
	// above we revert to the snapshot and consume any gas remaining. Additionally
	// when we're in homestead this also counts for code storage gas errors.
	if err != nil {
		fvm.StateDB.RevertToSnapshot(snapshot)
		snapshot = 0
		if err != errExecutionReverted {
			contract.UseGas(contract.Gas)
		}
	}
	return ret, contract.Gas, snapshot, err
}

// CallCode executes the contract associated with the addr with the given input
// as parameters. It also handles any necessary value transfer required and takes
// the necessary steps to create accounts and reverses the state in case of an
// execution error or failed value transfer.
//
// CallCode differs from Call in the sense that it executes the given address'
// code with the caller as context.
func (fvm *FVM) CallCode(caller ContractRef, addr common.Address, input []byte,
	gas uint64, value *big.Int, assets *protos.Asset) (ret []byte, leftOverGas uint64, err error) {
	if fvm.vmConfig.NoRecursion && fvm.depth > 0 {
		return nil, gas, nil
	}

	// Fail if we're trying to execute above the call depth limit
	if fvm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}

	leftOverGas = gas
	if !fvm.CanTransfer(fvm.View, fvm.Block, fvm.StateDB, caller.Address(), value, fvm.Vtx, fvm.CalculateBalance, assets) {
		return nil, leftOverGas, ErrInsufficientBalance
	}

	var (
		snapshot = fvm.StateDB.Snapshot()
		to = AccountRef(caller.Address())
	)
	// initialise a new contract and set the code that is to be used by the
	// FVM. The contract is a scoped environment for this execution context
	// only.
	contract := NewContract(caller, to, value, leftOverGas, assets)
	contract.SetCallCode(&addr, fvm.StateDB.GetCodeHash(addr), fvm.StateDB.GetCode(addr))

	ret, err = run(fvm, contract, input, false)
	if err != nil {
		fvm.StateDB.RevertToSnapshot(snapshot)
		if err != errExecutionReverted {
			contract.UseGas(contract.Gas)
		}
	}
	return ret, contract.Gas, err
}

// DelegateCall executes the contract associated with the addr with the given input
// as parameters. It reverses the state in case of an execution error.
//
// DelegateCall differs from CallCode in the sense that it executes the given address'
// code with the caller as context and the caller is set to the caller of the caller.
func (fvm *FVM) DelegateCall(caller ContractRef, addr common.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	if fvm.vmConfig.NoRecursion && fvm.depth > 0 {
		return nil, gas, nil
	}
	// Fail if we're trying to execute above the call depth limit
	if fvm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}

	var (
		snapshot = fvm.StateDB.Snapshot()
		to       = AccountRef(caller.Address())
	)

	// Initialise a new contract and make initialise the delegate values
	contract := NewContract(caller, to, nil, gas, nil).AsDelegate()
	contract.SetCallCode(&addr, fvm.StateDB.GetCodeHash(addr), fvm.StateDB.GetCode(addr))

	ret, err = run(fvm, contract, input, false)
	if err != nil {
		fvm.StateDB.RevertToSnapshot(snapshot)
		if err != errExecutionReverted {
			contract.UseGas(contract.Gas)
		}
	}
	return ret, contract.Gas, err
}

// StaticCall executes the contract associated with the addr with the given input
// as parameters while disallowing any modifications to the state during the call.
// Opcodes that attempt to perform such modifications will result in exceptions
// instead of performing the modifications.
func (fvm *FVM) StaticCall(caller ContractRef, addr common.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	// not support
	return nil, gas, nil
	if fvm.vmConfig.NoRecursion && fvm.depth > 0 {
		return nil, gas, nil
	}
	// Fail if we're trying to execute above the call depth limit
	if fvm.depth > int(params.CallCreateDepth) {
		return nil, gas, ErrDepth
	}
	// Make sure the readonly is only set if we aren't in readonly yet
	// this makes also sure that the readonly flag isn't removed for
	// child calls.
	if !fvm.interpreter.IsReadOnly() {
		fvm.interpreter.SetReadOnly(true)
		defer func() { fvm.interpreter.SetReadOnly(false) }()
	}

	var (
		to       = AccountRef(addr)
		snapshot = fvm.StateDB.Snapshot()
	)
	// Initialise a new contract and set the code that is to be used by the
	// FVM. The contract is a scoped environment for this execution context
	// only.
	contract := NewContract(caller, to, new(big.Int), gas, protos.NewAsset(protos.DivisibleAsset, protos.DefaultOrgId, protos.DefaultCoinId))
	contract.SetCallCode(&addr, fvm.StateDB.GetCodeHash(addr), fvm.StateDB.GetCode(addr))

	// When an error was returned by the FVM or when setting the creation code
	// above we revert to the snapshot and consume any gas remaining. Additionally
	// when we're in Homestead this also counts for code storage gas errors.
	ret, err = run(fvm, contract, input, true)
	if err != nil {
		fvm.StateDB.RevertToSnapshot(snapshot)
		if err != errExecutionReverted {
			contract.UseGas(contract.Gas)
		}
	}
	return ret, contract.Gas, err
}

// create creates a new contract using code as deployment code.
func (fvm *FVM) create(caller ContractRef, code []byte,
	gas uint64, value *big.Int, address common.Address, assets *protos.Asset, paramBytes []byte, doTransfer bool) (
	[]byte, common.Address, uint64, int, error) {
	// Depth check execution. Fail if we're trying to execute above the
	// limit.
	if fvm.depth > int(params.CallCreateDepth) {
		return nil, common.Address{}, gas, -1, ErrDepth
	}
	if doTransfer && !fvm.CanTransfer(fvm.View, fvm.Block, fvm.StateDB, caller.Address(), value, fvm.Vtx, fvm.CalculateBalance, assets) {
		return nil, common.Address{}, gas, -1, ErrInsufficientBalance
	}

	nonce := fvm.StateDB.GetNonce(caller.Address())
	fvm.StateDB.SetNonce(caller.Address(), nonce+1)

	// Ensure there's no existing contract already at the designated address
	contractHash := fvm.StateDB.GetCodeHash(address)
	if fvm.StateDB.GetNonce(address) != 0 || (contractHash != (common.Hash{}) && contractHash != emptyCodeHash) {
		return nil, common.Address{}, 0, -1, ErrContractAddressCollision
	}
	// Create a new account on the state
	snapshot := fvm.StateDB.Snapshot()
	fvm.StateDB.CreateAccount(address)
	fvm.StateDB.SetNonce(address, 1)

	if doTransfer {
		fvm.Transfer(fvm.StateDB, caller.Address(), address, value, fvm.Vtx, assets)
	}

	// initialise a new contract and set the code that is to be used by the
	// FVM. The contract is a scoped environment for this execution context
	// only.
	contract := NewContract(caller, AccountRef(address), value, gas, assets)
	contract.SetCallCode(&address, crypto.Keccak256Hash(code), code)

	if fvm.vmConfig.NoRecursion && fvm.depth > 0 {
		return nil, address, gas, -1, nil
	}

	if fvm.vmConfig.Debug && fvm.depth == 0 {
		fvm.vmConfig.Tracer.CaptureStart(caller.Address(), address, true, code, gas, value)
	}
	start := time.Now()

	// paramBytes always nil when contract code is evm bytes
	ret, err := run(fvm, contract, paramBytes, false)

	// check whether the max code size has been exceeded
	maxCodeSizeExceeded := len(ret) > params.MaxCodeSize
	// if the contract creation ran successfully and no errors were returned
	// calculate the gas required to store the code. If the code could not
	// be stored due to not enough gas set an error and let it be handled
	// by the error checking condition below.
	if err == nil && !maxCodeSizeExceeded {
		createDataGas := uint64(len(ret)) * params.CreateDataGas
		if contract.UseGas(createDataGas) {
			fvm.StateDB.SetCode(address, ret)
		} else {
			err = ErrCodeStoreOutOfGas
		}
	}

	// When an error was returned by the FVM or when setting the creation code
	// above we revert to the snapshot and consume any gas remaining. Additionally
	// when we're in homestead this also counts for code storage gas errors.
	if maxCodeSizeExceeded || err != nil {
		fvm.StateDB.RevertToSnapshot(snapshot)
		snapshot = -1
		if err != errExecutionReverted {
			contract.UseGas(contract.Gas)
		}
	}
	// Assign err if contract code size exceeds the max while the err is still empty.
	if maxCodeSizeExceeded && err == nil {
		err = errMaxCodeSizeExceeded
	}
	if fvm.vmConfig.Debug && fvm.depth == 0 {
		fvm.vmConfig.Tracer.CaptureEnd(ret, gas-contract.Gas, time.Since(start), err)
	}
	return ret, address, contract.Gas, snapshot, err

}

// Create creates a new contract using code as deployment code.
func (fvm *FVM) Create(caller ContractRef, code []byte,
	gas uint64, value *big.Int, assets *protos.Asset, inputHash []byte, paramBytes []byte, doTransfer bool) (
	ret []byte, contractAddr common.Address, leftOverGas uint64, snapshot int, err error) {
	if paramBytes != nil && !isWasmCode(code) {
		code = append(code, paramBytes...)
		paramBytes = nil
	}

	switch caller.Address()[0] {
	case common.ContractHashAddrID:
		var buf = make([]byte, 8)
		binary.BigEndian.PutUint64(buf, fvm.StateDB.GetNonce(caller.Address()))
		contractAddr, _ = crypto.CreateContractAddress(caller.Address().Bytes(), code, buf)
	case common.PubKeyHashAddrID:
		fallthrough
	case common.ScriptHashAddrID:
		contractAddr, _ = crypto.CreateContractAddress(caller.Address().Bytes(), []byte{}, inputHash)
	default:
		return nil, common.Address{}, gas, -1, errors.New("invalid caller address type")
	}

	if codeSize := fvm.StateDB.GetCodeSize(contractAddr); codeSize > 0 {
		return nil, common.Address{}, gas, -1, errors.New("contract already exist")
	}

	if IsSystemContract(contractAddr.Big()) {
		return nil, common.Address{}, gas, -1, errors.New("invalid contract address")
	}

	return fvm.create(caller, code, gas, value, contractAddr, assets, paramBytes, doTransfer)
}

// Create2 creates a new contract using code as deployment code.
//
// The different between Create2 with Create is Create2 uses sha3(0xff ++ msg.sender ++ salt ++ sha3(init_code))[12:]
// instead of the usual sender-and-nonce-hash as the address where the contract is initialized at.
func (fvm *FVM) Create2(caller ContractRef, code []byte, gas uint64, endowment *big.Int, salt *big.Int,
	assets *protos.Asset, checkBalance bool) (
	ret []byte, contractAddr common.Address, leftOverGas uint64, snapshot int, err error) {
	contractAddr = crypto.CreateAddress2(caller.Address(), common.BigToHash(salt), code)
	return fvm.create(caller, code, gas, endowment, contractAddr, assets, nil, checkBalance)
}

// ChainConfig returns the environment's chain configuration
func (fvm *FVM) ChainConfig() *params.ChainConfig { return fvm.chainConfig }
