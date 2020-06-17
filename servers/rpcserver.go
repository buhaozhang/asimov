// Copyright (c) 2018-2020 The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package servers

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/cache"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/common/hexutil"
	fnet "github.com/AsimovNetwork/asimov/common/net"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/mempool"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/rpcs/node"
	"github.com/AsimovNetwork/asimov/rpcs/rawdb"
	"github.com/AsimovNetwork/asimov/rpcs/rpc"
	"github.com/AsimovNetwork/asimov/rpcs/rpcjson"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/abi"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type PublicRpcAPI struct {
	stack *node.Node
	cfg   *rpcserverConfig
}

// NewPublicWeb3API creates a new Web3Service instance
func NewPublicRpcAPI(stack *node.Node, config *rpcserverConfig) *PublicRpcAPI {
	return &PublicRpcAPI{
		stack: stack,
		cfg:   config,
	}
}

func (s *PublicRpcAPI) GetBlockChainInfo() (interface{}, error) {
	// Obtain a snapshot of the current best known blockchain state. We'll
	// populate the response to this call primarily from this snapshot.
	params := s.cfg.ChainParams
	chain := s.cfg.Chain
	chainSnapshot := chain.BestSnapshot()

	chainInfo := &rpcjson.GetBlockChainInfoResult{
		Chain:         params.Name(),
		Blocks:        chainSnapshot.Height,
		BestBlockHash: chainSnapshot.Hash.UnprefixString(),
		MedianTime:    chainSnapshot.TimeStamp,
		Round:         int32(chainSnapshot.Round),
		Slot:          int16(chainSnapshot.SlotIndex),
	}

	return chainInfo, nil
}

func (s *PublicRpcAPI) GetBlockHash(blockHeight int32) (string, error) {
	hash, err := s.cfg.Chain.BlockHashByHeight(blockHeight)
	if err != nil {
		return "", &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCBlockHashNotFound,
			Message: "Failed to get block hash by height",
		}
	}
	return hash.UnprefixString(), nil
}

func (s *PublicRpcAPI) GetBlockHeight(blockHash string) (int32, error) {
	hash := common.HexToHash(blockHash)
	height, err := s.cfg.Chain.BlockHeightByHash(&hash)
	if err != nil {
		return 0, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCBlockHeightNotFound,
			Message: "Failed to get block height by hash",
		}
	}
	return height, nil
}

func (s *PublicRpcAPI) UpTime() (int64, error) {
	return time.Now().Unix() - s.cfg.StartupTime, nil
}

// Check whether the provided string format address is a valid Asimov 21-byte address
func (s *PublicRpcAPI) ValidateAddress(address string) (bool, error) {
	_, err := asiutil.DecodeAddress(address)
	if err != nil {
		// Return false if error occurs.
		return false, nil
	}

	return true, nil
}

func (s *PublicRpcAPI) GetCurrentNet() (common.AsimovNet, error) {
	return s.cfg.ChainParams.Net, nil
}

func (s *PublicRpcAPI) GetBestBlock() (*rpcjson.GetBestBlockResult, error) {
	best := s.cfg.Chain.BestSnapshot()
	result := &rpcjson.GetBestBlockResult{
		Hash:   best.Hash.UnprefixString(),
		Height: best.Height,
	}
	return result, nil
}

func (s *PublicRpcAPI) GetBlock(blockHash string, verbose bool, verboseTx bool) (interface{}, error) {
	// Load the raw block bytes from the database.
	hash := common.HexToHash(blockHash)
	var blkBytes []byte
	err := s.cfg.DB.View(func(dbTx database.Tx) error {
		var err error
		blkBytes, err = dbTx.FetchBlock(database.NewNormalBlockKey(&hash))
		return err
	})
	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCBlockNotFound,
			Message: "Block not found",
		}
	}

	// When the verbose flag isn't set, simply return the serialized block
	// as a hex-encoded string.
	if !verbose {
		return hex.EncodeToString(blkBytes), nil
	}

	// The verbose flag is set, so generate the JSON object and return it.

	// Deserialize the block.
	blk, err := asiutil.NewBlockFromBytes(blkBytes)
	if err != nil {
		context := "Failed to deserialize block"
		return nil, internalRPCError(err.Error(), context)
	}

	// Get the block height from chain.
	blockHeight, err := s.cfg.Chain.BlockHeightByHash(&hash)
	if err != nil {
		context := "Failed to obtain block height"
		return nil, internalRPCError(err.Error(), context)
	}
	best := s.cfg.Chain.BestSnapshot()

	// Get next block hash unless there are none.
	var nextHashString string
	if blockHeight < best.Height {
		nextHash, err := s.cfg.Chain.BlockHashByHeight(blockHeight + 1)
		if err != nil {
			context := "Failed to obtain next block"
			return nil, internalRPCError(err.Error(), context)
		}
		nextHashString = nextHash.UnprefixString()
	}

	blockHeader := &blk.MsgBlock().Header
	blockReply := rpcjson.GetBlockVerboseResult{
		Hash:          blockHash,
		Version:       blockHeader.Version,
		MerkleRoot:    blockHeader.MerkleRoot.String(),
		PreviousHash:  blockHeader.PrevBlock.String(),
		StateRoot:     blockHeader.StateRoot.String(),
		Time:          blockHeader.Timestamp,
		Round:         blockHeader.Round,
		Slot:          blockHeader.SlotIndex,
		GasLimit:      blockHeader.GasLimit,
		GasUsed:       blockHeader.GasUsed,
		Weight:        blockHeader.Weight,
		Confirmations: int64(1 + best.Height - blockHeight),
		Height:        int64(blockHeight),
		Size:          int32(len(blkBytes)),
		NextHash:      nextHashString,
	}

	if !verboseTx {
		transactions := blk.Transactions()
		txNames := make([]string, len(transactions))
		for i, tx := range transactions {
			txNames[i] = tx.Hash().String()
		}

		blockReply.Tx = txNames
	} else {
		txns := blk.Transactions()
		rawTxns := make([]rpcjson.TxResult, len(txns))
		for i, tx := range txns {
			rawTxn, err := createTxResult(*s.cfg, tx.MsgTx(),
				tx.Hash().String(), nil, hash.UnprefixString(),
				blockHeight, best.Height, false)
			if err != nil {
				return nil, err
			}
			rawTxns[i] = *rawTxn
		}
		blockReply.RawTx = rawTxns
	}

	if len(blk.MsgBlock().PreBlockSigs) > 0 {
		msgSigs := blk.MsgBlock().PreBlockSigs
		sigResults := make([]rpcjson.MsgSignResult, len(msgSigs))
		for i, sig := range msgSigs {
			rawMsgSign, err := createMsgSignResult(sig)
			if err != nil {
				return nil, err
			}
			sigResults[i] = *rawMsgSign
		}
		blockReply.PreSigList = sigResults
	}

	return blockReply, nil
}

func (s *PublicRpcAPI) GetBlockHeader(blockHash string, verbose bool) (interface{}, error) {
	// Fetch the header from chain.
	hash := common.HexToHash(blockHash)
	blockHeader, err := s.cfg.Chain.FetchHeader(&hash)
	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCBlockHeaderNotFound,
			Message: "Block header not found",
		}
	}

	// When the verbose flag isn't set, simply return the serialized block
	// header as a hex-encoded string.
	if !verbose {
		var headerBuf bytes.Buffer
		err := blockHeader.Serialize(&headerBuf)
		if err != nil {
			context := "Failed to serialize block header"
			return nil, internalRPCError(err.Error(), context)
		}
		return hex.EncodeToString(headerBuf.Bytes()), nil
	}

	// The verbose flag is set, so generate the JSON object and return it.

	// Get the block height from chain.
	blockHeight, err := s.cfg.Chain.BlockHeightByHash(&hash)
	if err != nil {
		context := "Failed to obtain block height"
		return nil, internalRPCError(err.Error(), context)
	}
	best := s.cfg.Chain.BestSnapshot()

	// Get next block hash unless there are none.
	var nextHashString string
	if blockHeight < best.Height {
		nextHash, err := s.cfg.Chain.BlockHashByHeight(blockHeight + 1)
		if err != nil {
			context := "Failed to obtain next block"
			return nil, internalRPCError(err.Error(), context)
		}
		nextHashString = nextHash.UnprefixString()
	}

	blockHeaderReply := rpcjson.GetBlockHeaderVerboseResult{
		Hash:          blockHash,
		Confirmations: int64(1 + best.Height - blockHeight),
		Height:        blockHeight,
		Version:       blockHeader.Version,
		MerkleRoot:   blockHeader.MerkleRoot.String(),
		StateRoot:    blockHeader.StateRoot.String(),
		NextHash:     nextHashString,
		PreviousHash: blockHeader.PrevBlock.String(),
		Time:         blockHeader.Timestamp,
		GasLimit:     int64(blockHeader.GasLimit),
		GasUsed:      int64(blockHeader.GasUsed),
	}
	return blockHeaderReply, nil
}

func (s *PublicRpcAPI) getBalance(address string) ([]rpcjson.GetBalanceResult, error) {
	byte, err := hexutil.Decode(address)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode string address")
	}

	view := txo.NewUtxoViewpoint()
	assetsMap := make(map[string]int64)
	balance := make([]rpcjson.GetBalanceResult, 0)

	_, err = s.cfg.Chain.FetchUtxoViewByAddress(view, byte)
	if err == nil {
		for _, e := range view.Entries() {
			assets := hex.EncodeToString(e.Asset().Bytes())
			if e.Asset().IsIndivisible() {
				b := rpcjson.GetBalanceResult{
					Asset: assets,
					Value: strconv.FormatInt(e.Amount(), 10),
				}
				balance = append(balance, b)
			} else {
				if amount, ok := assetsMap[assets]; ok {
					assetsMap[assets] = amount + e.Amount()
				} else {
					assetsMap[assets] = e.Amount()
				}
			}
		}
	}

	for k, v := range assetsMap {
		b := rpcjson.GetBalanceResult{
			Asset: k,
			Value: strconv.FormatInt(v, 10),
		}
		balance = append(balance, b)
	}

	return balance, nil
}

// Get balance of an address.
// The result contains a list of {asset, value} which the address owns.
func (s *PublicRpcAPI) GetBalance(address string) ([]rpcjson.GetBalanceResult, error) {
	return s.getBalance(address)
}

// Get balance for each address in the array.
func (s *PublicRpcAPI) GetBalances(addresses []string) (interface{}, error) {
	result := make([]rpcjson.GetBalancesResult, 0)

	for _, address := range addresses {

		balance, err := s.getBalance(address)

		if err != nil {
			return nil, internalRPCError(err.Error(), "Failed to decode one of string addresses")
		}

		balanceWithAddress := rpcjson.GetBalancesResult{
			Address: address,
			Assets: balance,
		}

		result = append(result, balanceWithAddress)
	}

	return result, nil
}

func (s *PublicRpcAPI) GetBlockListByHeight(offset int32, count int32) (interface{}, error) {
	resultBlocks := make([]rpcjson.GetBlockVerboseResult, 0)

	best := s.cfg.Chain.BestSnapshot()
	for i := int32(0); i < count; i++ {
		hash, err := s.cfg.Chain.BlockHashByHeight(offset + i)
		if err != nil {
			break
		}
		block, vBlock, err := asiutil.GetBlockPair(s.cfg.DB, hash)
		if err != nil {
			if _, ok := err.(asiutil.MissVBlockError); ok && hash.IsEqual(chaincfg.ActiveNetParams.GenesisHash) {
				vBlock = asiutil.NewVBlock(&protos.MsgVBlock{}, hash)
			} else {
				context := "Failed to get block"
				return nil, internalRPCError(err.Error(), context)
			}
		}

		header := block.MsgBlock().Header
		var totalFees rpcjson.FeeResult
		var totalRewardValue int64

		FeesList := make([]rpcjson.FeeResult, 0)

		if header.Height == 0 {
			totalRewardValue = 0
		} else {
			tmpReward := blockchain.CalcBlockSubsidy(header.Height, s.cfg.ChainParams)
			totalRewardValue = tmpReward
			for _, txOut := range block.MsgBlock().Transactions[0].TxOut {
				v := txOut.Value
				if txOut.Asset.Equal(&asiutil.AsimovAsset) {
					v -= tmpReward
				}
				if v > 0 {
					totalFees.Asset = hex.EncodeToString(txOut.Asset.Bytes())
					totalFees.Value = v
					FeesList = append(FeesList, totalFees)
				}
			}
		}

		var receipts types.Receipts
		rawTxns := make([]rpcjson.TxResult, 0)
		receipts = rawdb.ReadReceipts(s.cfg.Chain.EthDB(), *hash, uint64(header.Height))

		for _, tx := range block.MsgBlock().Transactions {
			rawTxn, err := createTxResult(*s.cfg, tx,
				tx.TxHash().String(), &header, hash.String(),
				header.Height, best.Height, false)
			if err != nil {
				return nil, err
			}

			rawTxns = append(rawTxns, *rawTxn)
		}

		vtxRawTxns := make([]rpcjson.TxResult, 0)
		for _, tx := range vBlock.MsgVBlock().VTransactions {
			rawTxn, err := createTxResult(*s.cfg, tx,
				tx.TxHash().String(), &header, hash.String(),
				header.Height, best.Height, true)
			if err != nil {
				return nil, err
			}

			vtxRawTxns = append(vtxRawTxns, *rawTxn)
		}
		var receiptResult = make([]*rpcjson.ReceiptResult, 0)
		for _, receipt := range receipts {
			rcpt, err := createTxReceiptResult(receipt)
			if err != nil {
				return nil, internalRPCError(err.Error(), "Failed to decode transaction receipt")
			}
			receiptResult = append(receiptResult, rcpt)
		}
		// Get next block hash unless there are none.
		var nextHashString string
		if header.Height < best.Height {
			nextHash, err := s.cfg.Chain.BlockHashByHeight(header.Height + 1)
			if err == nil {
				nextHashString = nextHash.String()
			}
		}

		blockReply := rpcjson.GetBlockVerboseResult{
			Hash:          hash.String(),
			Confirmations: int64(1 + best.Height - header.Height),
			Size:          int32(block.MsgBlock().SerializeSize()),
			Height:        int64(header.Height),
			NextHash:      nextHashString,
			Version:       header.Version,
			Time:          header.Timestamp,
			MerkleRoot:    header.MerkleRoot.String(),
			PreviousHash:  header.PrevBlock.String(),
			TxCount:       uint64(len(block.MsgBlock().Transactions)),
			Round:         header.Round,
			Slot:          header.SlotIndex,
			Weight:        header.Weight,
			RawTx:         rawTxns,
			Vtxs:          vtxRawTxns,
			Receipts:      receiptResult,
			GasLimit:      header.GasLimit,
			GasUsed:       header.GasUsed,
			Reward:        totalRewardValue,
			FeeList:       FeesList,
		}
		resultBlocks = append(resultBlocks, blockReply)
		if len(nextHashString) == 0 {
			break
		}
	}
	return resultBlocks, nil
}

func (s *PublicRpcAPI) makeupUTXO(out protos.OutPoint, e *txo.UtxoEntry, addr *common.Address) (*rpcjson.ListUnspentResult) {
	cfg := s.cfg.Chain.GetTip().Height() - e.BlockHeight()
	r := &rpcjson.ListUnspentResult{
		TxID:          out.Hash.UnprefixString(),
		Vout:          out.Index,
		Assets:        hex.EncodeToString(e.Asset().Bytes()),
		ScriptPubKey:  hex.EncodeToString(e.PkScript()),
		Amount:        e.Amount(),
		Address:       addr.String(),
		Confirmations: int64(cfg),
		Height:        e.BlockHeight(),
		Spendable:     !e.IsCoinBase() || cfg >= chaincfg.ActiveNetParams.CoinbaseMaturity,
	}
	if e.LockItem() != nil {
		r.ListLockEntry = make([]*rpcjson.LockEntryResult, 0)
		for _, le := range e.LockItem().EntriesList() {
			r.ListLockEntry = append(r.ListLockEntry, &rpcjson.LockEntryResult{
				Id:     hex.EncodeToString(le.Id[:]),
				Amount: le.Amount,
			})
		}
	}
	return r
}

// Get list of UTXO of a given asset for all address in the array.
// If asset is not specified, all assets will be fetched.
func (s *PublicRpcAPI) GetUtxoByAddress(addresses []string, asset string) (interface{}, error) {
	var a protos.Asset
	if asset != "" {
		aa, err := hex.DecodeString(asset)
		if err != nil {
			return nil, internalRPCError(err.Error(), "Failed to decode asset")
		}
		a = *protos.AssetFromBytes(aa)
	}

	utxos := make([]*rpcjson.ListUnspentResult, 0)
	totalCount := uint32(100)
	for _, addr := range addresses {
		byte, err := hexutil.Decode(addr)
		if err != nil {
			return nil, internalRPCError(err.Error(), "Failed to decode address")
		}

		addr, err := common.NewAddress(byte)
		if err != nil {
			return nil, internalRPCError(err.Error(), "Failed to create ADDRESS object")
		}

		view := txo.NewUtxoViewpoint()
		_, err = s.cfg.Chain.FetchUtxoViewByAddress(view, addr.ScriptAddress())
		if err == nil {
			count := uint32(0)
			for out, e := range view.Entries() {
				count = count + uint32(1)

				if count > totalCount {
					break
				}

				if asset == "" || e.Asset().Equal(&a) {
					r := s.makeupUTXO(out, e, addr)
					utxos = append(utxos, r)
				}
			}
		}
	}

	return utxos, nil
}

type tempItem struct {
	entry    *txo.UtxoEntry
	outPoint protos.OutPoint
}

func quickSort(arr []*tempItem) {
	less := func(a, b *tempItem) bool {
		if a.entry.BlockHeight() < b.entry.BlockHeight() {
			return true
		}
		if a.entry.BlockHeight() > b.entry.BlockHeight() {
			return false
		}
		r := bytes.Compare(a.outPoint.Hash[:], a.outPoint.Hash[:])
		return r < 0 || r == 0 && a.outPoint.Index < a.outPoint.Index
	}

	var partition func(lo int, piv int, arr []*tempItem) int

	partition = func(lo int, piv int, arr []*tempItem) int {
		is := lo

		for i := lo; i < piv; i++ {
			if less(arr[i], arr[piv]) {
				if i != is {
					arr[i], arr[is] = arr[is], arr[i]
				}

				is++
			}
		}

		arr[is], arr[piv] = arr[piv], arr[is]

		if is-1 > lo {
			partition(lo, is-1, arr)
		}
		if is+1 < piv {
			partition(is+1, piv, arr)
		}

		return is
	}

	l := len(arr)
	piv := l - 1

	partition(0, piv, arr)
}

// Get UTXO of a given asset for an address.
// If asset is not specified, all assets will be fetched.
// The qualified UTXO is sorted and the page [from, from+count] will be retrieved.
func (s *PublicRpcAPI) GetUtxoInPage(address string, asset string, from, count int32) (interface{}, error) {
	var a protos.Asset
	if asset != "" {
		aa, err := hex.DecodeString(asset)
		if err != nil {
			return nil, internalRPCError(err.Error(), "Failed to decode asset")
		}
		a = *protos.AssetFromBytes(aa)
	}
	byte, err := hexutil.Decode(address)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode address")
	}
	addr, err := common.NewAddress(byte)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to create ADDRESS object")
	}

	utxos := make([]*rpcjson.ListUnspentResult, 0, count)
	view := txo.NewUtxoViewpoint()
	_, err = s.cfg.Chain.FetchUtxoViewByAddress(view, addr.ScriptAddress())
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to get utxo in page")
	}

	tempList := make([]*tempItem, 0, len(view.Entries()))
	for out, e := range view.Entries() {
		tempList = append(tempList, &tempItem{
			e, out,
		})
	}

	if len(tempList) > 1 {
		quickSort(tempList)
	}

	idx := int32(0)
	for i := int(0); i < len(tempList); i++ {
		e := tempList[i].entry
		out := tempList[i].outPoint
		if asset == "" || e.Asset().Equal(&a) {
			idx++
			if idx <= from || idx > from+count {
				continue
			}
			r := s.makeupUTXO(out, e, addr)
			utxos = append(utxos, r)
		}
	}
	page := rpcjson.UnspentPageResult{
		utxos, idx,
	}

	return page, nil
}

func (s *PublicRpcAPI) GetNetTotals() (interface{}, error) {
	totalBytesRecv, totalBytesSent := s.cfg.ConnMgr.NetTotals()
	reply := &rpcjson.GetNetTotalsResult{
		TotalBytesRecv: totalBytesRecv,
		TotalBytesSent: totalBytesSent,
		TimeMillis:     time.Now().UTC().UnixNano() / int64(time.Millisecond),
	}
	return reply, nil
}


func (s *PublicRpcAPI) CreateRawTransaction(inputs []rpcjson.TransactionInput, outputs []rpcjson.TransactionOutput, lockTime *int64, gasLimit *int32) (interface{}, error) {

	// Validate the locktime, if given.
	if lockTime != nil &&
		(*lockTime < 0 || *lockTime > int64(protos.MaxTxInSequenceNum)) {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCInvalidParameter,
			Message: "Locktime out of range",
		}
	}

	// Add all transaction inputs to a new transaction after performing
	// some validity checks.
	mtx := protos.NewMsgTx(protos.TxVersion)
	firstInputScriptPubKey := []byte{}
	var err error
	for idx, input := range inputs {
		txHash := common.HexToHash(input.Txid)

		prevOut := protos.NewOutPoint(&txHash, input.Vout)
		txIn := protos.NewTxIn(prevOut, []byte{})
		if lockTime != nil && *lockTime != 0 {
			txIn.Sequence = protos.MaxTxInSequenceNum - 1
		}
		mtx.AddTxIn(txIn)

		if idx == 0 {
			firstInputScriptPubKey, err = hex.DecodeString(input.ScriptPubKey)
			if err != nil {
				return nil, rpcDecodeHexError(input.ScriptPubKey)
			}
		}
	}

	// Add all transaction outputs to the transaction after performing
	// some validity checks.
	// iterate all outputs to generate contract address if it is contract create transaction
	contractAddress := make(map[uint64]string)
	for index, output := range outputs {
		ctype := output.ContractType

		data, err := hex.DecodeString(output.Data)
		if err != nil {
			return nil, rpcDecodeHexError(output.Data)
		}
		pkScript := []byte{}
		amount, err := strconv.ParseInt(output.Amount, 10, 64)
		if err != nil {
			return nil, &rpcjson.RPCError{
				Code:    rpcjson.ErrRPCInvalidOutPutAmount,
				Message: "Invalid amount",
			}
		}
		assets, err := hex.DecodeString(output.Assets)
		if err != nil {
			return nil, rpcDecodeHexError(output.Assets)
		}

		if ctype == txscript.CreateTy.String() || ctype == txscript.CallTy.String() || ctype == txscript.TemplateTy.String() || ctype == txscript.VoteTy.String() {

			// Ensure amount is in the valid range for monetary amounts.
			if amount < 0 || amount > common.MaxXing {
				return nil, &rpcjson.RPCError{
					Code:    rpcjson.ErrRPCInvalidOutPutAmount,
					Message: "Invalid amount",
				}
			}
		} else {
			// Ensure amount is in the valid range for monetary amounts.
			if amount <= 0 || amount > common.MaxXing {
				return nil, &rpcjson.RPCError{
					Code:    rpcjson.ErrRPCInvalidOutPutAmount,
					Message: "Invalid amount",
				}
			}
		}

		// No contract call involved
		if len(data) == 0 {
			// Decode the provided address.
			addr, err := asiutil.DecodeAddress(output.Address)
			if err != nil {
				return nil, &rpcjson.RPCError{
					Code:    rpcjson.ErrRPCInvalidAddressOrKey,
					Message: "Invalid address or key: " + err.Error(),
				}
			}

			// Ensure the address is one of the supported types and that
			// the network encoded with the address matches the network the
			// NodeServer is currently on.
			switch addr.(type) {
			case *common.Address:
			default:
				return nil, &rpcjson.RPCError{
					Code:    rpcjson.ErrRPCInvalidAddressOrKey,
					Message: "Invalid address or key",
				}
			}

			// Create a new script which pays to the provided address.
			pkScript, err = txscript.PayToAddrScript(addr)
			if err != nil {
				context := "Failed to generate pay-to-address script"
				return nil, internalRPCError(err.Error(), context)
			}
		} else {
			// Contract call
			addr, err := hexutil.Decode(output.Address)
			if output.Address != "" || ctype != txscript.CreateTy.String() {
				if err != nil {
					context := "Failed to decode output address"
					return nil, internalRPCError(err.Error(), context)
				}
			}

			pkScript, err = txscript.PayToContractScript(ctype, addr)
			if err != nil {
				context := "Failed to generate pay-to-contract script"
				return nil, internalRPCError(err.Error(), context)
			}

			if ctype == txscript.CreateTy.String() {
				if len(firstInputScriptPubKey) == 0 {
					errStr := "#0 input scriptPubKey is none"
					return nil, internalRPCError(errStr, "")
				}
				_, addresses, _, _ := txscript.ExtractPkScriptAddrs(firstInputScriptPubKey)

				// Generate contract address
				if len(addresses) != 1 {
					errStr := "should only be one caller"
					return nil, internalRPCError(errStr, "")
				}

				caller := addresses[0].StandardAddress()

				category, templateName, _, ok := blockchain.DecodeCreateContractData(data)
				block, stateDB := createTempBlockState(s.cfg)

				_, ok, _ = s.cfg.Chain.GetByteCode(nil, block, common.SystemContractReadOnlyGas,
					stateDB, chaincfg.ActiveNetParams.FvmParam, category, templateName)
				if !ok {
					context := "Incorrect data protocol"
					return nil, internalRPCError("Contract data is invalid.", context)
				}
				inputHash := asiutil.GenInputHash(mtx)
				// cAddress, err := crypto.CreateContractAddress(caller[:], append(byteCode, constructor...), inputHash)
				cAddress, err := crypto.CreateContractAddress(caller[:], []byte{}, inputHash)

				if err != nil {
					context := "Failed to generate contract address"
					return nil, internalRPCError(err.Error(), context)
				}

				contractAddress[uint64(index)] = hexutil.Encode(cAddress.Bytes())
			}
		}

		txOut := protos.NewContractTxOut(amount, pkScript, *protos.AssetFromBytes(assets), data)
		mtx.AddTxOut(txOut)
	}

	if gasLimit == nil {
		mtx.TxContract = protos.TxContract{GasLimit: uint32(21000)}
	} else {
		mtx.TxContract = protos.TxContract{GasLimit: uint32(*gasLimit)}
	}

	// Set the Locktime, if given.
	if lockTime != nil {
		mtx.LockTime = uint32(*lockTime)
	}

	// Return the serialized and hex-encoded transaction.  Note that this
	// is intentionally not directly returning because the first return
	// value is a string and it would result in returning an empty string to
	// the client instead of nothing (nil) in the case of an error.
	mtxHex, err := messageToHex(mtx)
	if err != nil {
		return nil, err
	}

	return &rpcjson.CreateRawTransactionResult{
		Hex:          mtxHex,
		ContractAddr: contractAddress,
	}, nil
}

func (s *PublicRpcAPI) DecodeRawTransaction(hexTx string) (interface{}, error) {
	// Deserialize the transaction.
	if len(hexTx)%2 != 0 {
		hexTx = "0" + hexTx
	}
	serializedTx, err := hex.DecodeString(hexTx)
	if err != nil {
		return nil, rpcDecodeHexError(hexTx)
	}
	var mtx protos.MsgTx
	err = mtx.Deserialize(bytes.NewReader(serializedTx))
	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCDeserialization,
			Message: "TX decode failed: " + err.Error(),
		}
	}

	// Create and return the result.
	txReply := rpcjson.TxRawDecodeResult{
		Txid:     mtx.TxHash().UnprefixString(),
		Version:  mtx.Version,
		Locktime: mtx.LockTime,
		Vin:      createVinList(&mtx),
		Vout:     createVoutList(&mtx, nil),
	}
	return txReply, nil
}

// Calculate a contract address before deploying the contract on chain.
// Note the calculation is part of logic implemented in the `CreateRawTransaction` function.
// As a result, `CreateRawTransaction` is directly called instead of composing the similar code again.
func (s *PublicRpcAPI) CalculateContractAddress(inputs []rpcjson.TransactionInput, outputs []rpcjson.TransactionOutput) (interface{}, error) {
	result, err := s.CreateRawTransaction(inputs, outputs, nil, nil)
	if err != nil {
		return result, err
	} else {
		return result.(*rpcjson.CreateRawTransactionResult).ContractAddr, nil
	}
}

func (s *PublicRpcAPI) DecodeScript(hexScript string) (interface{}, error) {
	// Convert the hex script to bytes.
	if len(hexScript)%2 != 0 {
		hexScript = "0" + hexScript
	}
	script, err := hex.DecodeString(hexScript)
	if err != nil {
		return nil, rpcDecodeHexError(hexScript)
	}

	// The disassembled string will contain [error] inline if the script
	// doesn't fully parse, so ignore the error here.
	disbuf, _ := txscript.DisasmString(script)

	// Get information about the script.
	// Ignore the error here since an error means the script couldn't parse
	// and there is no additional information about it anyways.
	scriptClass, addrs, reqSigs, _ := txscript.ExtractPkScriptAddrs(script)
	addresses := make([]string, len(addrs))
	for i, addr := range addrs {
		addresses[i] = addr.EncodeAddress()
	}

	// Convert the script itself to a pay-to-script-hash address.
	p2sh, err := common.NewAddressWithId(common.ScriptHashAddrID, common.Hash160(script))
	if err != nil {
		context := "Failed to convert script to pay-to-script-hash"
		return nil, internalRPCError(err.Error(), context)
	}

	// Generate and return the reply.
	reply := rpcjson.DecodeScriptResult{
		Asm:       disbuf,
		ReqSigs:   int32(reqSigs),
		Type:      scriptClass.String(),
		Addresses: addresses,
	}
	if scriptClass != txscript.ScriptHashTy {
		reply.P2sh = p2sh.EncodeAddress()
	}
	return reply, nil
}

// Get system contract ABI information on a given block height.
// Note the term system contract and genesis contract are interchangeable in this context.
func (s *PublicRpcAPI) GetGenesisContractByHeight(height int32, contractAddr common.Address) (interface{}, error) {
	var result = struct {
		Exist bool   `json:"exist"`
		ABI   string `json:"abi"`
	}{
		Exist: false,
		ABI:   "",
	}

	contract := s.cfg.ContractMgr.GetActiveContractByHeight(height, contractAddr)
	if contract == nil {
		return result, nil
	}
	result.Exist = true
	result.ABI = contract.AbiInfo
	return result, nil
}

// Get system contract ABI information on latest block height.
func (s *PublicRpcAPI) GetGenesisContract(contractAddr common.Address) (interface{}, error) {
	blockHeight := s.cfg.Chain.BestSnapshot().Height
	contract := s.cfg.ContractMgr.GetActiveContractByHeight(blockHeight, contractAddr)
	if contract == nil {
		return nil, internalRPCError("Can not find contract by this name", "")
	}

	var result = struct {
		Address    string `json:"address"`
		Code       string `json:"code"`
		AbiInfo    string `json:"abiInfo"`
	}{
		Address:    contractAddr.String(),
		Code:       contract.Code,
		AbiInfo:    contract.AbiInfo,
	}

	return result, nil
}

// Get the contract addresses which issued the given assets
func (s *PublicRpcAPI) GetContractAddressesByAssets(assets []string) (interface{}, error) {
	block, stateDB := createTempBlockState(s.cfg)
	var result = make([]string, 0)

	contractAddrs, isSuccess, _ := s.cfg.ContractMgr.GetContractAddressByAsset(common.SystemContractReadOnlyGas,
		block, stateDB, chaincfg.ActiveNetParams.FvmParam, assets)
	if !isSuccess {
		return nil, internalRPCError("Failed to get contract address", "")
	}

	for _, addr := range contractAddrs {
		result = append(result, addr.String())
	}
	return result, nil
}

// Get detail information of given assets.
func (s *PublicRpcAPI) GetAssetInfoList(assets []string) (interface{}, error) {

	block, stateDB := createTempBlockState(s.cfg)

	results, err := s.cfg.ContractMgr.GetAssetInfoByAssetId(
		block, stateDB, chaincfg.ActiveNetParams.FvmParam, assets)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to get asset info")

	}

	return results, nil
}

// Get a list of template information.
// The result contains a page from pageNo to pageNo+pageSize of given category.
func (s *PublicRpcAPI) GetContractTemplateList(approved bool, category uint16, pageNo int, pageSize int) (interface{}, error) {

	block, stateDB := createTempBlockState(s.cfg)

	var getCountFunc string
	var getTempFunc string
	if approved {
		getCountFunc, getTempFunc = common.ContractTemplateWarehouse_GetApprovedTemplatesCountFunction(),
			common.ContractTemplateWarehouse_GetApprovedTemplateFunction()
	} else {
		getCountFunc, getTempFunc = common.ContractTemplateWarehouse_GetSubmittedTemplatesCountFunction(),
			common.ContractTemplateWarehouse_GetSubmittedTemplateFunction()
	}
	totalLength, result, err, leftOvergas := s.cfg.ContractMgr.GetTemplates(block,
		common.SystemContractReadOnlyGas, stateDB, chaincfg.ActiveNetParams.FvmParam,
		getCountFunc, getTempFunc, category, pageNo, pageSize)
	if err != nil {
		return nil, internalRPCError(err.Error(), "GetContractTemplateList leaves")
	}
	rpcsLog.Debug("GetContractTemplateList costs gas ", common.SystemContractReadOnlyGas - leftOvergas)

	return ainterface.Page{totalLength, result}, nil
}

// Get name of the template based on which the given address (contract) is deployed.
func (s *PublicRpcAPI) GetContractTemplateName(contractAddress string) (interface{}, error) {
	result, err := s.GetContractTemplate(contractAddress)

	if err != nil {
		return result, err
	} else {
		return result.(rpcjson.ContractTemplate).TemplateTName, nil
	}
}

// Get information of the template based on which the given address (contract) is deployed.
func (s *PublicRpcAPI) GetContractTemplate(contractAddress string) (interface{}, error) {
	chain := s.cfg.Chain
	addr, err := hexutil.Decode(contractAddress)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode contractAddress")
	}

	if addr[0] != common.ContractHashAddrID {
		return nil, internalRPCError("The input contract address is not valid", "")
	}

	block, stateDB := createTempBlockState(s.cfg)

	templateType, templateName, _ := chain.GetTemplateInfo(addr, common.SystemContractReadOnlyGas,
		block, stateDB, chaincfg.ActiveNetParams.FvmParam)

	return rpcjson.ContractTemplate{
		TemplateTName: templateName,
		TemplateType:  templateType,
	}, nil
}

// Call a readonly function (view, pure in solidity) in a contract and return the execution result of the contract function
func (s *PublicRpcAPI) CallReadOnlyFunction(callerAddress string, contractAddress string, data string, name string, abi string) (interface{}, error) {

	input := common.Hex2Bytes(data)

	block, stateDB := createTempBlockState(s.cfg)

	callerAddressBytes, err := hexutil.Decode(callerAddress)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode callerAddress")
	}
	callerAddr := common.BytesToAddress(callerAddressBytes)

	contractAddrBytes, err := hexutil.Decode(contractAddress)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode contractAddress")
	}
	contractAddr := common.BytesToAddress(contractAddrBytes)

	fmt.Println("************* call function ******************")
	fmt.Println(contractAddr)
	fmt.Println(data)

	ret, _, err := fvm.CallReadOnlyFunction(callerAddr, block, s.cfg.Chain, stateDB, chaincfg.ActiveNetParams.FvmParam, common.SystemContractReadOnlyGas, contractAddr, input)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to call readonly function")
	}
	result, err := fvm.UnPackReadOnlyResult(abi, name, ret)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to unpack readonly function execution result")
	}

	return result, nil
}

// This function is provided to internal IDE tool. IT IS NOT MEANT FOR PUBLIC USE.
// The function provides a way to call a contract function in a state preserving manner.
// A contract function can be called and returns the execution result without affecting the block state.
// In order to prevent the execution to end in OUT OF GAS, the gas set to call the contract function is 1000000000.
func (s *PublicRpcAPI) Call(callerAddress string, contractAddress string, data string, name string, abiStr string, amount int64, asset string) (interface{}, error) {

	res := &rpcjson.CallResult{}
	input := common.Hex2Bytes(data)

	chain := s.cfg.Chain

	block, stateDB := createTempBlockState(s.cfg)

	callerAddressBytes, err := hexutil.Decode(callerAddress)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode callerAddress")
	}
	callerAddr := common.BytesToAddress(callerAddressBytes)

	contractAddrBytes, err := hexutil.Decode(contractAddress)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode contractAddress")
	}
	contractAddr := common.BytesToAddress(contractAddrBytes)

	// Gen Event signature map
	var eventSignatureMap = make(map[string]string)

	// Analyze log using ABI
	definition, err := abi.JSON(strings.NewReader(abiStr))
	events := definition.Events
	for _, event := range events {
		signature := make([]string, 0)
		inputName := make([]string, 0)
		signature = append(signature, event.Name, "(")
		for _, input := range event.Inputs {
			signature = append(signature, input.Type.String(), ",")
			inputName = append(inputName, input.Name)
		}

		if len(event.Inputs) > 0 {
			signature = signature[:len(signature)-1]
		}
		signature = append(signature, ")")
		sum := crypto.Keccak256([]byte(strings.Join(signature, "")))
		hash := common.BytesToHash(sum)
		eventSignatureMap[hash.String()] = event.Name
	}

	fmt.Println("************* call function ******************")
	fmt.Println(name)
	fmt.Println(contractAddr)
	fmt.Println(data)

	context := fvm.NewFVMContext(callerAddr, new(big.Int).SetInt64(1), block, chain, nil, nil)
	vmInstance := vm.NewFVM(context, stateDB, chaincfg.ActiveNetParams.FvmParam, *chain.GetVmConfig())

	var assets *protos.Asset
	if asset != "" {
		assetBytes := common.Hex2Bytes(asset)
		assets = protos.AssetFromBytes(assetBytes)
	}

	ret, leftGas, _, err := vmInstance.Call(vm.AccountRef(callerAddr), contractAddr, input, uint64(1000000000), big.NewInt(amount), assets, true)
	res.GasUsed = uint64(1000000000) - leftGas
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to call contract function")
	}

	if ret != nil {
		result, err := fvm.UnPackReadOnlyResult(abiStr, name, ret)
		if err != nil {
			return nil, internalRPCError(err.Error(), "Failed to unpack function execution result")
		}

		res.Result = result
	}

	logs := stateDB.GetLogs(common.Hash{})
	var logResults = make([]rpcjson.CallLogResult, 0)
	for _, log := range logs {
		signature := log.Topics[0]
		if eventName, ok := eventSignatureMap[signature.String()]; ok {
			result, err := fvm.UnpackEvent(abiStr, eventName, log.Data)

			if err != nil {
				return nil, err
			}

			logResults = append(logResults, rpcjson.CallLogResult{
				EventName: eventName,
				Log:       result,
			})
		}
	}
	res.Logs = logResults

	return res, nil
}

// This function is provided to internal IDE tool. IT IS NOT MEANT FOR PUBLIC USE.
// This function provides a way to execute a series of operations
// (submit template, deploy contract instance, call a contract) in a state preserving manner.
// The operations will be executed in order and without affecting the block state.
// In order to prevent the execution to end in OUT OF GAS, the gas set to call the contract function is 1000000000.
func (s *PublicRpcAPI) Test(callerAddress string, byteCode string, argStr string, callDatas []rpcjson.TestCallData, abiStr string) (interface{}, error) {

	res := &rpcjson.TestResult{}

	chain := s.cfg.Chain

	callerAddressBytes, err := hexutil.Decode(callerAddress)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode callerAddress")
	}
	callerAddr := common.BytesToAddress(callerAddressBytes)

	// Generate Event signature map
	var eventSignatureMap = make(map[string]string)

	// Analyze event using ABI
	definition, err := abi.JSON(strings.NewReader(abiStr))
	events := definition.Events
	for _, event := range events {
		signature := make([]string, 0)
		inputName := make([]string, 0)
		signature = append(signature, event.Name, "(")
		for _, input := range event.Inputs {
			signature = append(signature, input.Type.String(), ",")
			inputName = append(inputName, input.Name)
		}
		if len(event.Inputs) > 0 {
			signature = signature[:len(signature)-1]
		}
		signature = append(signature, ")")
		sum := crypto.Keccak256([]byte(strings.Join(signature, "")))
		hash := common.BytesToHash(sum)
		eventSignatureMap[hash.String()] = event.Name
	}

	byteCodeHash := common.Hex2Bytes(byteCode)
	argHash := make([]byte, 0)
	if argStr != "" {
		argHash = common.Hex2Bytes(argStr)
	}

	for idx, callData := range callDatas {
		idxStr := strconv.FormatInt(int64(idx), 16)

		inputHash := []byte(idxStr)
		caller := vm.AccountRef(callerAddr)
		amount := big.NewInt(0)
		asset := &asiutil.AsimovAsset
		if callData.Caller != "" {
			callerBytes, err := hexutil.Decode(callData.Caller)
			if err != nil {
				return nil, internalRPCError(err.Error(), "Failed to decode caller address in call data")
			}

			caller = vm.AccountRef(common.BytesToAddress(callerBytes))
		}

		if callData.Amount != uint32(0) {
			amount = big.NewInt(int64(callData.Amount))
		}

		if callData.Asset != "" {
			assetBytes := common.Hex2Bytes(callData.Asset)
			asset = protos.AssetFromBytes(assetBytes)
		}

		var voteValueFunc func() int64
		voteValueFunc = func() int64 {
			value, err := strconv.ParseInt(callData.VoteValue, 10, 64)
			if err != nil {
				return 0
			}
			return value
		}

		block, stateDB := createTempBlockState(s.cfg)
		context := fvm.NewFVMContext(callerAddr, new(big.Int).SetInt64(1), block, chain, nil, voteValueFunc)
		vmInstance := vm.NewFVM(context, stateDB, chaincfg.ActiveNetParams.FvmParam, *chain.GetVmConfig())

		_, contractAddr, _, _, err := vmInstance.Create(
			vm.AccountRef(callerAddr), byteCodeHash, uint64(1000000000), big.NewInt(0),
			&asiutil.AsimovAsset, inputHash, argHash, false)

		if err != nil {
			return nil, err
		}
		name := callData.Name
		data := callData.Data

		ret, leftGas, _, err := vmInstance.Call(caller, contractAddr, common.Hex2Bytes(data),
			uint64(1000000000), amount, asset, true)

		callResult := &rpcjson.CallResult{}
		callResult.Name = name
		callResult.GasUsed = uint64(1000000000) - leftGas
		if err != nil {
			callResult.Result = err.Error()
		}
		if ret != nil && err == nil {
			result, err := fvm.UnPackReadOnlyResult(abiStr, name, ret)
			if err != nil {
				return nil, internalRPCError(err.Error(), "")
			}

			callResult.Result = result
		}

		logs := stateDB.Logs()

		var logResults = make([]rpcjson.CallLogResult, 0)
		for _, log := range logs {

			signature := log.Topics[0]

			fmt.Println(log.Data)

			if eventName, ok := eventSignatureMap[signature.String()]; ok {
				result, err := fvm.UnpackEvent(abiStr, eventName, log.Data)
				if err != nil {
					return nil, internalRPCError(err.Error(), "")
				}

				logResults = append(logResults, rpcjson.CallLogResult{
					EventName: eventName,
					Log:       result,
				})
			}

		}
		callResult.Logs = logResults

		res.List = append(res.List, *callResult)
	}

	return res, nil
}

// create a template block and a stateDB.
func createTempBlockState(cfg   *rpcserverConfig) (*asiutil.Block, *state.StateDB) {
	tipNode := cfg.Chain.GetTip()
	header := protos.BlockHeader{
		PrevBlock:tipNode.Hash(),
		SlotIndex:tipNode.Slot() + 1,
		Round:    tipNode.Round(),
		Height:   tipNode.Height() + 1,
	}
	if tipNode.Slot() == cfg.ChainParams.RoundSize {
		header.SlotIndex = 0
		header.Round ++
	}
	block := asiutil.NewBlock(&protos.MsgBlock{Header:header})

	stateDB, err := state.New(tipNode.StateRoot(), cfg.Chain.GetStateCache())
	if err != nil {
		return nil, nil
	}
	return block, stateDB
}

// This function is provided only for INTERNAL USE.
// By running this function, the caller can estimate gas cost of a specific contract call.
// Note the estimated gas cost is augmented by 120% in order to prevent OUT OF GAS error in real execution.
func (s *PublicRpcAPI) EstimateGas(caller string, contractAddress string, amount int64, asset string, data string,
	callType string, voteValue int64) (interface{}, error) {

	chain := s.cfg.Chain

	callerAddressBytes, err := hexutil.Decode(caller)
	if err != nil {
		return 0, internalRPCError(err.Error(), "Failed to decode callerAddress")
	}

	contractAddressBytes, err := hexutil.Decode(contractAddress)
	if err != nil {
		return 0, internalRPCError(err.Error(), "Failed to decode contractAddress")
	}

	assetBytes := common.Hex2Bytes(asset)

	assets := protos.AssetFromBytes(assetBytes)

	callerAddr := common.BytesToAddress(callerAddressBytes)

	dataBytes := common.Hex2Bytes(data)
	var voteValueFunc func() int64
	if callType == "vote" {
		voteValueFunc = func() int64 {
			return voteValue
		}
	}
	block, stateDB := createTempBlockState(s.cfg)

	context := fvm.NewFVMContext(callerAddr, new(big.Int).SetInt64(1), block, chain, nil, voteValueFunc)
	vmInstance := vm.NewFVM(context, stateDB, chaincfg.ActiveNetParams.FvmParam, *chain.GetVmConfig())
	leftOverGas := uint64(1000000000)

	if callType == "create" {
		inputHash := []byte("estimateGas")
		category, templateName, constructor, ok := blockchain.DecodeCreateContractData(dataBytes)
		if !ok {
			return 0, internalRPCError("", "Failed to decode contract data")
		}
		var byteCode []byte
		var addr common.Address

		byteCode, ok, leftOverGas = chain.GetByteCode(nil, block, leftOverGas, stateDB, chaincfg.ActiveNetParams.FvmParam, category, templateName)
		if !ok {
			return 0, internalRPCError("", "Failed to get contract template data")
		}

		_, addr, leftOverGas, _, err = vmInstance.Create(vm.AccountRef(callerAddr), byteCode, leftOverGas, big.NewInt(amount), assets, inputHash, constructor, false)
		if err != nil {
			return 0, internalRPCError(err.Error(), "Failed to create contract")
		}

		err, leftOverGas = chain.InitTemplate(category, templateName, addr, leftOverGas, assets, vmInstance)
		if err != nil {
			return 0, internalRPCError(err.Error(), "Failed to init template")
		}
	} else if callType == "template" {

		category, templateName, _, _, _, _ := blockchain.DecodeTemplateContractData(dataBytes)

		createTemplateAddr, _, createTemplateABI := chain.GetSystemContractInfo(common.TemplateWarehouse)
		createFunc := common.ContractTemplateWarehouse_CreateFunction()
		txHash := []byte("estimateGas")

		runCode, err := fvm.PackFunctionArgs(createTemplateABI, createFunc, category, string(templateName), common.BytesToHash(txHash))
		if err != nil {
			return 0, internalRPCError(err.Error(), "Failed to call PackFunctionArgs")
		}

		_, leftOverGas, _, err = vmInstance.Call(vm.AccountRef(callerAddr), createTemplateAddr, runCode, leftOverGas, big.NewInt(amount), assets, true)
		if err != nil {
			return 0, internalRPCError(err.Error(), "Failed to create contract template")
		}
	} else {

		_, leftOverGas, _, err = vmInstance.Call(vm.AccountRef(callerAddr), common.BytesToAddress(contractAddressBytes), dataBytes, leftOverGas, big.NewInt(amount), assets, false)
		if err != nil {
			return 0, internalRPCError(err.Error(), "Failed to call contract")
		}
	}

	leftOverGas, err = chain.CheckTransferValidate(stateDB, vmInstance.Vtx, block, leftOverGas)
	if err != nil {
		return 0, internalRPCError(err.Error(), "Failed to validate transfer")
	}

	result := uint64(1000000000) - leftOverGas
	result = uint64(math.Ceil(float64(result + uint64(2300)) * float64(1.2)))
	fmt.Println("gaslimit EstimateGas ", result)

	return result, nil
}

// This function is provided only for INTERNAL USE.
// This function provides a way to run a transaction in a state preserving manner.
// Which is especially useful to estimate gas cost at blockchain level instead of VM level (compared to `EstimateGas`)
func (s *PublicRpcAPI) RunTransaction(hexTx string, utxos []*rpcjson.ListUnspentResult) (interface{}, error) {
	bytesTx, err := hex.DecodeString(hexTx)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to decode hex tx")
	}

	tx := &protos.MsgTx{}
	reader := bytes.NewReader(bytesTx)
	err = tx.Deserialize(reader)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to deserialize tx")
	}

	if tx.TxContract.GasLimit == 0 {
		tx.TxContract.GasLimit = 100000000
	}

	view := txo.NewUtxoViewpoint()
	if utxos != nil {
		var lockItem *txo.LockItem
		for _, utxo := range utxos {
			outpoint := protos.OutPoint{
				Hash:  common.HexToHash(utxo.TxID),
				Index: utxo.Vout,
			}
			pkScript, err := hex.DecodeString(utxo.ScriptPubKey)
			if err != nil {
				return nil, internalRPCError(err.Error(), "Failed to decode pkScript")
			}
			bytesAssets, err := hex.DecodeString(utxo.Assets)
			if err != nil {
				return nil, internalRPCError(err.Error(), "Failed to decode asset")
			}
			assets := protos.AssetFromBytes(bytesAssets)
			if len(utxo.ListLockEntry) > 0 {
				lockItem = txo.NewLockItem()
				for _, item := range utxo.ListLockEntry {
					bytesVoteId, err := hex.DecodeString(item.Id)
					if err != nil {
						return nil, internalRPCError(err.Error(), "Failed to decode vote id")
					}
					bytesVoteId = common.LeftPadBytes(bytesVoteId, 32)
					lockEntry := &txo.LockEntry{
						Amount: item.Amount,
					}
					copy(lockEntry.Id[:], bytesVoteId[:common.HashLength])
					lockItem.PutEntry(lockEntry)
				}
			} else {
				lockItem = nil
			}

			entry := txo.NewUtxoEntry(utxo.Amount, pkScript, utxo.Height, false, assets, lockItem)
			view.AddEntry(outpoint, entry)
		}
	}

	block, stateDB := createTempBlockState(s.cfg)
	block.MsgBlock().AddTransaction(tx)

	fee, _, _ := blockchain.CheckTransactionInputs(asiutil.NewTx(tx), s.cfg.Chain.BestSnapshot().Height, view, s.cfg.Chain)
	receipt, err, gasUsed, vtx, _ := s.cfg.Chain.ConnectTransaction(
		block, 0, view, asiutil.NewTx(tx),
		nil, stateDB, fee)

	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to connect transaction when estimating gas")
	}
	result := &rpcjson.RunTxResult{
		Receipt: receipt,
		GasUsed: gasUsed,
	}
	if vtx != nil {
		result.VTX = &rpcjson.TxRawDecodeResult{}
		result.VTX.Vin = make([]rpcjson.Vin, len(vtx.TxIn))
		result.VTX.Vout = make([]rpcjson.Vout, len(vtx.TxOut))
		for i, in := range vtx.TxIn {
			vin := &result.VTX.Vin[i]
			vin.Txid = in.PreviousOutPoint.Hash.String()
			vin.Vout = in.PreviousOutPoint.Index
		}
		for i, out := range vtx.TxOut {
			vout := &result.VTX.Vout[i]
			vout.Value = out.Value
			vout.ScriptPubKey.Hex = hex.EncodeToString(out.PkScript)
			vout.Asset = hex.EncodeToString(out.Asset.Bytes())
		}
	}

	return result, nil

}

func (s *PublicRpcAPI) GetRawTransaction(txId string, verbose bool, vinExtra bool) (interface{}, error) {
	// Convert the provided transaction hash hex to a Hash.
	txHash := common.HexToHash(txId)

	// Try to fetch the transaction from the memory pool and if that fails,
	// try the block database.
	var mtx *protos.MsgTx
	var blkHash *common.Hash
	var blkHeight int32
	isVtx := false
	tx, _, err := s.cfg.TxMemPool.FetchTransaction(&txHash)
	if err != nil {
		if s.cfg.TxIndex == nil {
			return nil, &rpcjson.RPCError{
				Code: rpcjson.ErrRPCNoTxInfo,
				Message: "The transaction index must be " +
					"enabled to query the blockchain " +
					"(specify --txindex)",
			}
		}

		// Look up the location of the transaction.
		blockRegion, err := s.cfg.TxIndex.FetchBlockRegion(txHash[:])
		if err != nil {
			context := "Failed to retrieve transaction location"
			return nil, internalRPCError(err.Error(), context)
		}
		if blockRegion == nil {
			return nil, &rpcjson.RPCError{
				Code: rpcjson.ErrRPCNoTxInfo,
				Message: fmt.Sprintf("Failed to fetch blockRegion by txHash: %v, "+
					"the blockRegion is nil", &txHash),
			}
		}

		// Normal transaction
		// Load the raw transaction bytes from the database.
		var txBytes []byte
		err = s.cfg.DB.View(func(dbTx database.Tx) error {
			var err error
			txBytes, err = dbTx.FetchBlockRegion(blockRegion)
			return err
		})
		if err != nil {
			context := "Failed to fetch txBytes by blockRegion"
			return nil, internalRPCError(err.Error(), context)
		}
		//// The verbose flag is set, so generate the JSON object and return it.

		// When the verbose flag isn't set, simply return the serialized
		// transaction as a hex-encoded string.  This is done here to
		// avoid deserializing it only to reserialize it again later.
		if !verbose {
			return hex.EncodeToString(txBytes), nil
		}

		// Grab the block height.
		blkHash = &common.Hash{}
		copy(blkHash[:], blockRegion.Key[:common.HashLength])
		blkHeight, err = s.cfg.Chain.BlockHeightByHash(blkHash)
		if err != nil {
			context := "Failed to retrieve block height"
			return nil, internalRPCError(err.Error(), context)
		}

		// Deserialize the transaction
		var msgTx protos.MsgTx
		err = msgTx.Deserialize(bytes.NewReader(txBytes))
		if err != nil {
			context := "Failed to deserialize transaction"
			return nil, internalRPCError(err.Error(), context)
		}
		isVtx = blockRegion.Key.IsVirtual()
		mtx = &msgTx
	} else {
		// When the verbose flag isn't set, simply return the
		// network-serialized transaction as a hex-encoded string.
		if !verbose {
			// Note that this is intentionally not directly
			// returning because the first return value is a
			// string and it would result in returning an empty
			// string to the client instead of nothing (nil) in the
			// case of an error.
			mtxHex, err := messageToHex(tx.MsgTx())
			if err != nil {
				return nil, err
			}
			return mtxHex, nil
		}

		mtx = tx.MsgTx()
	}

	// The verbose flag is set, so generate the JSON object and return it.
	var blkHeader *protos.BlockHeader
	var blkHashStr string
	var chainHeight int32
	if blkHash != nil {
		// Fetch the header from chain.
		header, err := s.cfg.Chain.FetchHeader(blkHash)
		if err != nil {
			context := "Failed to fetch block header"
			return nil, internalRPCError(err.Error(), context)
		}

		blkHeader = &header
		blkHashStr = blkHash.UnprefixString()
		chainHeight = s.cfg.Chain.BestSnapshot().Height
	}

	if !vinExtra {
		rawTxn, err := createTxRawResult(mtx, txHash.UnprefixString(),
			blkHeader, blkHashStr, blkHeight, chainHeight)
		if err != nil {
			return nil, err
		}
		return *rawTxn, nil
	} else {
		rawTxn, err := createTxResult(*s.cfg, mtx, txHash.UnprefixString(),
			blkHeader, blkHashStr, blkHeight, chainHeight, isVtx)
		if err != nil {
			return nil, err
		}
		return *rawTxn, nil
	}
}

// Get virtual transactions of a block in detail
// In Asimov, all asset operations occur in VM is converted to Virtual transactions and send back to blockchain
// Virtual transaction is one of key designs in Asimov blockchain, for more knowledge please refer to the technical white paper
func (s *PublicRpcAPI) GetVirtualTransactions(blkHashStr string, verbose bool, vinExtra bool) (interface{}, error) {
	// Load the raw block bytes from the database.
	hash := common.HexToHash(blkHashStr)

	blkHeight, err := s.cfg.Chain.BlockHeightByHash(&hash)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to retrieve block height")
	}
	var blkBytes []byte
	err = s.cfg.DB.View(func(dbTx database.Tx) error {
		var err error
		blkBytes, err = dbTx.FetchBlock(database.NewVirtualBlockKey(&hash))
		return err
	})
	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCBlockNotFound,
			Message: "Block not found",
		}
	}

	// When the verbose flag isn't set, simply return the serialized block
	// as a hex-encoded string.
	if !verbose {
		return hex.EncodeToString(blkBytes), nil
	}

	// Deserialize the block.
	vBlock, err := asiutil.NewVBlockFromBytes(blkBytes, &hash)
	if err != nil {
		context := "Failed to deserialize block"
		return nil, internalRPCError(err.Error(), context)
	}

	best := s.cfg.Chain.BestSnapshot()
	result := make([]interface{}, 0)

	for _, vtx := range vBlock.MsgVBlock().VTransactions {

		var rawTxn interface{}
		var err error

		if !vinExtra {
			rawTxn, err = createTxRawResult(vtx, vtx.TxHash().UnprefixString(),
				nil, blkHashStr, blkHeight, best.Height)
		} else {
			rawTxn, err = createTxResult(*s.cfg, vtx, vtx.TxHash().UnprefixString(),
				nil, blkHashStr, blkHeight, best.Height, true)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, rawTxn)
		if err != nil {
			return nil, err
		}

	}

	return result, nil

}

func (s *PublicRpcAPI) GetTransactionReceipt(txId string) (interface{}, error) {
	tx, err := s.GetRawTransaction(txId, true, false)
	if err != nil {
		return nil, err
	}

	txRawResult := tx.(rpcjson.TxRawResult)
	if txRawResult.BlockHash == "" {
		return nil, nil
	}

	// Fetch block hash
	blockHash := common.HexToHash(txRawResult.BlockHash)

	block, err := s.cfg.Chain.BlockByHash(&blockHash)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to get block by hash")
	}

	receipts := rawdb.ReadReceipts(s.cfg.Chain.EthDB(), common.Hash(*block.Hash()), uint64(block.Height()))

	txHash := common.HexToHash(txId)
	for _, receipt := range receipts {
		if common.Hash(txHash) == receipt.TxHash {
			return receipt, nil
		}
	}

	return nil, internalRPCError("No receipt found", "Failed to GetTransactionReceipt")
}

func (s *PublicRpcAPI) SendRawTransaction(hexTx string) (interface{}, error) {
	// Deserialize and send off to tx relay
	hexStr := hexTx
	if len(hexStr)%2 != 0 {
		hexStr = "0" + hexStr
	}
	serializedTx, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, rpcDecodeHexError(hexStr)
	}

	var msgTx protos.MsgTx
	err = msgTx.Deserialize(bytes.NewReader(serializedTx))
	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCDeserialization,
			Message: "TX decode failed: " + err.Error(),
		}
	}

	// Use 0 for the tag to represent local node.
	tx := asiutil.NewTx(&msgTx)
	acceptedTxs, err := s.cfg.TxMemPool.ProcessTransaction(tx, false, false, 0)
	if err != nil {
		// When the error is a rule error, it means the transaction was
		// simply rejected as opposed to something actually going wrong,
		// so logger it as such.  Otherwise, something really did go wrong,
		// so logger it as an actual error.  In both cases, a JSON-RPC
		// error is returned to the client with the deserialization
		// error code (to match bitcoind behavior).
		if _, ok := err.(mempool.RuleError); ok {
			rpcsLog.Debugf("Rejected transaction %v: %v", tx.Hash(),
				err)
		} else {
			rpcsLog.Errorf("Failed to process transaction %v: %v",
				tx.Hash(), err)
		}
		return nil, internalRPCError(err.Error(), "Failed to ProcessTransaction")
	}

	// When the transaction was accepted it should be the first item in the
	// returned array of accepted transactions.  The only way this will not
	// be true is if the API for ProcessTransaction changes and this code is
	// not properly updated, but ensure the condition holds as a safeguard.
	//
	// Also, since an error is being returned to the caller, ensure the
	// transaction is removed from the memory pool.
	if len(acceptedTxs) == 0 || !acceptedTxs[0].Tx.Hash().IsEqual(tx.Hash()) {
		s.cfg.TxMemPool.RemoveTransaction(tx, true)

		errStr := fmt.Sprintf("transaction %v is not in accepted list",
			tx.Hash())
		return nil, internalRPCError(errStr, "")
	}

	// Generate and relay inventory vectors for all newly accepted
	// transactions into the memory pool due to the original being
	// accepted.
	s.cfg.ConnMgr.RelayTransactions(acceptedTxs)

	// Notify both websocket and getblocktemplate long poll clients of all
	// newly accepted transactions.
	// s.NotifyNewTransactions(acceptedTxs)

	// Keep track of all the sendrawtransaction request txns so that they
	// can be rebroadcast if they don't make their way into a block.
	txD := acceptedTxs[0]
	iv := protos.NewInvVect(protos.InvTypeTx, txD.Tx.Hash())
	s.cfg.ConnMgr.AddRebroadcastInventory(iv, txD)

	return tx.Hash().String(), nil
}

// 	Address     string
// 	Verbose     *int  `jsonrpcdefault:"1"`
// 	Skip        *int  `jsonrpcdefault:"0"`
// 	Count       *int  `jsonrpcdefault:"100"`
// 	VinExtra    *int  `jsonrpcdefault:"0"`
// 	Reverse     *bool `jsonrpcdefault:"false"`
// 	FilterAddrs *[]string
func (s *PublicRpcAPI) SearchRawTransactions(address string, verbose bool, skip int, count int, vinExtra bool, reverse bool, filterAddress []string) (interface{}, error) {
	// Respond with an error if the address index is not enabled.
	addrIndex := s.cfg.AddrIndex
	if addrIndex == nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCMisc,
			Message: "Address index must be enabled (--addrindex)",
		}
	}

	// Including the extra previous output information requires the
	// transaction index.  Currently the address index relies on the
	// transaction index, so this check is redundant, but it's better to be
	// safe in case the address index is ever changed to not rely on it.
	if vinExtra && s.cfg.TxIndex == nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCMisc,
			Message: "Transaction index must be enabled (--txindex)",
		}
	}

	// Attempt to decode the supplied address.
	addr, err := asiutil.DecodeAddress(address)
	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCInvalidAddressOrKey,
			Message: "Invalid address or key: " + err.Error(),
		}
	}

	// Override the default number of requested entries if needed.  Also,
	// just return now if the number of requested entries is zero to avoid
	// extra work.
	numRequested := 100
	if count != 0 {
		numRequested = count
		if numRequested < 0 {
			numRequested = 1
		}
	}
	if numRequested == 0 {
		return nil, nil
	}

	// Override the default number of entries to skip if needed.
	var numToSkip int
	if skip != 0 {
		numToSkip = skip
		if numToSkip < 0 {
			numToSkip = 0
		}
	}

	// Add transactions from mempool first if client asked for reverse
	// order.  Otherwise, they will be added last (as needed depending on
	// the requested counts).
	//
	// NOTE: This code doesn't sort by dependency.  This might be something
	// to do in the future for the client's convenience, or leave it to the
	// client.
	numSkipped := uint32(0)
	addressTxns := make([]retrievedTx, 0, numRequested)
	if reverse {
		// Transactions in the mempool are not in a block header yet,
		// so the block header field in the retieved transaction struct
		// is left nil.
		mpTxns, mpSkipped := fetchMempoolTxnsForAddress(s.cfg, addr,
			uint32(numToSkip), uint32(numRequested))
		numSkipped += mpSkipped
		for _, tx := range mpTxns {
			addressTxns = append(addressTxns, retrievedTx{tx: tx})
		}
	}

	// Fetch transactions from the database in the desired order if more are
	// needed.
	if len(addressTxns) < numRequested {
		err = s.cfg.DB.View(func(dbTx database.Tx) error {
			regions, dbSkipped, err := addrIndex.TxRegionsForAddress(
				dbTx, addr, uint32(numToSkip)-numSkipped,
				uint32(numRequested-len(addressTxns)), reverse)
			if err != nil {
				return err
			}

			// Load the raw transaction bytes from the database.
			serializedTxns, err := dbTx.FetchBlockRegions(regions)
			if err != nil {
				return err
			}

			// Add the transaction and the hash of the block it is
			// contained in to the list.  Note that the transaction
			// is left serialized here since the caller might have
			// requested non-verbose output and hence there would be
			// no point in deserializing it just to reserialize it
			// later.
			hashes := make([]common.Hash, len(serializedTxns))
			for i, serializedTx := range serializedTxns {
				copy(hashes[i][:], regions[i].Key[:common.HashLength])
				addressTxns = append(addressTxns, retrievedTx{
					txBytes: serializedTx,
					blkHash: &hashes[i],
					blkType: regions[i].Key[common.HashLength],
				})
			}
			numSkipped += dbSkipped

			return nil
		})
		if err != nil {
			context := "Failed to load address index entries"
			return nil, internalRPCError(err.Error(), context)
		}

	}

	// Add transactions from mempool last if client did not request reverse
	// order and the number of results is still under the number requested.
	if !reverse && len(addressTxns) < numRequested {
		// Transactions in the mempool are not in a block header yet,
		// so the block header field in the retrieved transaction struct
		// is left nil.
		mpTxns, mpSkipped := fetchMempoolTxnsForAddress(s.cfg, addr,
			uint32(numToSkip)-numSkipped, uint32(numRequested-
				len(addressTxns)))
		numSkipped += mpSkipped
		for _, tx := range mpTxns {
			addressTxns = append(addressTxns, retrievedTx{tx: tx})
		}
	}

	// Address has never been used if neither source yielded any results.
	if len(addressTxns) == 0 {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCNoTxInfo,
			Message: "No information available about address",
		}
	}

	// Serialize all of the transactions to hex.
	hexTxns := make([]string, len(addressTxns))
	for i := range addressTxns {
		// Simply encode the raw bytes to hex when the retrieved
		// transaction is already in serialized form.
		rtx := &addressTxns[i]
		if rtx.txBytes != nil {
			hexTxns[i] = hex.EncodeToString(rtx.txBytes)
			continue
		}

		// Serialize the transaction first and convert to hex when the
		// retrieved transaction is the deserialized structure.
		hexTxns[i], err = messageToHex(rtx.tx.MsgTx())
		if err != nil {
			return nil, err
		}
	}

	// When not in verbose mode, simply return a list of serialized txns.
	if !verbose {
		return hexTxns, nil
	}

	// Normalize the provided filter addresses (if any) to ensure there are
	// no duplicates.
	filterAddrMap := make(map[string]struct{})
	if filterAddress != nil && len(filterAddress) > 0 {
		for _, addr := range filterAddress {
			filterAddrMap[addr] = struct{}{}
		}
	}

	// The verbose flag is set, so generate the JSON object and return it.
	best := s.cfg.Chain.BestSnapshot()
	srtList := make([]rpcjson.SearchRawTransactionsResult, len(addressTxns))
	for i := range addressTxns {
		// The deserialized transaction is needed, so deserialize the
		// retrieved transaction if it's in serialized form (which will
		// be the case when it was lookup up from the database).
		// Otherwise, use the existing deserialized transaction.
		rtx := &addressTxns[i]
		var mtx *protos.MsgTx
		if rtx.tx == nil {
			// Deserialize the transaction.
			mtx = new(protos.MsgTx)
			err := mtx.Deserialize(bytes.NewReader(rtx.txBytes))
			if err != nil {
				context := "Failed to deserialize transaction"
				return nil, internalRPCError(err.Error(),
					context)
			}
		} else {
			mtx = rtx.tx.MsgTx()
		}

		result := &srtList[i]
		result.Hex = hexTxns[i]
		result.Txid = mtx.TxHash().UnprefixString()
		result.Vin, _, err = createVinListPrevOut(*s.cfg, mtx, vinExtra,
			filterAddrMap)
		if err != nil {
			return nil, err
		}
		result.Vout = createVoutList(mtx, filterAddrMap)
		result.Version = mtx.Version
		result.LockTime = mtx.LockTime

		// Transactions grabbed from the mempool aren't yet in a block,
		// so conditionally fetch block details here.  This will be
		// reflected in the final JSON output (mempool won't have
		// confirmations or block information).
		var blkHeader *protos.BlockHeader
		var blkHashStr string
		var blkHeight int32
		if blkHash := rtx.blkHash; blkHash != nil {
			// Fetch the header from chain.
			header, err := s.cfg.Chain.FetchHeader(blkHash)
			if err != nil {
				return nil, &rpcjson.RPCError{
					Code:    rpcjson.ErrRPCBlockHeaderNotFound,
					Message: "Failed to obtain block header",
				}
			}

			// Get the block height from chain.
			height, err := s.cfg.Chain.BlockHeightByHash(blkHash)
			if err != nil {
				context := "Failed to obtain block height"
				return nil, internalRPCError(err.Error(), context)
			}

			blkHeader = &header
			blkHashStr = blkHash.UnprefixString()
			blkHeight = height
		}

		// Add the block information to the result if there is any.
		if blkHeader != nil {
			// This is not a typo, they are identical in Bitcoin
			// Core as well.
			result.Time = blkHeader.Timestamp
			result.Blocktime = blkHeader.Timestamp
			result.BlockHash = blkHashStr
			result.Confirmations = int64(1 + best.Height - blkHeight)
			result.GasLimit = int64(blkHeader.GasLimit)
			result.GasUsed = int64(blkHeader.GasUsed)
		}
	}

	return srtList, nil
}

func (s *PublicRpcAPI) GetTransactionsByAddresses(addresses []string, numToSkip uint32, numRequested uint32) (interface{}, error) {
	// Respond with an error if the address index is not enabled.
	addrIndex := s.cfg.AddrIndex
	res := make(map[string][]rpcjson.TxResult)
	if addrIndex == nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCMisc,
			Message: "Address index must be enabled (--addrindex)",
		}
	}

	// Attempt to decode the supplied address.
	for _, address := range addresses {

		addr, err := asiutil.DecodeAddress(address)
		if err != nil {
			return nil, &rpcjson.RPCError{
				Code:    rpcjson.ErrRPCInvalidAddressOrKey,
				Message: "Invalid address or key: " + err.Error(),
			}
		}
		addressTxns := make([]retrievedTx, 0)

		err = s.cfg.DB.View(func(dbTx database.Tx) error {

			regions, _, err := addrIndex.TxRegionsForAddress(
				dbTx, addr, numToSkip, numRequested, true)

			if err != nil {
				return err
			}

			// Load the raw transaction bytes from the database.
			serializedTxns, err := dbTx.FetchBlockRegions(regions)
			if err != nil {
				return err
			}

			// Add the transaction and the hash of the block it is
			// contained in to the list.  Note that the transaction
			// is left serialized here since the caller might have
			// requested non-verbose output and hence there would be
			// no point in deserializing it just to reserialize it
			// later.
			hashes := make([]common.Hash, len(serializedTxns))
			for i, serializedTx := range serializedTxns {
				copy(hashes[i][:], regions[i].Key[:common.HashLength])
				addressTxns = append(addressTxns, retrievedTx{
					txBytes: serializedTx,
					blkHash: &hashes[i],
					blkType: regions[i].Key[common.HashLength],
				})
			}

			return nil
		})
		if err != nil {
			context := "Failed to load address index entries"
			return nil, internalRPCError(err.Error(), context)
		}

		hexTxns := make([]string, len(addressTxns))
		for i := range addressTxns {
			// Simply encode the raw bytes to hex when the retrieved
			// transaction is already in serialized form.
			rtx := &addressTxns[i]
			if rtx.txBytes != nil {
				hexTxns[i] = hex.EncodeToString(rtx.txBytes)
				continue
			}

			// Serialize the transaction first and convert to hex when the
			// retrieved transaction is the deserialized structure.
			hexTxns[i], err = messageToHex(rtx.tx.MsgTx())
			if err != nil {
				return nil, err
			}
		}

		best := s.cfg.Chain.BestSnapshot()
		srtList := make([]rpcjson.TxResult, len(addressTxns))

		for i := range addressTxns {

			rtx := &addressTxns[i]

			var mtx *protos.MsgTx
			if rtx.tx == nil {
				// Deserialize the transaction.
				mtx = new(protos.MsgTx)
				err := mtx.Deserialize(bytes.NewReader(rtx.txBytes))
				if err != nil {
					context := "Failed to deserialize transaction"
					return nil, internalRPCError(err.Error(),
						context)
				}
			} else {
				mtx = rtx.tx.MsgTx()
			}

			var blkHeader *protos.BlockHeader
			var blkHashStr string
			var blkHeight int32
			if blkHash := rtx.blkHash; blkHash != nil {
				// Fetch the header from chain.
				header, err := s.cfg.Chain.FetchHeader(blkHash)
				if err != nil {
					return nil, &rpcjson.RPCError{
						Code:    rpcjson.ErrRPCBlockHeaderNotFound,
						Message: "Failed to obtain block header",
					}
				}

				// Get the block height from chain.
				height, err := s.cfg.Chain.BlockHeightByHash(blkHash)
				if err != nil {
					context := "Failed to obtain block height"
					return nil, internalRPCError(err.Error(), context)
				}

				blkHeader = &header
				blkHashStr = blkHash.UnprefixString()
				blkHeight = height
			}

			result, err := createTxResult(*s.cfg, mtx,
				mtx.TxHash().String(), blkHeader, blkHashStr,
				blkHeight, best.Height, rtx.blkType == byte(database.BlockVirtual))
			if err != nil {
				return nil, err
			}

			srtList[i] = *result
		}
		res[address] = srtList

	}

	return res, nil
}

func (s *PublicRpcAPI) GetMempoolTransactions(txIds []string) (interface{}, error) {
	if len(txIds) != 0 {
		result := make(map[string]*protos.MsgTx, 0)

		for _, txId := range txIds {
			hash := common.HexToHash(txId)
			tx, err := s.cfg.TxMemPool.FetchAnyTransaction(&hash)
			if err != nil || tx == nil {
				continue
			}
			result[txId] = tx.MsgTx()
		}
		return result, nil
	}

	descs := s.cfg.TxMemPool.TxDescs()
	result := make([]interface{}, len(descs))
	for i := range descs {
		result[i] = descs[i].Tx.MsgTx()
	}
	return result, nil
}

func (s *PublicRpcAPI) AddNode(_addr string, _subCmd rpcjson.AddNodeSubCmd) (interface{}, error) {
	addr := fnet.NormalizeAddress(_addr, s.cfg.ChainParams.DefaultPort)
	var err error
	switch _subCmd {
	case "add":
		err = s.cfg.ConnMgr.Connect(addr, true)
	case "remove":
		err = s.cfg.ConnMgr.RemoveByAddr(addr)
	case "onetry":
		err = s.cfg.ConnMgr.Connect(addr, false)
	default:
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCInvalidParameter,
			Message: "invalid subcommand for addnode",
		}
	}

	if err != nil {
		return nil, &rpcjson.RPCError{
			Code:    rpcjson.ErrRPCInvalidParameter,
			Message: err.Error(),
		}
	}

	// no data returned unless an error.
	return nil, nil
}

// Get the list of assets which can be used as transaction fees on Asimov blockchain
// By default, only Asim can be used as transaction fee.
// The validator committee can choose to add new asset to the list as needed.
func (s *PublicRpcAPI) GetFeeList() (interface{}, error) {

	block, stateDB := createTempBlockState(s.cfg)

	fees, err := s.cfg.ContractMgr.GetFees(block,
		stateDB, chaincfg.ActiveNetParams.FvmParam)
	if err != nil {
		return nil, internalRPCError(err.Error(), "Failed to get fees")
	}

	feeList := make([]rpcjson.FeeItemResult, 0, len(fees)+1)
	for assets, height := range fees {
		feeList = append(feeList, rpcjson.FeeItemResult{Assets: hex.EncodeToString(assets.Bytes()), Height: height})
	}
	feeList = append(feeList, rpcjson.FeeItemResult{Assets:hex.EncodeToString(asiutil.AsimovAsset.Bytes()), Height:0})
	return feeList, nil
}

// Get template information by category and template name
func (s *PublicRpcAPI) GetContractTemplateInfoByName(category uint16, templateName string) (interface{}, error) {

	block, stateDB := createTempBlockState(s.cfg)

	templateContent, ok, _ := s.cfg.ContractMgr.GetTemplate(block,
		common.SystemContractReadOnlyGas,
		stateDB, chaincfg.ActiveNetParams.FvmParam, category, templateName)
	if !ok {
		return nil, internalRPCError("error:template not found", "")
	}
	result, ok := getTemplateInfoByKey(templateContent.Key, s)
	if !ok {
		return nil, internalRPCError("error:template not found", "")
	}
	return result, nil
}

// Get template information by key
// The key is the the transaction id in which the template is submitted
func (s *PublicRpcAPI) GetContractTemplateInfoByKey(key string) (interface{}, error) {
	result, ok := getTemplateInfoByKey(key, s)
	if !ok {
		return nil, internalRPCError("error:template not found", "")
	}
	return result, nil
}

func (s *PublicRpcAPI) GetContractExecuteError(txid string)(interface{},error){
	data,err:=cache.GetExecuteError(txid)
	if err!=nil{
		return nil,internalRPCError(err.Error(),
			"Failed to get contract execute error")
	}
	return data,nil
}

func getTemplateInfoByKey(key string, s *PublicRpcAPI) (interface{}, bool) {
	keyHash := common.HexToHash(key)
	category, templateName, byteCode, abi, source, err := s.cfg.Chain.FetchTemplate(nil, &keyHash)
	if err != nil {
		return nil, false
	}

	result := rpcjson.ContractTemplateDetail{
		Category:     category,
		TemplateName: string(templateName),
		ByteCode:     common.Bytes2Hex(byteCode),
		Abi:          string(abi),
		Source:       string(source),
	}

	return result, true
}

type AsimovRpcService struct {
	stack  *node.Node
	config *rpcserverConfig
}

func NewAsimovRpcService(ctx *node.ServiceContext, stack *node.Node, config *rpcserverConfig) (*AsimovRpcService, error) {
	eth := &AsimovRpcService{
		stack:  stack,
		config: config,
	}
	return eth, nil
}

// APIs return the collection of RPC services the asimov package offers.
// NOTE, some of these services probably need to be moved to somewhere else.
func (s *AsimovRpcService) APIs() []rpc.API {
	return []rpc.API{
		{
			Namespace: "asimov",
			Version:   "1.0",
			Service:   NewPublicRpcAPI(s.stack, s.config),
			Public:    true,
		},
	}
}

func (s *AsimovRpcService) Start() error {
	return nil
}

func (s *AsimovRpcService) Stop() error {
	return nil
}

func createTxReceiptResult(receipt *types.Receipt) (*rpcjson.ReceiptResult, error) {

	logs := make([]*rpcjson.LogResult, 0)
	for _, log := range receipt.Logs {

		topics := make([]string, 0)
		for _, topic := range log.Topics {
			topics = append(topics, topic.String())
		}
		logs = append(logs, &rpcjson.LogResult{
			Address:     log.Address.String(),
			Topics:      topics,
			Data:        common.Bytes2Hex(log.Data),
			BlockNumber: log.BlockNumber,
			TxHash:      log.TxHash.String(),
			TxIndex:     log.TxIndex,
			BlockHash:   log.BlockHash.String(),
			Index:       log.Index,
			Removed:     log.Removed,
		})

	}

	bloom := ""
	if len(logs) != 0 {
		bloom = common.Bytes2Hex(receipt.Bloom.Bytes())
	}

	return &rpcjson.ReceiptResult{
		PostState:         common.Bytes2Hex(receipt.PostState),
		Status:            receipt.Status,
		CumulativeGasUsed: receipt.CumulativeGasUsed,
		Bloom:             bloom,
		Logs:              logs,
		TxHash:            receipt.TxHash.String(),
		ContractAddress:   receipt.ContractAddress.String(),
		GasUsed:           receipt.GasUsed,
	}, nil

}

func (s *PublicRpcAPI) CurrentPeers() []string {
	return s.cfg.SyncMgr.ListPeerStates()
}

// Get detail mining information of consensus
func (s *PublicRpcAPI) GetConsensusMiningInfo() (interface{}, error) {
	chain := s.cfg.Chain

	contractAddr, miningMemberAbi := chain.GetConsensusMiningInfo()
	signUpFunc := common.ContractConsensusSatoshiPlus_SignupFunction()
	contractInfo := &rpcjson.GetConsensusMiningInfoResult{
		ContractAddr:    contractAddr.String(),
		MiningMemberAbi: miningMemberAbi,
		SignUpFunc:      signUpFunc,
	}
	return contractInfo, nil
}

// Get UTXO merge status of a given address
// Note UTXO merge is a feature provided by Asimov blockchain
// If there are too many UTXOs of a given address, they will be auto merged by the system
// in order to save space and prevent dust attack
// This feature is DISABLED for now
func (s *PublicRpcAPI) GetMergeUtxoStatus(address string, mergeCount int32) ([]rpcjson.GetMergeUtxoResult, error) {
	byte, err := hexutil.Decode(address)
	if err != nil {
		context := "Failed to decode address"
		return nil, internalRPCError(err.Error(), context)
	}
	addr, err := common.NewAddress(byte)
	if err != nil {
		context := "Failed to create ADDRESS object"
		return nil, internalRPCError(err.Error(), context)
	}

	addUtxoCnt := int32(0)
	view := txo.NewUtxoViewpoint()
	assetsMap := make(map[string]int64)
	preOutMap := make(map[string][]protos.OutPoint)
	assetBalance := make([]rpcjson.GetMergeUtxoResult, 0)
	outpoints, err := s.cfg.Chain.FetchUtxoViewByAddress(view, addr.ScriptAddress())
	if err == nil {
		if int32(len(*outpoints)) >= mergeCount {
			hasSpentInTxPool :=  s.cfg.TxMemPool.HasSpentInTxPool(outpoints)
			for preOut, e := range view.Entries() {
				if addUtxoCnt >= 600 {
					break
				}
				assets := hex.EncodeToString(e.Asset().Bytes())
				if !e.Asset().IsIndivisible() {
					findSpentInTxPool := false
					for _, spentPreOut := range hasSpentInTxPool {
						if spentPreOut == preOut {
							findSpentInTxPool = true
							break
						}
					}
					if findSpentInTxPool {
						continue
					}

					if amount, ok := assetsMap[assets]; ok {
						assetsMap[assets] = amount + e.Amount()
					} else {
						assetsMap[assets] = e.Amount()
					}

					if _, ok := preOutMap[assets]; ok {
						preOutList := preOutMap[assets]
						preOutList = append(preOutList,preOut)
						preOutMap[assets] = preOutList
					} else {
						preOutList := make([]protos.OutPoint,0)
						preOutList = append(preOutList,preOut)
						preOutMap[assets] = preOutList
					}
					addUtxoCnt ++
				}
			}
		}
	} else {
		context := "Failed to call FetchUtxoViewByAddress"
		return nil, internalRPCError(err.Error(), context)
	}

	if addUtxoCnt < mergeCount {
		return assetBalance, nil
	}
	for k, v := range assetsMap {
		b := rpcjson.GetMergeUtxoResult{
			Asset: k,
			Value: v,
			PreOutsList: preOutMap[k],
		}
		assetBalance = append(assetBalance, b)
	}
	return assetBalance, nil
}

// Get the sign-up status in Satoshi+ consensus of a given address
func (s *PublicRpcAPI) GetSignUpStatus(address string) (interface{}, error) {
	byte, err := hexutil.Decode(address)
	if err != nil {
		context := "Failed to decode address"
		return nil, internalRPCError(err.Error(), context)
	}
	addr, err := common.NewAddress(byte)
	if err != nil {
		context := "Failed to create ADDRESS object"
		return nil, internalRPCError(err.Error(), context)
	}

	chain := s.cfg.Chain
	chainSnapshot := chain.BestSnapshot()
	curHeight := chainSnapshot.Height
	curNode, err := chain.GetNodeByHeight(curHeight)
	if err != nil {
		context := "Failed to get node by height"
		return nil, internalRPCError(err.Error(), context)
	}

	isActive := blockchain.IsActive(addr, curNode)

	createSignUpFlag := false
	if !isActive {
		createSignUpFlag = true
	}

	balance := int64(0)
	prevOutsList := make([]protos.OutPoint, 0)
	if createSignUpFlag {
		view := txo.NewUtxoViewpoint()
		var prevOuts *[]protos.OutPoint
		prevOuts, err = chain.FetchUtxoViewByAddressAndAsset(view, addr.ScriptAddress(), &asiutil.AsimovAsset)
		if err != nil {
			context := "Failed to fetch utxo View by addr and asset"
			return nil, internalRPCError(err.Error(), context)
		}
		for _, prevOut := range *prevOuts {
			entry := view.LookupEntry(prevOut)
			if entry == nil || entry.IsSpent() {
				continue
			}
			balance += entry.Amount()
			prevOutsList = append(prevOutsList, prevOut)

			if balance >= int64(chaincfg.DefaultAutoSignUpGasLimit) {
				break
			}
		}
	}

	signUpInfo := &rpcjson.GetSignUpStatusResult{
		AutoSignUp:  createSignUpFlag,
		PreOutsList: prevOutsList,
		Balance:     balance,
	}
	return signUpInfo, nil
}

// Get block information in detail of a given round in Satoshi+ consensus
func (s *PublicRpcAPI) GetRoundInfo(round uint32) (interface{}, error) {
	validators, _, err := s.cfg.Chain.GetValidators(round)
	if err != nil {
		return nil, internalRPCError(err.Error(), "failed to get validators in the previous round")
	}
	res := make([]common.Address, len(validators))
	for i, v := range validators {
		res[i] = *v
	}

	return res, nil
}

func (s *PublicRpcAPI) GetBlockTemplate(privkey string, round uint32, slotIndex uint16) (interface{}, error) {
	acc, err := crypto.NewAccount(privkey)
	if err != nil {
		return nil,  internalRPCError(err.Error(), "privkey decode error")
	}

	// 5 seconds
	blockInteval := 5.0 * 100000
	template, err := s.cfg.BlockTemplateGenerator.ProduceNewBlock(acc,
		common.GasFloor, common.GasCeil,
		time.Now().Unix(), round, slotIndex, blockInteval)
	if err != nil {
		return nil, internalRPCError(err.Error(), "failed to get block template")
	}
	w := bytes.NewBuffer(make([]byte, 0, template.Block.MsgBlock().SerializeSize()))
	template.Block.MsgBlock().Serialize(w)
	return hexutil.Encode(w.Bytes()), nil
}

func (s *PublicRpcAPI) SignBlock(blockHash string, privkey string) (interface{}, error) {
	bytes, err := hex.DecodeString(blockHash)
	hash := common.BytesToHash(bytes)
	if err != nil {
		context := "Failed to decode hex to hash"
		return nil, internalRPCError(err.Error(), context)
	}

	privKeyBytes, err := hexutil.Decode(privkey)
	if err != nil {
		context := "Failed to decode private key"
		return nil, internalRPCError(err.Error(), context)
	}

	privKey, _ := crypto.PrivKeyFromBytes(crypto.S256(), privKeyBytes)

	signature, err := crypto.Sign(hash.Bytes(), (*ecdsa.PrivateKey)(privKey))
	if err != nil {
		context := "Failed to sign the block"
		return nil, internalRPCError(err.Error(), context)
	}
	return hexutil.Encode(signature), nil
}
