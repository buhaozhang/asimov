// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database/dbimpl/ethdb"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"io"
	"math/big"
	"os"
	"strings"
	"time"
)

var (
	start = []byte(`// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package chaincfg

import (
	"github.com/AsimovNetwork/asimov/common"
)`)

	nl            = []byte("\n")

	assetBytes = []*protos.Asset{
		protos.NewAsset(protos.DivisibleAsset, protos.DefaultOrgId, protos.DefaultCoinId),
	}
)

func write(w io.Writer, b []byte) {
	_, err := w.Write(b)
	if err != nil {
		panic(err)
	}
}

// newData1 returns files information of system contracts with type of byte slice
func newData1() []byte {
	jsonByte, err := json.Marshal(deployedTemplateContract)
	if err != nil {
		panic(err)
	}
	return jsonByte
}

func generateBlock(stateRoot common.Hash, merkleRoot common.Hash) *protos.MsgBlock {
	var genesisBlock = protos.MsgBlock{
		Header: protos.BlockHeader{
			Version:    1,
			PrevBlock:  common.Hash{}, // 0000000000000000000000000000000000000000000000000000000000000000
			MerkleRoot: merkleRoot,
			Timestamp:  1567123200, // 2019/8/30 00:00:00 +0000 UTC
			Height:     0,
			StateRoot:  stateRoot,
			GasLimit: params.GenesisGasLimit,
			GasUsed:  params.GenesisGasLimit,
			SigData:  [65]byte{},
		},
		Transactions: []*protos.MsgTx{coinbaseTx},
	}
	return &genesisBlock
}

var coinbaseTx = &protos.MsgTx{
	Version: 1,
	TxIn: []*protos.TxIn{
		{
			PreviousOutPoint: protos.OutPoint{
				Hash:  common.Hash{},
				Index: 0xffffffff,
			},
			SignatureScript: []byte{
				0x04, 0xff, 0xff, 0x00, 0x1d, 0x01, 0x04, 0x45, /* |.......E| */
				0x54, 0x68, 0x65, 0x20, 0x54, 0x69, 0x6d, 0x65, /* |The Time| */
				0x73, 0x20, 0x30, 0x35, 0x2f, 0x4a, 0x75, 0x6c, /* |s 05/Jul| */
				0x2f, 0x32, 0x30, 0x31, 0x39, 0x20, 0x43, 0x68, /* |/2019 Ch| */
				0x61, 0x6e, 0x63, 0x65, 0x6c, 0x6c, 0x6f, 0x72, /* |ancellor| */
				0x20, 0x6f, 0x6e, 0x20, 0x62, 0x72, 0x69, 0x6e, /* | on brin| */
				0x6b, 0x20, 0x6f, 0x66, 0x20, 0x73, 0x65, 0x63, /* |k of sec|*/
				0x6f, 0x6e, 0x64, 0x20, 0x62, 0x61, 0x69, 0x6c, /* |ond bail| */
				0x6f, 0x75, 0x74, 0x20, 0x66, 0x6f, 0x72, 0x20, /* |out for |*/
				0x62, 0x61, 0x6e, 0x6b, 0x73, /* |banks| */
			},
			Sequence: 0xffffffff,
		},
	},
	TxOut: []*protos.TxOut{
		{
			Value: 0,
			PkScript: []byte{
				0x41, 0x04, 0x67, 0x8a, 0xfd, 0xb0, 0xfe, 0x55, /* |A.g....U| */
				0x48, 0x27, 0x19, 0x67, 0xf1, 0xa6, 0x71, 0x30, /* |H'.g..q0| */
				0xb7, 0x10, 0x5c, 0xd6, 0xa8, 0x28, 0xe0, 0x39, /* |..\..(.9| */
				0x09, 0xa6, 0x79, 0x62, 0xe0, 0xea, 0x1f, 0x61, /* |..yb...a| */
				0xde, 0xb6, 0x49, 0xf6, 0xbc, 0x3f, 0x4c, 0xef, /* |..I..?L.| */
				0x38, 0xc4, 0xf3, 0x55, 0x04, 0xe5, 0x1e, 0xc1, /* |8..U....| */
				0x12, 0xde, 0x5c, 0x38, 0x4d, 0xf7, 0xba, 0x0b, /* |..\8M...| */
				0x8d, 0x57, 0x8a, 0x4c, 0x70, 0x2b, 0x6b, 0xf1, /* |.W.Lp+k.| */
				0x1d, 0x5f, 0xac, /* |._.| */
			},
			Asset: *assetBytes[0],
			Data:  newData1(),
		},
	},
	LockTime: 0,
}

func getSystemContractInfo(delegateAddr common.Address) (common.Address, []byte, string) {
	beneficiary := coinbaseTx.TxOut[0]
	cMap := chaincfg.TransferGenesisData(beneficiary.Data)

	var blockHeight int32 = 0
	contracts, ok := cMap[delegateAddr.String()]
	if !ok {
		return common.Address{}, nil, ""
	}
	for i := len(contracts) - 1; i >= 0; i-- {
		if blockHeight >= contracts[i].BlockHeight {
			return delegateAddr, contracts[i].Address, contracts[i].AbiInfo
		}
	}

	return common.Address{}, nil, ""
}

// createChainState initializes both the database and the chain state to the
// genesis block.  This includes creating the necessary buckets and inserting
// the genesis block, so it must only be called on an uninitialized database.
func createChainState() common.Hash {
	beneficiary := coinbaseTx.TxOut[0]
	stateDB := ethdb.NewMemDatabase()
	statedb, err := state.New(common.Hash{}, state.NewDatabase(stateDB))
	if err != nil {
		panic(err)
	}

	ts := time.Unix(1567123200, 0) // 2019/8/30 00:00:00 +0000 UTC

	// call fvm and create instances for system contracts
	context := vm.Context{
		CanTransfer: fvm.CanTransfer,
		Transfer:    fvm.Transfer,
		PackFunctionArgs:      fvm.PackFunctionArgs,
		UnPackFunctionResult:  fvm.UnPackFunctionResult,
		GetSystemContractInfo: getSystemContractInfo,
		Origin:                chaincfg.OfficialAddress,
		BlockNumber: new(big.Int).SetInt64(0),
		Time:        new(big.Int).SetInt64(ts.Unix()),
		GasPrice: new(big.Int).SetInt64(1),
	}

	chainConfig := &params.ChainConfig{
		ChainID: big.NewInt(0),
		Ethash:  new(params.EthashConfig),
	}
	vmenv := vm.NewFVM(context, statedb, chainConfig, vm.Config{})

	sender := vm.AccountRef(chaincfg.OfficialAddress)
	cMap := chaincfg.TransferGenesisData(beneficiary.Data)
	for k, v := range cMap {
		fmt.Println("init system contracts, contract name = " + v[0].Name + "；delegate contract = " + string(k))
		byteCode := common.Hex2Bytes(v[0].Code)
		_, addr, _, _, err := vmenv.Create(sender, byteCode, uint64(4604216000), common.Big0, &beneficiary.Asset, byteCode, nil, true)
		if err != nil {
			panic(err)
		}
		fmt.Println("contract address = ", addr.Hex())

		if v[0].InitCode != "" {
			proxyAddr := common.HexToAddress(k)
			_, _, _, err := vmenv.Call(sender, proxyAddr, common.Hex2Bytes(v[0].InitCode), uint64(4604216000), common.Big0, &beneficiary.Asset, true)
			if err != nil {
				panic(err)
			}
			fmt.Println("Call init function success.")
		}
	}

	stateRoot := statedb.IntermediateRoot(false)
	return stateRoot
}

func main() {
	pwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	pwdArray := strings.Split(pwd, "/")
	if pwdArray[len(pwdArray)-1] != "asimov" {
		panic(errors.New("set work directory to asimov"))
	}
	if len(os.Args) < 2 {
		panic(errors.New("please set arg of system contract folder"))
	}
	systemContractFolder := flag.String("systemContractPath", "", "folder of system contracts."+
		"\n eg.--systemContractPath ~/go/src/github.com/AsimovNetwork/asimov/systemcontracts/files")
	flag.Parse()
	if *systemContractFolder == "" {
		panic(errors.New("please set arg of system contract folder"))
	}

	// to generate new system contracts information, by two steps
	// 1、compile. when done, comment out it and goto step 2
	// 2、write contracts parameters and generate genesis data

	// please config your network first, default network is devnet
	// testnet mainnet devnet
	network := common.MainNet.String()

	//// first step:
	//fmt.Println("Step 1, write system contracts start...")
	//skipPoa := network == common.MainNet.String()
	//writeSystemContract(*systemContractFolder, network, skipPoa)
	//fmt.Println("Step 1, write system contracts end...")

	// second step:
	fmt.Println("Step 2, write genesis block start...")
	genesisHash := writeGenesisBin(network)
	writeGenesis(network, genesisHash)
	fmt.Println("Step 2, write genesis block end...")
}

func writeGenesisBin(testwork string) *common.Hash {
	fi, err := os.Create("genesisbin/"+testwork+".block")
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}
	defer fi.Close()

	stateRoot := createChainState()
	merkles := blockchain.BuildMerkleTreeStore([]*asiutil.Tx{asiutil.NewTx(coinbaseTx)})
	merkleRoot := *merkles[len(merkles)-1]

	msgblock := generateBlock(stateRoot, merkleRoot)
	block := asiutil.NewBlock(msgblock)
	blockbytes, err := block.Bytes()

	if err != nil {
		fmt.Println("ERROR when convert block to bytes", err.Error())
		return nil
	}

	_, err = fi.Write(blockbytes)
	if err != nil {
		fmt.Println("Write block bin error", err.Error())
		return nil
	}
	genesisHash := msgblock.Header.BlockHash()
	return &genesisHash
}

func writeGenesis(network string, genesisHash *common.Hash) {
	fi, err := os.Create("chaincfg/"+network+".go")
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	defer fi.Close()

	write(fi, start)
	write(fi, nl)

	// Write genesis hash
	write(fi, []byte(`
// `+network+`GenesisHash is the hash of the first block in the block chain for the `+network+`
// network (genesis block).
var `+network+`GenesisHash = common.HexToHash("`))
	write(fi, []byte(genesisHash.String()))
	write(fi, []byte(`")`))
	write(fi, nl)
}
