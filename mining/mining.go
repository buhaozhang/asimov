// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package mining

import (
	"container/heap"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"sort"
	"time"

	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/common/serialization"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
)

const (
	// BlockHeaderOverhead is the max number of bytes it takes to serialize
	// a block header and max possible transaction count.
	BlockHeaderOverhead = protos.BlockHeaderPayload + serialization.MaxVarIntPayload

	// CoinbaseFlags is added to the coinbase script of a generated block.
	CoinbaseFlags = "/P2SH/asimovd/"

	// Init status of minging source tx
	MiningTxInit = 1 << iota

	// Processed status of minging source tx
	MiningTxProcessed

	// The count of a normal tx's input should less than 32. It also accept tx with more input.
    txInputNum = 32
)

// TxDesc is a descriptor about a transaction in a transaction source along with
// additional metadata.
type TxDesc struct {
	// Tx is the transaction associated with the entry.
	Tx *asiutil.Tx

	// Added is the time when the entry was added to the source pool.
	Added time.Time

	// Round is the block height when the entry was added to the the source
	// pool.
	Height int32

	// Fee is the total fee the transaction associated with the entry pays.
	Fee int64

	// FeeList is the list of all asset fee with the entry pays.
	FeeList *map[protos.Asset]int64

	// GasPrice is the price of fee the transaction pays.
	// GasPrice = fee / (size * common.GasPerByte + gaslimit)
	GasPrice float64

	// UtxoFetchCount is count number of utxo validation when producing
	// new block for this txdesc
	UtxoFetchCount float64
}

// A list of TxDesc, this type is only used for sort.
type TxDescList []*TxDesc


// Len returns the number of items in the list.
func (l TxDescList) Len() int {
	return len(l)
}

// Less returns whether the item in the list with index i should sort
// before the item with index j by great GasPrice / UtxoFetchCount.
func (l TxDescList) Less(i, j int) bool {
	return l[i].GasPrice * l[j].UtxoFetchCount > l[j].GasPrice * l[i].UtxoFetchCount
}

// Swap swaps the items at the passed indices in the list.
func (l TxDescList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

type SigDesc struct {
	sig *protos.MsgBlockSign
}

// TxSource represents a source of transactions to consider for inclusion in
// new blocks.
//
// The interface contract requires that all of these methods are safe for
// concurrent access with respect to the source.
type TxSource interface {
	// MiningDescs returns a slice of mining descriptors for all the
	// transactions in the source pool.
	TxDescs() TxDescList

	// UpdateForbiddenTxs put given txhashes into forbiddenTxs.
	// If size of forbiddenTxs exceed limit, clear some olders.
	UpdateForbiddenTxs(txHashes []*common.Hash, height int64)
}

type SigSource interface {
	MiningDescs(height int32) []*asiutil.BlockSign
}

// TxPrioItem houses a transaction along with extra information that allows the
// transaction to be prioritized and track dependencies on other transactions
// which have not been mined into a block yet.
type TxPrioItem struct {
	tx       *asiutil.Tx
	gasPrice float64

	// dependsOn holds a map of transaction hashes which this one depends
	// on.  It will only be set when the transaction references other
	// transactions in the source pool and hence must come after them in
	// a block.
	dependsOn map[common.Hash]struct{}
}

// txPriorityQueue implements a priority queue of TxPrioItem elements that
// supports an arbitrary compare function as defined by txPriorityQueueLessFunc.
type txPriorityQueue struct {
	items []*TxPrioItem
}

// Len returns the number of items in the priority queue.  It is part of the
// heap.Interface implementation.
func (pq *txPriorityQueue) Len() int {
	return len(pq.items)
}

// Less returns whether the item in the priority queue with index i should sort
// before the item with index j by deferring to the assigned less function.  It
// is part of the heap.Interface implementation.
func (pq *txPriorityQueue) Less(i, j int) bool {
	return pq.items[i].gasPrice > pq.items[j].gasPrice
}

// Swap swaps the items at the passed indices in the priority queue.  It is
// part of the heap.Interface implementation.
func (pq *txPriorityQueue) Swap(i, j int) {
	pq.items[i], pq.items[j] = pq.items[j], pq.items[i]
}

// Push pushes the passed item onto the priority queue.  It is part of the
// heap.Interface implementation.
func (pq *txPriorityQueue) Push(x interface{}) {
	pq.items = append(pq.items, x.(*TxPrioItem))
}

// Pop removes the highest priority item (according to Less) from the priority
// queue and returns it.  It is part of the heap.Interface implementation.
func (pq *txPriorityQueue) Pop() interface{} {
	n := len(pq.items)
	item := pq.items[n-1]
	pq.items[n-1] = nil
	pq.items = pq.items[0 : n-1]
	return item
}

// NewTxPriorityQueue returns a new transaction priority queue that reserves the
// passed amount of space for the elements.  The new priority queue uses either
// the txPQByPriority or the txPQByFee compare function depending on the
// sortByFee parameter and is already initialized for use with heap.Push/Pop.
// The priority queue can grow larger than the reserved space, but extra copies
// of the underlying array can be avoided by reserving a sane value.
func NewTxPriorityQueue(reserve int) *txPriorityQueue {
	pq := &txPriorityQueue{
		items: make([]*TxPrioItem, 0, reserve),
	}
	return pq
}

// mergeUtxoView adds all of the entries in viewB to viewA.  The result is that
// viewA will contain all of its original entries plus all of the entries
// in viewB.  It will replace any entries in viewB which also exist in viewA
// if the entry in viewA is spent.
func mergeUtxoView(viewA *txo.UtxoViewpoint, viewB *txo.UtxoViewpoint) {
	viewAEntries := viewA.Entries()
	for outpoint, _ := range viewB.Entries() {
		if entryA, exists := viewAEntries[outpoint]; exists && entryA != nil && entryA.IsSpent() {
			return
		}
	}
	for outpoint, entryB := range viewB.Entries() {
		if entryB != nil {
			viewA.AddEntry(outpoint, entryB)
		}
	}
	return
}

// StandardCoinbaseScript returns a standard script suitable for use as the
// signature script of the coinbase transaction of a new block.  In particular,
// it starts with the block height that is required by version 2 blocks and adds
// the extra nonce as well as additional coinbase flags.
func StandardCoinbaseScript(nextBlockHeight int32, extraNonce uint64) ([]byte, error) {
	return txscript.NewScriptBuilder().AddInt64(int64(nextBlockHeight)).
		AddInt64(int64(extraNonce)).AddData([]byte(CoinbaseFlags)).
		Script()
}

// CreateCoinbaseTx returns a coinbase transaction paying an appropriate subsidy
// based on the passed block height to the provided address.  When the address
// is nil, the coinbase transaction will instead be redeemable by anyone.
//
// See the comment for NewBlockTemplate for more information about why the nil
// address handling is useful.
func CreateCoinbaseTx(params *chaincfg.Params, nextBlockHeight int32, addr common.IAddress,
	contractOut *protos.TxOut) (*asiutil.Tx, *protos.TxOut, error) {
	// The extra nonce helps ensure the transaction is not a duplicate transaction
	// (paying the same value to the same public key address would otherwise be an
	// identical transaction for block version 1).
	extraNonce := uint64(0)
	coinbaseScript, err := StandardCoinbaseScript(nextBlockHeight, extraNonce)
	if err != nil {
		return nil, nil, err
	}
	// Create the script to pay to the provided payment address.
	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return nil, nil, err
	}

	tx := protos.NewMsgTx(protos.TxVersion)
	tx.AddTxIn(&protos.TxIn{
		// Coinbase transactions have no inputs, so previous outpoint is
		// zero hash and max index.
		PreviousOutPoint: *protos.NewOutPoint(&common.Hash{}, protos.MaxPrevOutIndex),
		SignatureScript:  coinbaseScript,
		Sequence:         protos.MaxTxInSequenceNum,
	})

	// If a new round start, update system consensus.
	if contractOut != nil {
		tx.AddTxOut(contractOut)
	}

	stdTxOut := &protos.TxOut{
		Value:    blockchain.CalcBlockSubsidy(nextBlockHeight, params),
		PkScript: pkScript,
		Asset:    asiutil.AsimovAsset,
	}
	tx.AddTxOut(stdTxOut)
	tx.TxContract.GasLimit = common.CoinbaseTxGas
	return asiutil.NewTx(tx), stdTxOut, nil
}

// logSkippedDeps logs any dependencies which are also skipped as a result of
// skipping a transaction while generating a block template at the trace level.
func logSkippedDeps(tx *asiutil.Tx, deps map[common.Hash]*TxPrioItem) {
	if deps == nil {
		return
	}

	for _, item := range deps {
		log.Tracef("Skipping tx %s since it depends on %s\n",
			item.tx.Hash(), tx.Hash())
	}
}

// getMilliSecond return the timestamp in millisecond of the current time.
func getMilliSecond() int64 {
	now := time.Now()
	return now.Unix() * 1000 + int64(now.Nanosecond()) / int64(time.Millisecond)
}

// BlockTemplate houses a block and a relation data including a virtual block,
// reciepts, logs.
type BlockTemplate struct {
	// Block is a block that is ready to be processed, it can't be mined or
	// passed from other nodes.
	Block *asiutil.Block

	// VBlock is a virtual block that is ready to be processed, it is mined.
	VBlock *asiutil.VBlock

	Receipts types.Receipts

	Logs     []*types.Log
}

// BlkTmplGenerator provides a type that can be used to generate block templates
// based on a given mining policy and source of transactions to choose from.
// It also houses additional state required in order to ensure the templates
// are built on top of the current best chain and adhere to the consensus rules.
type BlkTmplGenerator struct {
	policy       *Policy
	txSource     TxSource
	sigSource    SigSource
	chain        *blockchain.BlockChain

	FetchUtxoView func(tx *asiutil.Tx, dolock bool) (*txo.UtxoViewpoint, error)
}

// NewBlkTmplGenerator returns a new block template generator for the given
// policy using transactions from the provided transaction source.
//
// The additional state-related fields are required in order to ensure the
// templates are built on top of the current best chain and adhere to the
// consensus rules.
func NewBlkTmplGenerator(policy *Policy,
	txSource TxSource, sigSource SigSource, chain *blockchain.BlockChain) *BlkTmplGenerator {

	return &BlkTmplGenerator{
		policy:     policy,
		txSource:   txSource,
		sigSource:  sigSource,
		chain:      chain,
		FetchUtxoView: chain.FetchUtxoView,
	}
}

// ProduceNewBlock returns a new block template that is ready to be solved
// using the transactions from the passed transaction source pool and a coinbase
// that either pays to the passed address if it is not nil, or a coinbase that
// is redeemable by anyone if the passed address is nil.  The nil address
// functionality is useful since there are cases such as the getblocktemplate
// RPC where external mining software is responsible for creating their own
// coinbase which will replace the one generated for the block template.  Thus
// the need to have configured address can be avoided.
//
// The transactions selected and included are prioritized according to several
// factors.  First, each transaction has a priority calculated based on its
// value, age of inputs, and size.  Transactions which consist of larger
// amounts, older inputs, and small sizes have the highest priority.  Second, a
// fee per kilobyte is calculated for each transaction.  Transactions with a
// higher fee per kilobyte are preferred.  Finally, the block generation related
// policy settings are all taken into account.
//
// Once the high-priority area (if configured) has been filled with
// transactions, or the priority falls below what is considered high-priority,
// the priority queue is updated to prioritize by fees per kilobyte (then
// priority).
//
// Given the above, a block generated by this function is of the following form:
//
//   -----------------------------------  --  --
//  |                                   |   |
//  |                                   |   |
//  |                                   |   |
//  |  Transactions prioritized by price|   |
//  |                                   |   |
//  |-----------------------------------| --|
//  |      Coinbase Transaction         |   |
//   -----------------------------------  --
func (g *BlkTmplGenerator) ProduceNewBlock(account *crypto.Account, gasFloor, gasCeil uint64,
	blockTime int64,
	round uint32, slotIndex uint16, blockInterval float64) (
	blockTemplate *BlockTemplate, err error) {

	produceBlockTimeInterval := g.policy.BlockProductedTimeOut * blockInterval
	utxoValidateTimeInterval := g.policy.UtxoValidateTimeOut * produceBlockTimeInterval
	produceTxTimeInterval := (g.policy.UtxoValidateTimeOut + g.policy.TxConnectTimeOut) * produceBlockTimeInterval

	produceBlockStartTime := getMilliSecond()

	// Get the current source transactions and create a priority queue to
	// hold the transactions which are ready for inclusion into a block
	// along with some priority related and fee metadata.  Reserve the same
	// number of items that are available for the priority queue.  Also,
	// choose the initial sort order for the priority queue based on whether
	// or not there is an area allocated for high-priority transactions.
	sourceTxns := g.txSource.TxDescs()
	sort.Sort(sourceTxns)

	forbiddenTxHashes := make([]*common.Hash, 0, len(sourceTxns))
	priorityQueue := NewTxPriorityQueue(len(sourceTxns))
	txpool := make(map[common.Hash]int)
	for _, tx := range sourceTxns {
		txpool[*tx.Tx.Hash()] = MiningTxInit
	}

	// Collect pre blocks sigs
	bestHeight := g.chain.GetTip().Height()
	totalPreSigns := g.sigSource.MiningDescs(bestHeight)

	payToAddress := account.Address
	var msgBlock protos.MsgBlock
	header := &msgBlock.Header
	header.Round = round
	header.SlotIndex = slotIndex
	header.Timestamp = blockTime
	header.CoinBase = *payToAddress
	stateDB, feepool, contractOut, err := g.chain.Prepare(header, gasFloor, gasCeil)
	defer func() {
		g.chain.ChainRUnlock()
		if err == nil && len(forbiddenTxHashes) > 0 {
			g.txSource.UpdateForbiddenTxs(forbiddenTxHashes, int64(bestHeight + 1))
		}
	}()
	if err != nil {
		return nil, err
	}
	newBlock := asiutil.NewBlock(&msgBlock)

	// Create a standard coinbase transaction paying to the provided
	// address.  NOTE: The coinbase value will be updated to include the
	// fees from the selected transactions later after they have actually
	// been selected.  It is created here to detect any errors early
	// before potentially doing a lot of work below.
	coinbaseTx, stdTxout, err := CreateCoinbaseTx(chaincfg.ActiveNetParams.Params,
		header.Height, payToAddress,
		contractOut)
	if err != nil {
		return nil, err
	}
	coinbaseSigOpCost := int64(blockchain.CountSigOps(coinbaseTx))

	// flag whether core team take reward
	coreTeamRewardFlag := header.Height <= chaincfg.ActiveNetParams.Params.SubsidyReductionInterval ||
		blockTime - chaincfg.ActiveNetParams.Params.GenesisBlock.Header.Timestamp < 86400*(365*4+1)
	txoutSizePerAsset := stdTxout.SerializeSize()
	if coreTeamRewardFlag {
		txoutSizePerAsset *= 2
	}

	// Create a slice to hold the transactions to be included in the
	// generated block with reserved space.  Also create a utxo view to
	// house all of the input transactions so multiple lookups can be
	// avoided.
	blockTxns := make([]*asiutil.Tx, 0, len(sourceTxns) + 1)
	coinbaseGasLimit := int(coinbaseTx.MsgTx().TxContract.GasLimit)
	blockGasLimit := coinbaseGasLimit

	// The starting block size is the size of the block header plus the max
	// possible transaction count size, plus the size of the coinbase
	// transaction.
	blockSize := BlockHeaderOverhead + coinbaseTx.MsgTx().SerializeSize() -
		stdTxout.SerializeSize() + txoutSizePerAsset
	// add block body field ReceiptHash, bloom and three var size
	blockSize += common.HashLength + types.BloomByteLength + serialization.MaxVarIntPayload*3

	// The block weight is the sum of all signature's weight
	blockWeight := uint16(0)

	// filter signatures that is already packaged
	totalPreSigns = g.chain.FilterPackagedSignatures(totalPreSigns)
	preBlockSigs := make(protos.BlockSignList, 0, len(totalPreSigns))
	for _, blockSign := range totalPreSigns {
		if blockSign.MsgSign.BlockHeight < header.Height-common.BlockSignDepth {
			continue
		}
		//Skip the msgSign that the signed block is create by himself & on soft fork chain
		node, err := g.chain.GetNodeByHeight(blockSign.MsgSign.BlockHeight)
		if err != nil {
			continue
		}
		if node.Hash() != blockSign.MsgSign.BlockHash || node.Coinbase() == blockSign.MsgSign.Signer {
			continue
		}
		preBlockSigs = append(preBlockSigs, blockSign.MsgSign)
	}

	sort.Sort(preBlockSigs)
	// Add self sig weight.
	_, weightMap, err := g.chain.GetValidators(round)
	if err != nil {
		return nil, err
	}
	curWeight, ok := weightMap[*payToAddress]
	if !ok {
		errStr := fmt.Sprint("Unexpected slotIndex", payToAddress)
		return nil, errors.New(errStr)
	}
	blockWeight += curWeight
	for _, sign := range preBlockSigs {
		node, err := g.chain.GetNodeByHeight(sign.BlockHeight)
		if err != nil {
			continue
		}
		blockSize += sign.SerializeSize()
		_, weightMap, err = g.chain.GetValidators(node.Round())
		if err != nil {
			return nil, err
		}

		if _, ok := weightMap[sign.Signer]; !ok {
			continue
		}
		blockWeight += weightMap[sign.Signer]
		msgBlock.PreBlockSigs = append(msgBlock.PreBlockSigs, sign)
	}
	header.Weight = blockWeight

	// Now that the actual signs have been selected, update the
	// block size for the real sign count.
	blockSize -= serialization.MaxVarIntPayload -
		serialization.VarIntSerializeSize(uint64(len(preBlockSigs)))

	blockUtxos := txo.NewUtxoViewpoint()

	// dependers is used to track transactions which depend on another
	// transaction in the source pool.  This, in conjunction with the
	// dependsOn map kept with each dependent transaction helps quickly
	// determine which dependent transactions are now eligible for inclusion
	// in the block once each transaction has been included.
	dependers := make(map[common.Hash]map[common.Hash]*TxPrioItem)

	// Create slices to hold the fees and number of signature operations
	// for each of the selected transactions and add an entry for the
	// coinbase.  This allows the code below to simply append details about
	// a transaction as it is selected for inclusion in the final block.
	// However, since the total fees aren't known yet, use a dummy value for
	// the coinbase fee which will be updated later.
	txSigOpCosts := make([]int64, 0, len(sourceTxns))
	txSigOpCosts = append(txSigOpCosts, coinbaseSigOpCost)

	utxostart := getMilliSecond()
mempoolLoop:
	for _, txDesc := range sourceTxns {
		// break loop if time out
		if float64(getMilliSecond() - produceBlockStartTime) > utxoValidateTimeInterval {
			break
		}
		// A block can't have more than one coinbase or contain
		// non-finalized transactions.
		tx := txDesc.Tx
		if blockchain.IsCoinBase(tx) {
			log.Tracef("Skipping coinbase tx %s", tx.Hash())
			continue
		}
		if !blockchain.IsFinalizedTransaction(tx, header.Height, blockTime) {
			log.Tracef("Skipping non-finalized tx %s", tx.Hash())
			continue
		}

		// Fetch all of the utxos referenced by the this transaction.
		// NOTE: This intentionally does not fetch inputs from the
		// mempool since a transaction which depends on other
		// transactions in the mempool must come after those
		// dependencies in the final generated block.
		utxos, err := g.FetchUtxoView(tx, false)
		if err != nil {
			log.Warnf("Unable to fetch utxo view for tx %s: %v",
				tx.Hash(), err)
			continue
		}
		txDesc.UtxoFetchCount++

		// Setup dependencies for any transactions which reference
		// other transactions in the mempool so they can be properly
		// ordered below.
		prioItem := &TxPrioItem{
			tx: tx}
		for _, txIn := range tx.MsgTx().TxIn {
			originHash := &txIn.PreviousOutPoint.Hash
			entry := utxos.LookupEntry(txIn.PreviousOutPoint)
			if entry == nil || entry.IsSpent() {
				if _, exists := txpool[*originHash]; !exists {
					log.Tracef("Skipping tx %s because it "+
						"references unspent output %s "+
						"which is not available",
						tx.Hash(), txIn.PreviousOutPoint)
					continue mempoolLoop
				}

				// The transaction is referencing another
				// transaction in the source pool, so setup an
				// ordering dependency.
				deps, exists := dependers[*originHash]
				if !exists {
					deps = make(map[common.Hash]*TxPrioItem)
					dependers[*originHash] = deps
				}
				deps[*prioItem.tx.Hash()] = prioItem
				if prioItem.dependsOn == nil {
					prioItem.dependsOn = make(
						map[common.Hash]struct{})
				}
				prioItem.dependsOn[*originHash] = struct{}{}

				// Skip the check below. We already know the
				// referenced transaction is available.
				continue
			}
		}

		prioItem.gasPrice = txDesc.GasPrice

		// Merge the referenced outputs from the input transactions to
		// this transaction into the block utxo view.  This allows the
		// code below to avoid a second lookup.
		mergeUtxoView(blockUtxos, utxos)

		// Add the transaction to the priority queue to mark it ready
		// for inclusion in the block unless it has dependencies.
		if prioItem.dependsOn == nil {
			heap.Push(priorityQueue, prioItem)
		}
	}
	utxoEnd := getMilliSecond()

	blockSigOpCost := coinbaseSigOpCost
	allFees := map[protos.Asset]int64 {
		asiutil.AsimovAsset: 0,
	}
	var totalGasUsed uint64
	var receipts types.Receipts
	var	allLogs  []*types.Log
	txidx := 0
	var msgvblock protos.MsgVBlock
	stxos := make([]txo.SpentTxOut, 0, 1000)

	// Choose which transactions make it into the block.
	processTxStartTime := getMilliSecond()
	log.Debug("Start priorityQueue", priorityQueue.Len())
priorityQueueLoop:
	for priorityQueue.Len() > 0 {
		interval := float64(getMilliSecond() - produceBlockStartTime)
		if interval > produceBlockTimeInterval || interval > produceTxTimeInterval {
			log.Debug("mine time out ")
			break
		}

		// Grab the highest priority (or highest fee per kilobyte
		// depending on the sort order) transaction.
		prioItem := heap.Pop(priorityQueue).(*TxPrioItem)
		tx := prioItem.tx
		txpool[*tx.Hash()] = MiningTxProcessed

		// Grab any transactions which depend on this one.
		deps := dependers[*tx.Hash()]

		// Enforce maximum block size.  Also check for overflow.
		txSize := tx.MsgTx().SerializeSize()
		blockPlusTxSize := blockSize + txSize
		if blockPlusTxSize < blockSize ||
			blockPlusTxSize >= common.MaxBlockSize {
			log.Tracef("Skipping tx %s because it would exceed "+
				"the max block size", tx.Hash())
			logSkippedDeps(tx, deps)
			continue
		}

		// Enforce maximum gaslimit. Also check for overflow
		txGasLimit := int(tx.MsgTx().TxContract.GasLimit)
		blockPlusGaslimit := blockGasLimit + txGasLimit
		if blockPlusGaslimit < blockGasLimit ||
			blockPlusGaslimit >= int(header.GasLimit) {
			log.Tracef("Skipping tx %s because it would exceed "+
				"the max gas limit", tx.Hash())
			logSkippedDeps(tx, deps)
			continue
		}

		// Enforce maximum signature operation cost per block.  Also
		// check for overflow.
		sigOpCost, err := blockchain.GetSigOpCost(tx, false, blockUtxos)
		if err != nil {
			log.Tracef("Skipping tx %s due to error in "+
				"GetSigOpCost: %v", tx.Hash(), err)
			logSkippedDeps(tx, deps)
			continue
		}
		if blockSigOpCost+int64(sigOpCost) < blockSigOpCost ||
			blockSigOpCost+int64(sigOpCost) > blockchain.MaxBlockSigOpsCost {
			log.Tracef("Skipping tx %s because it would "+
				"exceed the maximum sigops per block", tx.Hash())
			logSkippedDeps(tx, deps)
			continue
		}

		// Ensure the transaction inputs pass all of the necessary
		// preconditions before allowing it to be added to the block.
		fee, feeList, err := blockchain.CheckTransactionInputs(tx, header.Height, blockUtxos, g.chain)
		if err != nil {
			log.Tracef("Skipping tx %s due to error in "+
				"CheckTransactionInputs: %v", tx.Hash(), err)
			logSkippedDeps(tx, deps)
			continue
		}

		for asset := range *feeList {
			if _, ok := feepool[asset]; !ok {
				log.Tracef("Skipping tx %s because its "+
					"fee %v is unsupported",
					tx.Hash(), asset)
				logSkippedDeps(tx, deps)
				continue priorityQueueLoop
			}
			if _, ok := allFees[asset]; !ok {
				txSize += txoutSizePerAsset
				blockPlusTxSize = blockSize + txSize
				if blockPlusTxSize < blockSize || blockPlusTxSize >= common.MaxBlockSize {
					log.Tracef("Skipping tx %s because it would exceed "+
						"the max block size", tx.Hash())
					logSkippedDeps(tx, deps)
					continue priorityQueueLoop
				}
			}
		}

		err = blockchain.ValidateTransactionScripts(tx, blockUtxos,
			txscript.StandardVerifyFlags)
		if err != nil {
			log.Tracef("Skipping tx %s due to error in "+
				"ValidateTransactionScripts: %v", tx.Hash(), err)
			logSkippedDeps(tx, deps)
			continue
		}

		// try connect transaction
		stateDB.Prepare(*tx.Hash(), common.Hash{}, txidx)
		receipt, err, gasUsed, vtx, _ := g.chain.ConnectTransaction(
			newBlock, txidx, blockUtxos, tx, &stxos, stateDB, fee)

		if err != nil {
			log.Debugf("Skipping tx %s because it failed to connect",
				tx.Hash())
			logSkippedDeps(tx, deps)
			forbiddenTxHashes = append(forbiddenTxHashes, tx.Hash())
			for _, txIn := range tx.MsgTx().TxIn {
				// Ensure the referenced utxo exists in the view.  This should
				// never happen unless there is a bug is introduced in the code.
				entry := blockUtxos.LookupEntry(txIn.PreviousOutPoint)
				if entry != nil {
					entry.UnSpent()
				}
			}
			continue
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
			allLogs  = append(allLogs, receipt.Logs...)
		}

		totalGasUsed += gasUsed
		if vtx != nil {
			msgvblock.AddTransaction(vtx)
		}
		txidx++

		// Add the transaction to the block, increment counters, and
		// save the fees and signature operation counts to the block
		// template.
		blockTxns = append(blockTxns, tx)
		blockSize += txSize
		blockGasLimit += txGasLimit
		blockSigOpCost += int64(sigOpCost)
		blockchain.MergeFees(&allFees, feeList)
		txSigOpCosts = append(txSigOpCosts, int64(sigOpCost))

		log.Tracef("Adding tx %s (gasPrice %.2f)",
			tx.Hash(), prioItem.gasPrice)

		// Add transactions which depend on this one (and also do not
		// have any other unsatisified dependencies) to the priority
		// queue.
		for _, item := range deps {
			// Add the transaction to the priority queue if there
			// are no more dependencies after this one.
			delete(item.dependsOn, *tx.Hash())
			if len(item.dependsOn) == 0 && txpool[*item.tx.Hash()] != MiningTxProcessed {
				heap.Push(priorityQueue, item)
			}
		}
	}
	processTxEndTime := getMilliSecond()

	log.Debugf("Miner total count of tx=%d, blockSize=%d, gasLimit=%d, sigOpCost=%d" +
		" time limit block interval=%d, utxo interval=%d, tx interval=%d" +
		" costs utxo %d, tx %d, all %d",
		txidx, blockSize, blockGasLimit, blockSigOpCost,
		int64(produceBlockTimeInterval), int64(utxoValidateTimeInterval), int64(produceTxTimeInterval),
		utxoEnd-utxostart, processTxEndTime - processTxStartTime,
		processTxEndTime - produceBlockStartTime)

	rebuildFunder(coinbaseTx, stdTxout, &allFees)

	// reward for core team
	if coreTeamRewardFlag {
		fundationAddr := common.HexToAddress(string(common.GenesisOrganization))
		pkScript, _ := txscript.PayToAddrScript(&fundationAddr)
		txoutLen := len(coinbaseTx.MsgTx().TxOut)
		for i := 0; i < txoutLen; i++ {
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
	stateDB.Prepare(*coinbaseTx.Hash(), common.Hash{}, txidx)
	fee, _, _ := blockchain.CheckTransactionInputs(coinbaseTx, g.chain.BestSnapshot().Height,
		blockUtxos, g.chain)
	receipt, err, gasUsed, vtx, _ := g.chain.ConnectTransaction(
		newBlock, txidx, blockUtxos, coinbaseTx, &stxos, stateDB, fee)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		receipts = append(receipts, receipt)
		allLogs = append(allLogs, receipt.Logs...)
	}
	totalGasUsed += gasUsed
	if vtx != nil {
		msgvblock.AddTransaction(vtx)
	}

	blockTxns = append(blockTxns, coinbaseTx)
	for _, tx := range blockTxns {
		msgBlock.AddTransaction(tx.MsgTx())
	}

	// Now that the actual transactions have been selected, update the
	// block size for the real transaction count and coinbase value with
	// the total fees accordingly.
	blockSize -= serialization.MaxVarIntPayload -
		serialization.VarIntSerializeSize(uint64(len(blockTxns)))

	// Create a new block ready to be solved.
	merkles := blockchain.BuildMerkleTreeStore(blockTxns)
	header.MerkleRoot = *merkles[len(merkles)-1]

	receiptHash := types.DeriveSha(receipts)
	logBloom := types.CreateBloom(receipts)

	msgBlock.ReceiptHash = receiptHash
	msgBlock.Bloom = logBloom
	msgBlock.Header.GasUsed = totalGasUsed
	msgBlock.Header.PoaHash = msgBlock.CalculatePoaHash()

	err = commit(&msgBlock, stateDB, account)
	if err != nil {
		return nil, err
	}

	block := asiutil.NewBlock(&msgBlock)
	template := BlockTemplate{
		Block: block,
		VBlock: asiutil.NewVBlock(&msgvblock, block.Hash()),
		Receipts: receipts,
		Logs: allLogs,
	}

	return &template, nil
}

// append fee into coinbase tx
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

// commit state and signature the given block
func commit(block *protos.MsgBlock, stateDB *state.StateDB, account *crypto.Account) error {
	stateRoot, err := stateDB.Commit(true)
	if err != nil {
		return err
	}
	if err := stateDB.Database().TrieDB().Commit(stateRoot, false); err != nil {
		return err
	}
	block.Header.StateRoot = stateRoot

	blockHash := block.BlockHash()
	signature, err := crypto.Sign(blockHash[:], (*ecdsa.PrivateKey)(&account.PrivateKey))
	if err != nil {
		log.Errorf("sign block failed: %s", err)
		return err
	}
	copy(block.Header.SigData[:], signature)
	return nil
}