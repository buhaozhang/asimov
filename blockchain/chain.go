// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/cache"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/rpcs/rawdb"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/virtualtx"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/event"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"
)

const (
	// maxOrphanBlocks is the maximum number of orphan blocks that can be
	// queued.
	maxOrphanBlocks = 5000

	TemplateHeaderLength int = 18

	// 2M limit
	TemplateDataFieldLength int = 1 << 21

)

var (
	errExecutionRevertedString     = "fvm: execution reverted"
)

// BlockLocator is used to help locate a specific block.  The algorithm for
// building the block locator is to add the hashes in reverse order until
// the genesis block is reached.  In order to keep the list of locator hashes
// to a reasonable number of entries, first the most recent previous 12 block
// hashes are added, then the step is doubled each loop iteration to
// exponentially decrease the number of hashes as a function of the distance
// from the block being located.
//
// For example, assume a block chain with a side chain as depicted below:
// 	genesis -> 1 -> 2 -> ... -> 15 -> 16  -> 17  -> 18
// 	                              \-> 16a -> 17a
//
// The block locator for block 17a would be the hashes of blocks:
// [17a 16a 15 14 13 12 11 10 9 8 7 6 4 genesis]
type BlockLocator []*common.Hash

// orphanBlock represents a block that we don't yet have the parent for.  It
// is a normal block plus an expiration time to prevent caching the orphan
// forever.
type orphanBlock struct {
	block      *asiutil.Block
	expiration time.Time
}

// BestState houses information about the current best block and other info
// related to the state of the main chain as it exists from the point of view of
// the current best block.
//
// The BestSnapshot method can be used to obtain access to this information
// in a concurrent safe manner and the data will not be changed out from under
// the caller when chain state changes occur as the function name implies.
// However, the returned snapshot must be treated as immutable since it is
// shared by all callers.
type BestState struct {
	Hash      common.Hash // The hash of the block.
	StateRoot common.Hash // hash of the state root, used to fetch the root information.
	Height    int32       // The height of the block.
	SlotIndex uint16      // The slot index per round of the block.
	Round     uint32      // The round of the block.
	Weight    uint64      // The total weight of the blocks from genesis to current block.
	BlockSize uint64      // The size of the block.
	NumTxns   uint64      // The number of txns in the block.
	TotalTxns uint64      // The total number of txns in the chain.
	TimeStamp int64       // timestamp of the block.

	GasLimit uint64 // The gaslimit of the block.
	GasUsed  uint64 // The used gas of the block.
}

// newBestState returns a new best stats instance for the given parameters.
func newBestState(node *blockNode, blockSize, numTxns,
	totalTxns uint64, timestamp int64) *BestState {

	return &BestState{
		Hash:      node.hash,
		StateRoot: node.stateRoot,
		Height:    node.height,
		SlotIndex: node.slot,
		Round:     node.round.Round,
		Weight:    node.weight,
		BlockSize: blockSize,
		NumTxns:   numTxns,
		TotalTxns: totalTxns,
		TimeStamp: timestamp,
		GasLimit:  node.gasLimit,
		GasUsed:   node.gasUsed,
	}
}

// BlockChain provides functions for working with the bitcoin block chain.
// It includes functionality such as rejecting duplicate blocks, ensuring blocks
// follow all rules, orphan handling, checkpoint handling, and best chain
// selection with reorganization.
type BlockChain struct {
	// The following fields are set when the instance is created and can't
	// be changed afterwards, so there is no need to protect them with a
	// separate mutex.
	checkpoints         []chaincfg.Checkpoint
	checkpointsByHeight map[int32]*chaincfg.Checkpoint
	db                  database.Transactor
	chainParams         *chaincfg.Params
	timeSource          MedianTimeSource
	indexManager        IndexManager
	contractManager     ainterface.ContractManager
	roundManager        ainterface.IRoundManager

	// chainLock protects concurrent access to the vast majority of the
	// fields in this struct below this point.
	chainLock sync.RWMutex

	// These fields are related to the memory block index.  They both have
	// their own locks, however they are often also protected by the chain
	// lock to help prevent logic races when blocks are being processed.
	//
	// index houses the entire block index in memory.  The block index is
	// a tree-shaped structure.
	//
	// bestChain tracks the current active chain by making use of an
	// efficient chain view into the block index.
	index     *blockIndex
	bestChain *chainView

	// These fields are related to handling of orphan blocks.  They are
	// protected by a combination of the chain lock and the orphan lock.
	orphanLock   sync.RWMutex
	orphans      map[common.Hash]*orphanBlock
	prevOrphans  map[common.Hash][]*orphanBlock
	oldestOrphan *orphanBlock

	// These fields are related to checkpoint handling.  They are protected
	// by the chain lock.
	nextCheckpoint *chaincfg.Checkpoint
	checkpointNode *blockNode

	// The state is used as a fairly efficient way to cache information
	// about the current best chain state that is returned to callers when
	// requested.  It operates on the principle of MVCC such that any time a
	// new block becomes the best block, the state pointer is replaced with
	// a new struct and the old state is left untouched.  In this way,
	// multiple callers can be pointing to different best chain states.
	// This is acceptable for most callers because the state is only being
	// queried at a specific point in time.
	//
	// In addition, some of the fields are stored in the database so the
	// chain state can be quickly reconstructed on load.
	stateLock     sync.RWMutex
	stateSnapshot *BestState

	// The following caches are used to efficiently keep track of the
	// current deployment threshold state of each rule change deployment.
	//
	// This information is stored in the database so it can be quickly
	// reconstructed on load.
	//
	// warningCaches caches the current deployment threshold state for blocks
	// in each of the **possible** deployments.  This is used in order to
	// detect when new unrecognized rule changes are being voted on and/or
	// have been activated such as will be the case when older versions of
	// the software are being used
	//
	// deploymentCaches caches the current deployment threshold state for
	// blocks in each of the actively defined deployments.
	warningCaches    []thresholdStateCache
	deploymentCaches []thresholdStateCache

	// The following fields are used to determine if certain warnings have
	// already been shown.
	//
	// unknownRulesWarned refers to warnings due to unknown rules being
	// activated.
	//
	// unknownVersionsWarned refers to warnings due to unknown versions
	// being mined.
	unknownRulesWarned    bool
	unknownVersionsWarned bool

	// The notifications field stores a slice of callbacks to be executed on
	// certain blockchain events.
	notificationsLock sync.RWMutex
	notifications     []NotificationCallback
	stateCache        state.Database // State database to reuse between imports (contains state cache)

	// rpcv2.0
	ethDB         database.Database
	rmLogsFeed    event.Feed
	chainFeed     event.Feed
	chainSideFeed event.Feed
	chainHeadFeed event.Feed
	logsFeed      event.Feed
	scope         event.SubscriptionScope

	templateIndex Indexer

	vmConfig vm.Config

	feesChan chan interface{}
}

// HaveBlock returns whether or not the chain instance has the block represented
// by the passed hash.  This includes checking the various places a block can
// be like part of the main chain, on a side chain, or in the orphan pool.
//
// This function is safe for concurrent access.
func (b *BlockChain) HaveBlock(hash *common.Hash) (bool, error) {
	exists, err := b.blockExists(hash)
	if err != nil {
		return false, err
	}
	return exists || b.IsKnownOrphan(hash), nil
}

// IsKnownOrphan returns whether the passed hash is currently a known orphan.
// Keep in mind that only a limited number of orphans are held onto for a
// limited amount of time, so this function must not be used as an absolute
// way to test if a block is an orphan block.  A full block (as opposed to just
// its hash) must be passed to ProcessBlock for that purpose.  However, calling
// ProcessBlock with an orphan that already exists results in an error, so this
// function provides a mechanism for a caller to intelligently detect *recent*
// duplicate orphans and react accordingly.
//
// This function is safe for concurrent access.
func (b *BlockChain) IsKnownOrphan(hash *common.Hash) bool {
	// Protect concurrent access.  Using a read lock only so multiple
	// readers can query without blocking each other.
	b.orphanLock.RLock()
	_, exists := b.orphans[*hash]
	b.orphanLock.RUnlock()

	return exists
}

// GetOrphanRoot returns the head of the chain for the provided hash from the
// map of orphan blocks.
//
// This function is safe for concurrent access.
func (b *BlockChain) GetOrphanRoot(hash *common.Hash) *common.Hash {
	// Protect concurrent access.  Using a read lock only so multiple
	// readers can query without blocking each other.
	b.orphanLock.RLock()
	defer b.orphanLock.RUnlock()

	// Keep looping while the parent of each orphaned block is
	// known and is an orphan itself.
	orphanRoot := hash
	prevHash := hash
	for {
		orphan, exists := b.orphans[*prevHash]
		if !exists {
			break
		}
		orphanRoot = prevHash
		prevHash = &orphan.block.MsgBlock().Header.PrevBlock
	}

	return orphanRoot
}

func (b *BlockChain) GetStateCache() state.Database {
	return b.stateCache
}

// removeOrphanBlock removes the passed orphan block from the orphan pool and
// previous orphan index.
func (b *BlockChain) removeOrphanBlock(orphan *orphanBlock) {
	// Protect concurrent access.
	b.orphanLock.Lock()
	defer b.orphanLock.Unlock()

	// Remove the orphan block from the orphan pool.
	orphanHash := orphan.block.Hash()
	delete(b.orphans, *orphanHash)

	// Remove the reference from the previous orphan index too.  An indexing
	// for loop is intentionally used over a range here as range does not
	// reevaluate the slice on each iteration nor does it adjust the index
	// for the modified slice.
	prevHash := &orphan.block.MsgBlock().Header.PrevBlock
	orphans := b.prevOrphans[*prevHash]
	for i := 0; i < len(orphans); i++ {
		hash := orphans[i].block.Hash()
		if hash.IsEqual(orphanHash) {
			copy(orphans[i:], orphans[i+1:])
			orphans[len(orphans)-1] = nil
			orphans = orphans[:len(orphans)-1]
			i--
		}
	}
	b.prevOrphans[*prevHash] = orphans

	// Remove the map entry altogether if there are no longer any orphans
	// which depend on the parent hash.
	if len(b.prevOrphans[*prevHash]) == 0 {
		delete(b.prevOrphans, *prevHash)
	}
}

// addOrphanBlock adds the passed block (which is already determined to be
// an orphan prior calling this function) to the orphan pool.  It lazily cleans
// up any expired blocks so a separate cleanup poller doesn't need to be run.
// It also imposes a maximum limit on the number of outstanding orphan
// blocks and will remove the oldest received orphan block if the limit is
// exceeded.
func (b *BlockChain) addOrphanBlock(block *asiutil.Block) {
	// Remove expired orphan blocks.
	for _, oBlock := range b.orphans {
		if time.Now().After(oBlock.expiration) {
			b.removeOrphanBlock(oBlock)
			continue
		}

		// Update the oldest orphan block pointer so it can be discarded
		// in case the orphan pool fills up.
		if b.oldestOrphan == nil || oBlock.expiration.Before(b.oldestOrphan.expiration) {
			b.oldestOrphan = oBlock
		}
	}

	// Limit orphan blocks to prevent memory exhaustion.
	if len(b.orphans)+1 > maxOrphanBlocks {
		// Remove the oldest orphan to make room for the new one.
		b.removeOrphanBlock(b.oldestOrphan)
		b.oldestOrphan = nil
	}

	// Protect concurrent access.  This is intentionally done here instead
	// of near the top since removeOrphanBlock does its own locking and
	// the range iterator is not invalidated by removing map entries.
	b.orphanLock.Lock()
	defer b.orphanLock.Unlock()

	// Insert the block into the orphan map with an expiration time
	// 1 hour from now.
	expiration := time.Now().Add(time.Hour)
	oBlock := &orphanBlock{
		block:      block,
		expiration: expiration,
	}
	b.orphans[*block.Hash()] = oBlock

	// Add to previous hash lookup index for faster dependency lookups.
	prevHash := &block.MsgBlock().Header.PrevBlock
	b.prevOrphans[*prevHash] = append(b.prevOrphans[*prevHash], oBlock)
}

// SequenceLock represents the converted relative lock-time in seconds, and
// absolute block-height for a transaction input's relative lock-times.
// According to SequenceLock, after the referenced input has been confirmed
// within a block, a transaction spending that input can be included into a
// block either after 'seconds' (according to past median time), or once the
// 'BlockHeight' has been reached.
type SequenceLock struct {
	Seconds     int64
	BlockHeight int32
}

// CalcSequenceLock computes a relative lock-time SequenceLock for the passed
// transaction using the passed UtxoViewpoint to obtain the past median time
// for blocks in which the referenced inputs of the transactions were included
// within. The generated SequenceLock lock can be used in conjunction with a
// block height, and adjusted median block time to determine if all the inputs
// referenced within a transaction have reached sufficient maturity allowing
// the candidate transaction to be included in a block.
//
// This function is safe for concurrent access.
func (b *BlockChain) CalcSequenceLock(tx *asiutil.Tx, utxoView *txo.UtxoViewpoint, mempool bool) (*SequenceLock, error) {
	b.chainLock.Lock()
	defer b.chainLock.Unlock()

	return b.calcSequenceLock(b.bestChain.Tip(), tx, utxoView, mempool)
}

// calcSequenceLock computes the relative lock-times for the passed
// transaction. See the exported version, CalcSequenceLock for further details.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) calcSequenceLock(node *blockNode, tx *asiutil.Tx, utxoView *txo.UtxoViewpoint, mempool bool) (*SequenceLock, error) {
	// A value of -1 for each relative lock type represents a relative time
	// lock value that will allow a transaction to be included in a block
	// at any given height or time. This value is returned as the relative
	// lock time in the case that BIP 68 is disabled, or has not yet been
	// activated.
	sequenceLock := &SequenceLock{Seconds: -1, BlockHeight: -1}

	// If the transaction's version is less than 2, and BIP 68 has not yet
	// been activated then sequence locks are disabled. Additionally,
	// sequence locks don't apply to coinbase transactions Therefore, we
	// return sequence lock values of -1 indicating that this transaction
	// can be included within a block at any given height or time.
	mTx := tx.MsgTx()
	if IsCoinBase(tx) {
		return sequenceLock, nil
	}

	// Grab the next height from the PoV of the passed blockNode to use for
	// inputs present in the mempool.
	nextHeight := node.height + 1

	for txInIndex, txIn := range mTx.TxIn {
		utxo := utxoView.LookupEntry(txIn.PreviousOutPoint)
		if utxo == nil {
			str := fmt.Sprintf("calcSequenceLock: output %v referenced from "+
				"transaction %s:%d either does not exist or "+
				"has already been spent", txIn.PreviousOutPoint,
				tx.Hash(), txInIndex)
			return sequenceLock, ruleError(ErrMissingTxOut, str)
		}

		// If the input height is set to the mempool height, then we
		// assume the transaction makes it into the next block when
		// evaluating its sequence blocks.
		inputHeight := utxo.BlockHeight()
		if inputHeight == 0x7fffffff {
			inputHeight = nextHeight
		}

		// Given a sequence number, we apply the relative time lock
		// mask in order to obtain the time lock delta required before
		// this input can be spent.
		sequenceNum := txIn.Sequence
		relativeLock := int64(sequenceNum & protos.SequenceLockTimeMask)

		switch {
		// Relative time locks are disabled for this input, so we can
		// skip any further calculation.
		case sequenceNum&protos.SequenceLockTimeDisabled == protos.SequenceLockTimeDisabled:
			continue
		case sequenceNum&protos.SequenceLockTimeIsSeconds == protos.SequenceLockTimeIsSeconds:
			// This input requires a relative time lock expressed
			// in seconds before it can be spent.  Therefore, we
			// need to query for the block prior to the one in
			// which this input was included within so we can
			// compute the past median time for the block prior to
			// the one which included this referenced output.
			prevInputHeight := inputHeight - 1
			if prevInputHeight < 0 {
				prevInputHeight = 0
			}
			blockNode := node.Ancestor(prevInputHeight)
			medianTime := blockNode.GetTime()

			// Time based relative time-locks as defined by BIP 68
			// have a time granularity of RelativeLockSeconds, so
			// we shift left by this amount to convert to the
			// proper relative time-lock. We also subtract one from
			// the relative lock to maintain the original lockTime
			// semantics.
			timeLockSeconds := (relativeLock << protos.SequenceLockTimeGranularity) - 1
			timeLock := medianTime + timeLockSeconds
			if timeLock > sequenceLock.Seconds {
				sequenceLock.Seconds = timeLock
			}
		default:
			// The relative lock-time for this input is expressed
			// in blocks so we calculate the relative offset from
			// the input's height as its converted absolute
			// lock-time. We subtract one from the relative lock in
			// order to maintain the original lockTime semantics.
			blockHeight := inputHeight + int32(relativeLock-1)
			if blockHeight > sequenceLock.BlockHeight {
				sequenceLock.BlockHeight = blockHeight
			}
		}
	}

	return sequenceLock, nil
}

// getReorganizeNodes finds the fork point between the main chain and the passed
// node and returns a list of block nodes that would need to be detached from
// the main chain and a list of block nodes that would need to be attached to
// the fork point (which will be the end of the main chain after detaching the
// returned list of block nodes) in order to reorganize the chain such that the
// passed node is the new end of the main chain.  The lists will be empty if the
// passed node is not on a side chain.
//
// This function may modify node statuses in the block index without flushing.
//
// This function MUST be called with the chain state lock held (for reads).
func (b *BlockChain) getReorganizeNodes(node *blockNode) (*list.List, *list.List) {
	attachNodes := list.New()
	detachNodes := list.New()

	// Do not reorganize to a known invalid chain. Ancestors deeper than the
	// direct parent are checked below but this is a quick check before doing
	// more unnecessary work.
	if b.index.NodeStatus(node.parent).KnownInvalid() {
		b.index.SetStatusFlags(node, statusInvalidAncestor)
		return detachNodes, attachNodes
	}

	// Find the fork point (if any) adding each block to the list of nodes
	// to attach to the main tree.  Push them onto the list in reverse order
	// so they are attached in the appropriate order when iterating the list
	// later.
	forkNode := b.bestChain.FindFork(node)
	invalidChain := false
	for n := node; n != nil && n != forkNode; n = n.parent {
		if b.index.NodeStatus(n).KnownInvalid() {
			invalidChain = true
			break
		}
		attachNodes.PushFront(n)
	}

	// If any of the node's ancestors are invalid, unwind attachNodes, marking
	// each one as invalid for future reference.
	if invalidChain {
		var next *list.Element
		for e := attachNodes.Front(); e != nil; e = next {
			next = e.Next()
			n := attachNodes.Remove(e).(*blockNode)
			b.index.SetStatusFlags(n, statusInvalidAncestor)
		}
		return detachNodes, attachNodes
	}

	// Start from the end of the main chain and work backwards until the
	// common ancestor adding each block to the list of nodes to detach from
	// the main chain.
	for n := b.bestChain.Tip(); n != nil && n != forkNode; n = n.parent {
		detachNodes.PushBack(n)
	}

	return detachNodes, attachNodes
}

// update fee lockitems in coinbase tx
func updateFeeLockItems(block *asiutil.Block, view *txo.UtxoViewpoint, feeLockItems map[protos.Asset]*txo.LockItem)  {
	coinbaseIdx := len(block.Transactions()) - 1
	coinbasetx := block.Transactions()[coinbaseIdx]
	prevOut := protos.OutPoint{Hash: *coinbasetx.Hash()}
	for txOutIdx, txOut := range coinbasetx.MsgTx().TxOut {
		if feeLockItems != nil && txOut.Value > 0 {
			prevOut.Index = uint32(txOutIdx)
			var lockItem *txo.LockItem
			if item, ok := feeLockItems[txOut.Asset]; ok {
				lockItem = item.Clone()
				for id, entry := range lockItem.Entries {
					if entry.Amount > txOut.Value {
						item.Entries[id].Amount = entry.Amount - txOut.Value
						entry.Amount = txOut.Value
					} else {
						delete(item.Entries, id)
					}
				}
			}
			view.AddTxOut(prevOut, txOut, true, block.Height(), lockItem)
		}
	}
}

// connectBlock handles connecting the passed node/block to the end of the main
// (best) chain.
//
// This passed utxo view must have all referenced txos the block spends marked
// as spent and all of the new txos the block creates added to it.  In addition,
// the passed stxos slice must be populated with all of the information for the
// spent txos.  This approach is used because the connection validation that
// must happen prior to calling this function requires the same details, so
// it would be inefficient to repeat it.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) connectBlock(node *blockNode, block *asiutil.Block,
	view *txo.UtxoViewpoint, stxos []txo.SpentTxOut, vblock *asiutil.VBlock,
	receipts types.Receipts, allLogs []*types.Log) error {

	// Make sure it's extending the end of the best chain.
	prevHash := &block.MsgBlock().Header.PrevBlock
	if !prevHash.IsEqual(&b.bestChain.Tip().hash) {
		return common.AssertError("connectBlock must be called with a block " +
			"that extends the main chain")
	}

	vtxSpentNum := countVtxSpentOutpus(vblock)
	spentNum := countSpentOutputs(block)
	if len(stxos) != spentNum + vtxSpentNum {
		return common.AssertError("connectBlock called with inconsistent " +
			"spent transaction out information")
	}

	// Write any block status changes to DB before updating best state.
	err := b.index.flushToDB()
	if err != nil {
		return err
	}

	// Generate a new best state snapshot that will be used to update the
	// database and later memory if all database updates are successful.
	b.stateLock.RLock()
	curTotalTxns := b.stateSnapshot.TotalTxns
	b.stateLock.RUnlock()
	numTxns := uint64(len(block.MsgBlock().Transactions))
	blockSize := uint64(block.MsgBlock().SerializeSize())
	state := newBestState(node, blockSize, numTxns,
		curTotalTxns+numTxns, block.MsgBlock().Header.Timestamp)

	// save receipts
	if receipts != nil {
		// batch := b.ethDB.NewBatch()
		rawdb.WriteReceipts(b.ethDB, *block.Hash(), uint64(block.Height()), receipts)
	}
	b.PostChainEvents(nil, allLogs)

	// Atomically insert info into the database.
	err = b.db.Update(func(dbTx database.Tx) error {
		// Update best block state.
		err := dbPutBestState(dbTx, state)
		if err != nil {
			return err
		}

		// Add the block hash and height to the block index which tracks
		// the main chain.
		err = dbPutBlockIndex(dbTx, block.Hash(), node.height)
		if err != nil {
			return err
		}

		// Update the utxo set using the state of the utxo view.  This
		// entails removing all of the utxos spent and adding the new
		// ones created by the block.
		err = dbPutUtxoView(dbTx, view)
		if err != nil {
			return err
		}

		// Update the balance using the state of the utxo view.
		err = dbPutBalance(dbTx, view)
		if err != nil {
			return err
		}

		err = dbStoreVBlock(dbTx, vblock)
		if err != nil {
			return err
		}

		err = dbPutLockItem(dbTx, view)
		if err != nil {
			return err
		}

		// Update the transaction spend journal by adding a record for
		// the block that contains all txos spent by it.
		err = dbPutSpendJournalEntry(dbTx, block.Hash(), stxos)
		if err != nil {
			return err
		}

		err = dbPutSignViewPoint(dbTx, block.Signs())
		if err != nil {
			return err
		}

		// Allow the index manager to call each of the currently active
		// optional indexes with the block being connected so they can
		// update themselves accordingly.
		if b.indexManager != nil {
			err := b.indexManager.ConnectBlock(dbTx, block, stxos, vblock)
			if err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	log.Infof("connectBlock success: height=%d, round=%d, slot=%d, hash=%s, stateRoot=%s",
		node.height, node.round.Round, node.slot, node.hash.String(), node.stateRoot.String())

	// Prune fully spent entries and mark all entries in the view unmodified
	// now that the modifications have been committed to the database.
	view.Commit()

	// This node is now the end of the best chain.
	b.bestChain.SetTip(node)
	b.BestSnapshot()

	// Update the state for the best block.  Notice how this replaces the
	// entire struct instead of updating the existing one.  This effectively
	// allows the old version to act as a snapshot which callers can use
	// freely without needing to hold a lock for the duration.  See the
	// comments on the state variable for more details.
	b.stateLock.Lock()
	b.stateSnapshot = state
	b.stateLock.Unlock()

	// Notify the caller that the block was connected to the main chain.
	// The caller would typically want to react with actions such as
	// updating wallets.
	b.chainLock.Unlock()
	data := []interface{}{block, vblock, node}
	b.sendNotification(NTBlockConnected, data)
	b.updateFees(block)
	b.chainLock.Lock()

	return nil
}

// disconnectBlock handles disconnecting the passed node/block from the end of
// the main (best) chain.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) disconnectBlock(node *blockNode, block *asiutil.Block, view *txo.UtxoViewpoint,
	vblock *asiutil.VBlock) error {

	// Make sure the node being disconnected is the end of the best chain.
	if !node.hash.IsEqual(&b.bestChain.Tip().hash) {
		errStr := fmt.Sprintf("disconnectBlock must be called with the " +
			"block at the end of the main chain")
		return ruleError(ErrHashMismatch, errStr)
	}

	// Load the previous block since some details for it are needed below.
	prevNode := node.parent
	var prevBlock *asiutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		prevBlock, err = dbFetchBlockByNode(dbTx, prevNode)
		return err
	})
	if err != nil {
		return err
	}

	// Write any block status changes to DB before updating best state.
	err = b.index.flushToDB()
	if err != nil {
		return err
	}

	// Generate a new best state snapshot that will be used to update the
	// database and later memory if all database updates are successful.
	b.stateLock.RLock()
	curTotalTxns := b.stateSnapshot.TotalTxns
	b.stateLock.RUnlock()
	numTxns := uint64(len(prevBlock.MsgBlock().Transactions))
	blockSize := uint64(prevBlock.MsgBlock().SerializeSize())
	newTotalTxns := curTotalTxns - uint64(len(block.MsgBlock().Transactions))
	state := newBestState(prevNode, blockSize, numTxns,
		newTotalTxns, prevNode.GetTime())

	err = b.db.Update(func(dbTx database.Tx) error {
		// Update best block state.
		err := dbPutBestState(dbTx, state)
		if err != nil {
			return err
		}

		// Remove the block hash and height from the block index which
		// tracks the main chain.
		err = dbRemoveBlockIndex(dbTx, block.Hash(), node.height)
		if err != nil {
			return err
		}

		// Update the utxo set using the state of the utxo view.  This
		// entails restoring all of the utxos spent and removing the new
		// ones created by the block.
		err = dbPutUtxoView(dbTx, view)
		if err != nil {
			return err
		}

		//Update the balance using the state of the utxo view.
		//
		err = dbPutBalance(dbTx, view)
		if err != nil {
			return err
		}

		//remove signature.
		for _, sign := range block.Signs() {
			err = dbRemoveSignature(dbTx, sign.Hash())
			if err != nil {
				return err
			}
		}

		// Before we delete the spend journal entry for this back,
		// we'll fetch it as is so the indexers can utilize if needed.
		stxos, err := dbFetchSpendJournalEntry(dbTx, block, vblock)
		if err != nil {
			return err
		}

		// Update the transaction spend journal by removing the record
		// that contains all txos spent by the block.
		err = dbRemoveSpendJournalEntry(dbTx, block.Hash())
		if err != nil {
			return err
		}

		// Allow the index manager to call each of the currently active
		// optional indexes with the block being disconnected so they
		// can update themselves accordingly.
		if b.indexManager != nil {
			err := b.indexManager.DisconnectBlock(dbTx, block, stxos, vblock)
			if err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	log.Infof("disconnectBlock success: height=%d,round=%d,slot=%d,hash=%s,stateRoot=%s",
		node.height, node.round.Round, node.slot, node.hash.String(), node.stateRoot.String())
	// Prune fully spent entries and mark all entries in the view unmodified
	// now that the modifications have been committed to the database.
	view.Commit()

	// This node's parent is now the end of the best chain.
	b.bestChain.SetTip(node.parent)

	// Update the state for the best block.  Notice how this replaces the
	// entire struct instead of updating the existing one.  This effectively
	// allows the old version to act as a snapshot which callers can use
	// freely without needing to hold a lock for the duration.  See the
	// comments on the state variable for more details.
	b.stateLock.Lock()
	b.stateSnapshot = state
	b.stateLock.Unlock()

	// Notify the caller that the block was disconnected from the main
	// chain.  The caller would typically want to react with actions such as
	// updating wallets.
	b.chainLock.Unlock()
	b.sendNotification(NTBlockDisconnected, []interface{}{block, vblock, node.parent})
	b.chainLock.Lock()

	return nil
}

// countSpentOutputs returns the number of utxos the passed block spends.
func countSpentOutputs(block *asiutil.Block) int {
	// Exclude the coinbase transaction since it can't spend anything.
	var numSpent int
	trans := block.Transactions()
	if len(trans) < 1 {
		return 0
	}

	coinbaseIdx := len(trans) - 1
	for _, tx := range trans[:coinbaseIdx] {
		numSpent += len(tx.MsgTx().TxIn)
	}
	return numSpent
}

// countVtxSpentOutpus calcs number of TxIn
func countVtxSpentOutpus(vblock *asiutil.VBlock) int {
	if vblock == nil {
		return 0
	}
	numSpent := 0
	for _, tx := range vblock.MsgVBlock().VTransactions {
		for _, in := range tx.TxIn {
			if asiutil.IsMintOrCreateInput(in) {
				continue
			}

			numSpent++
		}
	}

	return numSpent
}

// reorganizeChain reorganizes the block chain by disconnecting the nodes in the
// detachNodes list and connecting the nodes in the attach list.  It expects
// that the lists are already in the correct order and are in sync with the
// end of the current best chain.  Specifically, nodes that are being
// disconnected must be in reverse order (think of popping them off the end of
// the chain) and nodes the are being attached must be in forwards order
// (think pushing them onto the end of the chain).
//
// This function may modify node statuses in the block index without flushing.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) reorganizeChain(detachNodes, attachNodes *list.List) error {

	// Nothing to do if no reorganize nodes were provided.
	if detachNodes.Len() == 0 && attachNodes.Len() == 0 {
		return nil
	}

	// Ensure the provided nodes match the current best chain.
	tip := b.bestChain.Tip()

	if tip.height == 0 {
		return ruleError(ErrHeightMismatch, "reorganizeChain: genesis block can not be detached")
	}

	if detachNodes.Len() != 0 {
		firstDetachNode := detachNodes.Front().Value.(*blockNode)
		if firstDetachNode.hash != tip.hash {
			errStr := fmt.Sprintf("reorganize nodes to detach are "+
				"not for the current best chain -- first detach node %v, "+
				"current chain %v", &firstDetachNode.hash, &tip.hash)
			return ruleError(ErrHashMismatch, errStr)
		}
	}

	// Ensure the provided nodes are for the same fork point.
	if attachNodes.Len() != 0 && detachNodes.Len() != 0 {
		firstAttachNode := attachNodes.Front().Value.(*blockNode)
		lastDetachNode := detachNodes.Back().Value.(*blockNode)
		if firstAttachNode.parent.hash != lastDetachNode.parent.hash {
			errStr := fmt.Sprintf("reorganize nodes do not have the "+
				"same fork point -- first attach parent %v, last detach "+
				"parent %v", &firstAttachNode.parent.hash,
				&lastDetachNode.parent.hash)
			return ruleError(ErrHashMismatch, errStr)
		}
	}

	// Track the old and new best chains heads.
	oldBest := tip
	newBest := tip

	// All of the blocks to detach and related spend journal entries needed
	// to unspend transaction outputs in the blocks fbeing disconnected must
	// then they are needed again when doing the actual database updates.
	// Rather than doing two loads, cache the loaded data into these slices.
	detachBlocks := make([]*asiutil.Block, 0, detachNodes.Len())
	detachSpentTxOuts := make([][]txo.SpentTxOut, 0, detachNodes.Len())
	detachVBlocks := make([]*asiutil.VBlock, 0, detachNodes.Len())
	attachBlocks := make([]*asiutil.Block, 0, attachNodes.Len())
	attachVBlocks := make([]*asiutil.VBlock, 0, attachNodes.Len())
	attachReceipts := make([]types.Receipts, attachNodes.Len())
	attachLogs := make([][]*types.Log, attachNodes.Len())

	// Disconnect all of the blocks back to the point of the fork.  This
	// entails loading the blocks and their associated spent txos from the
	// database and using that information to unspend all of the spent txos
	// and remove the utxos created by the blocks.
	view := txo.NewUtxoViewpoint()
	view.SetBestHash(&oldBest.hash)
	for e := detachNodes.Front(); e != nil; e = e.Next() {
		n := e.Value.(*blockNode)

		block, vblock, err := asiutil.GetBlockPair(b.db, &n.hash)
		if err != nil {
			return err
		}
		if n.hash != *block.Hash() {
			errStr := fmt.Sprintf("detach block node hash %v (height "+
				"%v) does not match previous parent block hash %v", &n.hash,
				n.height, block.Hash())
			return ruleError(ErrHashMismatch, errStr)
		}

		// Load all of the utxos referenced by the block that aren't
		// already in the view.
		err = fetchInputUtxos(view, b.db, block)
		if err != nil {
			return err
		}

		// Load all of the spent txos for the block from the spend
		// journal.
		var stxos []txo.SpentTxOut
		err = b.db.View(func(dbTx database.Tx) error {
			stxos, err = dbFetchSpendJournalEntry(dbTx, block, vblock)
			return err
		})
		if err != nil {
			return err
		}

		// Store the loaded block and spend journal entry for later.
		detachBlocks = append(detachBlocks, block)
		detachSpentTxOuts = append(detachSpentTxOuts, stxos)
		detachVBlocks = append(detachVBlocks, vblock)

		err = disconnectTransactions(view, b.db, block, stxos, vblock)
		if err != nil {
			return err
		}
		b.contractManager.DisconnectBlock(block)

		newBest = n.parent
	}

	// Set the fork point only if there are nodes to attach since otherwise
	// blocks are only being disconnected and thus there is no fork point.
	var forkNode *blockNode
	if attachNodes.Len() > 0 {
		forkNode = newBest
	}

	// Perform several checks to verify each block that needs to be attached
	// to the main chain can be connected without violating any rules and
	// without actually connecting the block.
	//
	// NOTE: These checks could be done directly when connecting a block,
	// however the downside to that approach is that if any of these checks
	// fail after disconnecting some blocks or attaching others, all of the
	// operations have to be rolled back to get the chain back into the
	// state it was before the rule violation (or other failure).  There are
	// at least a couple of ways accomplish that rollback, but both involve
	// tweaking the chain and/or database.  This approach catches these
	// issues before ever modifying the chain.
	for i, e := 0, attachNodes.Front(); e != nil; i, e = i+1, e.Next() {
		n := e.Value.(*blockNode)
		block, vblock, err := asiutil.GetBlockPair(b.db, &n.hash)
		if err != nil {
			if _, ok := err.(asiutil.MissVBlockError); !ok {
				return err
			}
		}

		// Store the loaded block for later.
		attachBlocks = append(attachBlocks, block)

		// Skip checks if node has already been fully validated. Although
		// checkConnectBlock gets skipped, we still need to update the UTXO
		// view.
		if b.index.NodeStatus(n).KnownValid() && vblock != nil {
			err := fetchInputUtxos(view, b.db, block)
			if err != nil {
				return err
			}
			err = connectTransactions(view, b.db, block, vblock, nil)
			if err != nil {
				return err
			}
			for _, tx := range block.Transactions() {
				view.AddViewTx(tx.Hash(), tx.MsgTx())
			}

			newBest = n
			attachVBlocks = append(attachVBlocks, vblock)
			continue
		}

		// Notice the spent txout details are not requested here and
		// thus will not be generated.  This is done because the state
		// is not being immediately written to the database, so it is
		// not needed.
		//
		// In the case the block is determined to be invalid due to a
		// rule violation, mark it as invalid and mark all of its
		// descendants as having an invalid ancestor.
		var msgvblock protos.MsgVBlock
		receipts, allLogs, err := b.checkConnectBlock(n, block, view, nil, &msgvblock)
		if err != nil {
			if _, ok := err.(RuleError); ok {
				b.index.SetStatusFlags(n, statusValidateFailed)
				for de := e.Next(); de != nil; de = de.Next() {
					dn := de.Value.(*blockNode)
					b.index.SetStatusFlags(dn, statusInvalidAncestor)
				}
			}
			return err
		}
		b.index.SetStatusFlags(n, statusValid)

		attachVBlocks = append(attachVBlocks, asiutil.NewVBlock(&msgvblock, block.Hash()))
		attachReceipts[i]  = receipts
		attachLogs[i]      = allLogs

		newBest = n
	}

	// Reset the view for the actual connection code below.  This is
	// required because the view was previously modified when checking if
	// the reorg would be successful and the connection code requires the
	// view to be valid from the viewpoint of each block being connected or
	// disconnected.
	view = txo.NewUtxoViewpoint()
	view.SetBestHash(&b.bestChain.Tip().hash)
	// Disconnect blocks from the main chain.
	for i, e := 0, detachNodes.Front(); e != nil; i, e = i+1, e.Next() {
		n := e.Value.(*blockNode)
		block := detachBlocks[i]
		vblock := detachVBlocks[i]

		// Load all of the utxos referenced by the block that aren't
		// already in the view.
		err := fetchInputUtxos(view, b.db, block)
		if err != nil {
			return err
		}

		// Update the view to unspend all of the spent txos and remove
		// the utxos created by the block.
		err = disconnectTransactions(view, b.db, block, detachSpentTxOuts[i], vblock)
		if err != nil {
			return err
		}

		// Update the database and chain state.
		err = b.disconnectBlock(n, block, view, vblock)
		if err != nil {
			return err
		}
	}

	var block *asiutil.Block
	var vblock *asiutil.VBlock
	// Connect the new best chain blocks.
	for i, e := 0, attachNodes.Front(); e != nil; i, e = i+1, e.Next() {
		n := e.Value.(*blockNode)
		block = attachBlocks[i]
		vblock = attachVBlocks[i]

		// Load all of the utxos referenced by the block that aren't
		// already in the view.
		err := fetchInputUtxos(view, b.db, block)
		if err != nil {
			return err
		}

		// Update the view to mark all utxos referenced by the block
		// as spent and add all transactions being created by this block
		// to it.  Also, provide an stxo slice so the spent txout
		// details are generated.
		stxos := make([]txo.SpentTxOut, 0, countSpentOutputs(block) + countVtxSpentOutpus(vblock))

		err = connectTransactions(view, b.db, block, vblock, &stxos)
		if err != nil {
			return err
		}

		// Update the database and chain state.
		err = b.connectBlock(n, block, view, stxos, vblock,
			attachReceipts[i], attachLogs[i])
		if err != nil {
			return err
		}
	}

	if block != nil {
		b.updateFees(block)
	}

	// Log the point where the chain forked and old and new best chain
	// heads.
	if forkNode != nil {
		log.Infof("REORGANIZE: Chain forks at %v (height %v)", forkNode.hash,
			forkNode.height)
	}
	log.Infof("REORGANIZE: Old best chain head was %v (height %v)",
		&oldBest.hash, oldBest.height)
	log.Infof("REORGANIZE: New best chain head is %v (height %v)",
		newBest.hash, newBest.height)

	return nil
}

func (b *BlockChain) sendFee(fees map[protos.Asset]int32) {
	b.feesChan <- fees
}

func (b *BlockChain) updateFees(block *asiutil.Block) {
	stateDB, err := state.New(block.MsgBlock().Header.StateRoot, b.stateCache)
	fees, err := b.contractManager.GetFees(block,
		stateDB, chaincfg.ActiveNetParams.FvmParam)
	if err != nil {
		log.Errorf("handleBlockPersistCompleted GetFees error:", err)
		return
	}
	fees[asiutil.AsimovAsset] = 0

	select {
	case <-b.feesChan:
	default:
	}
	go b.sendFee(fees)
}

// connectBestChain handles connecting the passed block to the chain while
// respecting proper chain selection according to the chain with the most
// proof of work.  In the typical case, the new block simply extends the main
// chain.  However, it may also be extending (or creating) a side chain (fork)
// which may or may not end up becoming the main chain depending on which fork
// cumulatively has the most proof of work.  It returns whether or not the block
// ended up on the main chain (either due to extending the main chain or causing
// a reorganization to become the main chain).
//
// The flags modify the behavior of this function as follows:
//  - BFFastAdd: Avoids several expensive transaction validation operations.
//    This is useful when using checkpoints.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) connectBestChain(node *blockNode, block *asiutil.Block, vblock *asiutil.VBlock,
	receipts types.Receipts, logs []*types.Log,
	flags common.BehaviorFlags) (bool, error) {
	fastAdd := flags&common.BFFastAdd == common.BFFastAdd
	flushIndexState := func() {
		// Intentionally ignore errors writing updated node status to DB. If
		// it fails to write, it's not the end of the world. If the block is
		// valid, we flush in connectBlock and if the block is invalid, the
		// worst that can happen is we revalidate the block after a restart.
		if writeErr := b.index.flushToDB(); writeErr != nil {
			log.Warnf("Error flushing block index changes to disk: %v",
				writeErr)
		}
	}
	// We are extending the main (best) chain with a new block.  This is the
	// most common case.
	parentHash := &block.MsgBlock().Header.PrevBlock
	if parentHash.IsEqual(&b.bestChain.Tip().hash) {
		// Skip checks if node has already been fully validated.
		fastAdd = (fastAdd || b.index.NodeStatus(node).KnownValid()) && vblock != nil

		pSlot := b.bestChain.Tip().slot
		pRound := b.bestChain.Tip().round.Round
		curHeader := block.MsgBlock().Header
		if (chaincfg.Cfg.EmptyRound && pRound > curHeader.Round) ||
			(!chaincfg.Cfg.EmptyRound && pRound+1 != curHeader.Round && pRound != curHeader.Round) ||
			(pSlot >= curHeader.SlotIndex && pRound == curHeader.Round) {
			str := fmt.Sprintf("connectBestChain: block has old slot/round than parent: slot:%d/%d, round:%d/%d",
				pSlot, curHeader.SlotIndex, pRound, curHeader.Round)
			return false, ruleError(ErrBadSlotOrRound, str)
		}

		// Perform several checks to verify the block can be connected
		// to the main chain without violating any rules and without
		// actually connecting the block.
		view := txo.NewUtxoViewpoint()
		view.SetBestHash(parentHash)
		stxos := make([]txo.SpentTxOut, 0, countSpentOutputs(block) + countVtxSpentOutpus(vblock))

		var err error
		if !fastAdd {
			var msgvblock protos.MsgVBlock
			receipts, logs, err = b.checkConnectBlock(node, block, view, &stxos, &msgvblock)
			if err == nil {
				b.index.SetStatusFlags(node, statusValid)
			} else if _, ok := err.(RuleError); ok {
				b.index.SetStatusFlags(node, statusValidateFailed)
			} else {
				return false, err
			}
			flushIndexState()

			if err != nil {
				return false, err
			}

			vblock = asiutil.NewVBlock(&msgvblock, block.Hash())
		}

		// In the fast add case the code to check the block connection
		// was skipped, so the utxo view needs to load the referenced
		// utxos, spend them, and add the new utxos being created by
		// this block.
		if fastAdd {
			err := fetchInputUtxos(view, b.db, block)
			if err != nil {
				return false, err
			}
			err = connectTransactions(view, b.db, block, vblock, &stxos)
			if err != nil {
				return false, err
			}
			for _, tx := range block.Transactions() {
				view.AddViewTx(tx.Hash(), tx.MsgTx())
			}
		}

		// Connect the block to the main chain.
		err = b.connectBlock(node, block, view, stxos, vblock, receipts, logs)
		if err != nil {
			// If we got hit with a rule error, then we'll mark
			// that status of the block as invalid and flush the
			// index state to disk before returning with the error.
			if _, ok := err.(RuleError); ok {
				b.index.SetStatusFlags(
					node, statusValidateFailed,
				)
			}
			flushIndexState()

			return false, err
		}

		// If this is fast add, or this block node isn't yet marked as
		// valid, then we'll update its status and flush the state to
		// disk again.
		if fastAdd || !b.index.NodeStatus(node).KnownValid() {
			b.index.SetStatusFlags(node, statusValid)
			flushIndexState()
		}
		return true, nil
	}

	needReorg := node.weight > b.bestChain.tip().weight
	// We're extending (or creating) a side chain, but the cumulative
	// work for this new side chain is not enough to make it the new chain.
	if !needReorg {
		// Log information about how the block is forking the chain.
		fork := b.bestChain.FindFork(node)
		if fork.hash.IsEqual(parentHash) {
			log.Infof("FORK: Block %v forks the chain at height %d"+
				"/block %v, but does not cause a reorganize",
				node.hash, fork.height, fork.hash)
		} else {
			log.Infof("EXTEND FORK: Block %v extends a side chain "+
				"which forks the chain at height %d/block %v",
				node.hash, fork.height, fork.hash)
		}

		return false, nil
	}

	// We're extending (or creating) a side chain and the cumulative work
	// for this new side chain is more than the old best chain, so this side
	// chain needs to become the main chain.  In order to accomplish that,
	// find the common ancestor of both sides of the fork, disconnect the
	// blocks that form the (now) old fork from the main chain, and attach
	// the blocks that form the new chain to the main chain starting at the
	// common ancenstor (the point where the chain forked).
	detachNodes, attachNodes := b.getReorganizeNodes(node)

	// Reorganize the chain.
	log.Infof("REORGANIZE: Block %v is causing a reorganize.", node.hash)
	err := b.reorganizeChain(detachNodes, attachNodes)

	// Either getReorganizeNodes or reorganizeChain could have made unsaved
	// changes to the block index, so flush regardless of whether there was an
	// error. The index would only be dirty if the block failed to connect, so
	// we can ignore any errors writing.
	if writeErr := b.index.flushToDB(); writeErr != nil {
		log.Warnf("Error flushing block index changes to disk: %v", writeErr)
	}

	return err == nil, err
}

// ConnectTransaction updates the view by adding all new utxos created by the
// passed transaction and marking all utxos that the transactions spend as
// spent.  In addition, when the 'stxos' argument is not nil, it will be updated
// to append an entry for each spent txout.  An error will be returned if the
// view does not contain the required utxos.
func (b *BlockChain) ConnectTransaction(block *asiutil.Block, txidx int, view *txo.UtxoViewpoint, tx *asiutil.Tx,
	stxos *[]txo.SpentTxOut, stateDB *state.StateDB, fee int64) (
	receipt *types.Receipt, err error, gasUsed uint64, vtx *protos.MsgTx, feeLockItems map[protos.Asset]*txo.LockItem) {

	scriptClass := txscript.NonStandardTy
	txbaseGas := uint64(tx.MsgTx().SerializeSize() * common.GasPerByte)
	coinbase := IsCoinBase(tx)
	gas := uint64(tx.MsgTx().TxContract.GasLimit)
	leftOverGas := gas - txbaseGas
	snapshot := -1
	executeVMFailed := false
	contractAddr := common.Address{}
	var callerAddr common.Address
	var vmtx *virtualtx.VirtualTransaction

	defer func() {
		view.AddViewTx(tx.Hash(), tx.MsgTx())
		gasUsed = gas - leftOverGas
		if err != nil {
			return
		}
		if vmtx != nil && !executeVMFailed {
			vtx, leftOverGas, err = b.handleVTX(vmtx, block, view, stateDB, stxos, txidx, tx, leftOverGas)
			gasUsed = gas - leftOverGas
			if err != nil {
				if coinbase {
					return
				}
				if snapshot >= 0 {
					stateDB.RevertToSnapshot(snapshot)
					log.Error("handle vtx revert", block.Height(), txidx, snapshot)
				}
				err = nil
				executeVMFailed = true
			}
		}
		if coinbase || (scriptClass >= txscript.CreateTy || scriptClass <= txscript.VoteTy) {
			root := stateDB.IntermediateRoot(false).Bytes()
			// Create a new receipt for the transaction, storing the intermediate root and gas used by the tx
			// based on the eip phase, we're passing weather the root touch-delete accounts.
			receipt = types.NewReceipt(root, executeVMFailed, 0)
			receipt.TxHash = *tx.Hash()
			// if the transaction created a contract, store the creation address in the receipt.
			if scriptClass == txscript.CreateTy && contractAddr.Big().Int64() != 0 {
				receipt.ContractAddress = contractAddr
			}
			// Set the receipt logs and create a bloom for filtering
			receipt.Logs = stateDB.GetLogs(*tx.Hash())
			receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
			receipt.GasUsed = gasUsed
		}
		if executeVMFailed {
			vtx, err = b.connectContractRollback(view, block.Height(), stxos, txidx, tx)
		}
		if vtx != nil && len(vtx.TxIn) == 0 && len(vtx.TxOut) == 0 {
			vtx = nil
		}
	}()
	if coinbase {
		vmtx, err, snapshot = b.connectCoinbaseTX(block, view, tx, stxos, stateDB, fee)
		leftOverGas = 0
		feeLockItems = view.AddTxOuts(tx.Hash(), tx.MsgTx(), true, block.Height())
		return
	}

	// Spend the referenced utxos by marking them spent in the view and,
	// if a slice was provided for the spent txout details, append an entry
	// to it.
	for i, txIn := range tx.MsgTx().TxIn {
		// Ensure the referenced utxo exists in the view.  This should
		// never happen unless there is a bug is introduced in the code.
		entry := view.LookupEntry(txIn.PreviousOutPoint)
		if entry == nil {
			str := fmt.Sprintf("view missing input: %v",
				txIn.PreviousOutPoint)
			return nil, ruleError(ErrMissingTxOut, str), 0, nil, nil
		}

		// Only create the stxo details if requested.
		if stxos != nil {
			// Populate the stxo details using the utxo entry.
			var stxo = txo.SpentTxOut{
				Amount:     entry.Amount(),
				PkScript:   entry.PkScript(),
				Height:     entry.BlockHeight(),
				IsCoinBase: entry.IsCoinBase(),
				Asset:      entry.Asset(),
			}
			*stxos = append(*stxos, stxo)
		}

		// Mark the entry as spent.  This is not done until after the
		// relevant details have been accessed since spending it might
		// clear the fields from memory in the future.
		entry.Spend()

		//get the first input address as the contract caller.
		if i == 0 && tx.Type() == asiutil.TxTypeNormal {
			scriptClass, addrs, requiredSigs, errt := txscript.ExtractPkScriptAddrs(entry.PkScript())
			if errt != nil || len(addrs) <= 0 {
				log.Critical("extra caller addr error,", scriptClass, addrs, requiredSigs, errt)
				str := fmt.Sprintf("view missing input address %v", entry.PkScript())
				err = ruleError(ErrInvalidPkScript, str)
				return
			}

			callerAddr = addrs[0].StandardAddress()
		}
	}

	// Add the transaction's outputs as available utxos.
	feeLockItems = view.AddTxOuts(tx.Hash(), tx.MsgTx(), false, block.Height())

	b.updateBalanceCache(block, view, tx)

	if len(tx.MsgTx().TxOut) == 0 {
		return
	}

	// check smart contract call.
	txOut := tx.MsgTx().TxOut[0]
	var addrs []common.IAddress
	scriptClass, addrs, _, err = txscript.ExtractPkScriptAddrs(txOut.PkScript)
	if err != nil || (scriptClass != txscript.CreateTy && len(addrs) <= 0) {
		log.Error("get contract addr error,", scriptClass, addrs, err)
		str := fmt.Sprintf("invalid pkscript tx=%v, index=%d", tx.Hash(), tx.Index())
		err = ruleError(ErrInvalidPkScript, str)
		return
	}

	err = b.checkAssetForbidden(block, stateDB, tx, view, scriptClass)
	if err != nil {
		log.Info(err)
		return
	}

	if scriptClass < txscript.CreateTy || scriptClass > txscript.VoteTy {
		return
	}
	if scriptClass != txscript.CreateTy {
		contractAddr = addrs[0].StandardAddress()
	}

	vmtx, err, leftOverGas, contractAddr, snapshot = b.connectContract(block, view, stateDB, callerAddr, contractAddr, txOut, stxos, tx, scriptClass, leftOverGas, nil, fee)

	if err != nil {
		log.Info("handle vm excute error", err)
		err = nil
		executeVMFailed = true
	} else if txOut.Value > 0 {
		if scriptClass == txscript.CreateTy || scriptClass == txscript.TemplateTy {
			// modify the utxo owner to the new contract address.
			prevout := protos.NewOutPoint(tx.Hash(), 0)
			utxo := view.LookupEntry(*prevout)
			if utxo != nil {
				pkScript, err1 := txscript.PayToAddrScript(&contractAddr)
				if err1 != nil {
					err = err1
					return
				}
				utxo.SetPkScript(pkScript)
			}
			log.Info("CREATE TX REPLACE ADDRESS ", contractAddr.String())
		}
	}

	return
}

// generate, check, and connect to block for the virtual tx.
func (b *BlockChain) handleVTX(vmtx *virtualtx.VirtualTransaction,
	block *asiutil.Block,
	view *txo.UtxoViewpoint,
	stateDB *state.StateDB,
	stxos *[]txo.SpentTxOut,
	txidx int,
	tx *asiutil.Tx,
	gas uint64,
) (msgTx *protos.MsgTx, leftOverGas uint64, err error) {
	leftOverGas, err = b.CheckTransferValidate(stateDB, vmtx, block, gas)
	if err != nil {
		return
	}

	msgTx, err = b.GenVtxMsg(block, view, tx, vmtx)
	if err != nil {
		log.Error(err)
		return
	}

	if msgTx != nil && len(msgTx.TxOut) > 0 && len(msgTx.TxIn) > 0 {
		tx := asiutil.NewTx(msgTx)
		err = CheckTransactionSanity(tx)
		if err != nil {
			return
		}

		err = CheckVTransactionInputs(tx, view)
		if err != nil {
			return
		}

		// reuse version for normal tx index.
		msgTx.Version = uint32(txidx)
		err = b.connectVTX(view, tx, block.Height(), stxos)
		if err != nil {
			log.Error("connectVTX vtx msg error.")
			return
		}

		b.updateBalanceCache(block, view, tx)
	} else {
		msgTx = nil
	}
	return
}

// round consensus special tx created by validator to flush award info to consensus contract.
func (b *BlockChain) connectCoinbaseTX(block *asiutil.Block,
	view *txo.UtxoViewpoint,
	tx *asiutil.Tx, stxos *[]txo.SpentTxOut,
	db *state.StateDB,
	fee int64) (vtx *virtualtx.VirtualTransaction, err error, snapshot int) {

	gas := uint64(math.MaxUint64)
	poaAddr := chaincfg.OfficialAddress
	snapshot = 0
	var scriptClass txscript.ScriptClass
	var addrs []common.IAddress
	tempsnapshot := 0
	for _, txOut := range tx.MsgTx().TxOut {
		scriptClass, addrs, _, err = txscript.ExtractPkScriptAddrs(txOut.PkScript)
		if err != nil || len(addrs) <= 0 {
			log.Error("get contract addr error in coinbase tx", scriptClass, addrs, err)
			str := fmt.Sprintf("no address in coinbase tx pkscript %v", txOut.PkScript)
			err = ruleError(ErrInvalidPkScript, str)
			return
		}
		if scriptClass == txscript.CallTy && len(txOut.Data) > 0 {
			vtx, err, _, _, tempsnapshot = b.connectContract(block, view, db, poaAddr, addrs[0].StandardAddress(), txOut,
				stxos, tx, scriptClass, gas, vtx, fee)
			// coinbase tx is not allowed be failed
			if err != nil {
				str := fmt.Sprintf("coinbase tx call contract failed %v", err)
				log.Error(str)
				err = ruleError(ErrBadCoinbaseData, str)
				return
			}
			if snapshot == 0 && tempsnapshot > 0 {
				snapshot = tempsnapshot
			}
		}
	}

	return
}

// connect virtual transaction to block.
func (b *BlockChain) connectVTX(view *txo.UtxoViewpoint, vtx *asiutil.Tx, height int32, stxos *[]txo.SpentTxOut) error {
	// Spend the referenced utxos by marking them spent in the view and,
	// if a slice was provided for the spent txout details, append an entry
	// to it.
	// check first.
	for _, txIn := range vtx.MsgTx().TxIn {
		// Ensure the referenced utxo exists in the view.  This should
		// never happen unless there is a bug is introduced in the code.
		isVtxMint := asiutil.IsMintOrCreateInput(txIn)
		if isVtxMint {
			continue
		}

		entry := view.LookupEntry(txIn.PreviousOutPoint)
		if entry == nil || entry.IsSpent() {
			str := fmt.Sprintf("the entry of input PreviousOutPoint is nil or entry has been spent %v", txIn.PreviousOutPoint)
			return ruleError(ErrMissingTxOut, str)
		}
	}

	for _, txIn := range vtx.MsgTx().TxIn {
		// Ensure the referenced utxo exists in the view.  This should
		// never happen unless there is a bug is introduced in the code.
		isVtxMint := asiutil.IsMintOrCreateInput(txIn)
		if isVtxMint {
			log.Info("mint asset.!!!")
			continue
		}
		entry := view.LookupEntry(txIn.PreviousOutPoint)
		// Only create the stxo details if requested.
		if stxos != nil {
			// Populate the stxo details using the utxo entry.
			var stxo = txo.SpentTxOut{
				Amount:     entry.Amount(),
				PkScript:   entry.PkScript(),
				Height:     entry.BlockHeight(),
				IsCoinBase: entry.IsCoinBase(),
				Asset:      entry.Asset(),
			}
			*stxos = append(*stxos, stxo)
		}

		// Mark the entry as spent.  This is not done until after the
		// relevant details have been accessed since spending it might
		// clear the fields from memory in the future.
		entry.Spend()
	}

	// Add the transaction's outputs as available utxos.
	view.AddTxOuts(vtx.Hash(), vtx.MsgTx(), false, height)
	return nil
}

// execute contract in the tx.
func (b *BlockChain) connectContract(
	block *asiutil.Block,
	view *txo.UtxoViewpoint,
	stateDB *state.StateDB,
	caller, targetContractAddr common.Address,
	out *protos.TxOut,
	stxos *[]txo.SpentTxOut,
	tx *asiutil.Tx,
	contractCode txscript.ScriptClass,
	gas uint64,
	vtx *virtualtx.VirtualTransaction,
	fee int64) (
	vtxr *virtualtx.VirtualTransaction, err error, leftOverGas uint64, newContractAddr common.Address, snapshot int) {

	log.Debug("connectContract enter", contractCode)
	defer func() {
		log.Debug("connectContract exit", contractCode, err)
	}()

	var voteValue func() int64
	if contractCode == txscript.VoteTy {
		voteValue = func() int64 {
			id := txo.PickVoteArgument(out.Data)
			voteId := txo.NewVoteId(targetContractAddr, id)
			ret := getVoteValue(view, tx, voteId, &out.Asset)
			log.Infof("DEBUG LOG NIL CRASH block vote", tx.Hash().String(), ret)
			return ret
		}
	}

	leftOverGas = gas
	// in order to retention accuracy, mul 10000 for contract to check
	gasPrice := fee*10000/int64(tx.MsgTx().TxContract.GasLimit)
	context := fvm.NewFVMContext(caller, new(big.Int).SetInt64(gasPrice), block, b, view, voteValue)
	vmenv := vm.NewFVMWithVtx(context, stateDB, chaincfg.ActiveNetParams.FvmParam, *b.GetVmConfig(), vtx)
	var ret []byte
	switch contractCode {
	case txscript.VoteTy:
		fallthrough
	case txscript.CallTy:
		ret,leftOverGas, snapshot, err = b.executeContract(vmenv, caller, targetContractAddr, out.Data, &out.Asset, out.Value, leftOverGas)
		if err != nil {
			if err.Error() == errExecutionRevertedString{
				cache.PutExecuteError(tx.Hash().String(),common.Bytes2Hex(ret))
			}
			return
		}
	case txscript.TemplateTy:
		leftOverGas, snapshot, err = b.commitTemplateContract(vmenv, caller, tx, out, leftOverGas)
		newContractAddr = targetContractAddr
		if err != nil {
			return
		}
	case txscript.CreateTy:
		newContractAddr, leftOverGas, snapshot, err = b.createContract(vmenv, caller, view, block, stateDB, tx, out, leftOverGas)
		if err != nil {
			return
		}
	}
	vtxr = vmenv.Vtx
	return
}

// failed to call/create evm, roll back this out utxo, give it back to the sender.
func (b *BlockChain) connectContractRollback(
	view *txo.UtxoViewpoint,
	height int32,
	stxos *[]txo.SpentTxOut,
	txIndex int,
	tx *asiutil.Tx) (*protos.MsgTx, error) {

	msgTx, err := b.GenRollbackMsg(view, tx)
	if err != nil {
		log.Error("GenRollbackMsg vtx msg error.")
		return nil, err
	}

	msgTx.Version = uint32(txIndex)
	b.connectVTX(view, asiutil.NewTx(msgTx), height, stxos)
	return msgTx, nil
}

// call vm to exe contract.
func (b *BlockChain) executeContract(
	vmenv *vm.FVM,
	callerAddr, contractAddr common.Address,
	data []byte, asset *protos.Asset,
	value int64,
	gasLimit uint64) (ret []byte,leftOverGas uint64, snapshot int, err error) {
	t1 := time.Now()

	ret,leftOverGas, snapshot, err = vmenv.Call(vm.AccountRef(callerAddr), contractAddr, data, gasLimit, big.NewInt(value), asset, false)

	t2 := time.Now()

	log.Trace("contract exec result:", leftOverGas, (t2.Nanosecond()-t1.Nanosecond())/1000000)
	if err != nil {
		log.Info("vm call error.", err)
	}
	return
}

// call vm to create a contract.
func (b *BlockChain) createContract(
	vmenv *vm.FVM,
	callerAddr common.Address,
	view *txo.UtxoViewpoint,
	block *asiutil.Block,
	stateDB *state.StateDB,
	tx *asiutil.Tx,
	out *protos.TxOut,
	gas uint64) (newAddr common.Address, leftOverGas uint64, snapshot int, err error) {

	leftOverGas = gas

	category, templateName, constructor, ok := DecodeCreateContractData(out.Data)
	if !ok {
		errStr := fmt.Sprintf("create contract, incorrect data protocol")
		return common.Address{}, leftOverGas, -1, ruleError(ErrBadContractData, errStr)
	}
	byteCode, ok, leftOverGas := b.GetByteCode(view, block, leftOverGas,
		stateDB, chaincfg.ActiveNetParams.FvmParam, category, templateName)
	if !ok {
		errStr := fmt.Sprintf("create contract, get byte code from template error")
		return common.Address{}, leftOverGas, -1, ruleError(ErrBadContractByteCode, errStr)
	}

	var inputHash []byte = nil
	if tx != nil {
		inputHash = tx.GetInputHash()
	}

	t1 := time.Now()
	// append(byteCode, constructor...) concat in Create
	_, newAddr, leftOverGas, snapshot, err = vmenv.Create(vm.AccountRef(callerAddr), byteCode, leftOverGas, big.NewInt(out.Value), &out.Asset, inputHash, constructor, false)
	if err != nil {
		errStr := fmt.Sprint("create contract error ", err)
		err = ruleError(ErrFailedCreateContract, errStr)
		return
	}

	// Init Template
	err, leftOverGas = b.InitTemplate(category, templateName, newAddr, leftOverGas, &out.Asset, vmenv)
	if err != nil {
		errStr := fmt.Sprint("init template error ", category, templateName, newAddr, err)
		err = ruleError(ErrFailedInitTemplate, errStr)
		return common.Address{}, leftOverGas, snapshot, err
	}

	t2 := time.Now()
	log.Infof("create contract height %d, addr %s, leftgas %d, time cost %d",
		block.Height(), newAddr.String(), leftOverGas, (t2.Nanosecond()-t1.Nanosecond())/1000000)

	return
}

//it's a template contract.
func (b *BlockChain) commitTemplateContract(
	vmenv *vm.FVM,
	callerAddr common.Address,
	tx *asiutil.Tx,
	out *protos.TxOut,
	gasLimit uint64) (leftOverGas uint64, snapshot int, err error) {

	category, templateName, _, _, _, err := DecodeTemplateContractData(out.Data)
	if err != nil {
		return 0, -1, err
	}

	createTemplateAddr, _, createTemplateABI := b.GetSystemContractInfo(common.TemplateWarehouse)
	createFunc := common.ContractTemplateWarehouse_CreateFunction()
	runCode, err := fvm.PackFunctionArgs(createTemplateABI, createFunc, category, string(templateName), tx.Hash())

	_, leftOverGas, snapshot, err = vmenv.Call(vm.AccountRef(callerAddr), createTemplateAddr, runCode, gasLimit, big.NewInt(out.Value), &out.Asset, false)
	if err != nil {
		errStr := fmt.Sprint("create template error:" + err.Error() + "," + tx.Hash().String())
		err = ruleError(ErrFailedCreateTemplate, errStr)
	}
	return
}

//check if the asset in the tx is forbidden.
func (b *BlockChain) checkAssetForbidden(
	block *asiutil.Block,
	stateDB *state.StateDB,
	tx *asiutil.Tx, view *txo.UtxoViewpoint,
	scriptClass txscript.ScriptClass) error {

	assetsCache := make(map[string]interface{})
	for _, txin := range tx.MsgTx().TxIn {
		utxo := view.LookupEntry(txin.PreviousOutPoint)
		if utxo == nil {
			str := fmt.Sprintf("checkAssetForbidden:utxo not found %v", txin.PreviousOutPoint)
			return ruleError(ErrMissingTxOut, str)
		}
		if utxo.Amount() == 0 {
			continue
		}

		limit := b.contractManager.IsLimit(block, stateDB, utxo.Asset())
		if limit <= 0 {
			continue
		}

		addrBytes, err := pkScriptToAddr(utxo.PkScript())
		if err != nil {
			str := fmt.Sprintf("checkAssetForbidden: can not extract utxo pkScript: %v", err)
			return ruleError(ErrInvalidPkScript, str)
		}
		key := utxo.Asset().String() + string(addrBytes)
		if _, ok := assetsCache[key]; ok {
			continue
		}
		support, _ := b.contractManager.IsSupport(block, stateDB, common.SupportCheckGas, utxo.Asset(), addrBytes)
		if !support {
			str := fmt.Sprintf("checkAssetForbidden: asset %v forbid utxo %v", utxo.Asset(), addrBytes)
			return ruleError(ErrForbiddenAsset, str)
		}
		assetsCache[key] = struct{}{}
	}

	for i, txout := range tx.MsgTx().TxOut {
		if txout.Value == 0 {
			continue
		}
		limit := b.contractManager.IsLimit(block, stateDB, &txout.Asset)
		if limit <= 0 {
			continue
		}
		addrBytes, err := pkScriptToAddr(txout.PkScript)
		if err != nil {
			if i == 0 && scriptClass == txscript.CreateTy {
				str := fmt.Sprintf("checkAssetForbidden: asset %v forbid to create ty", txout.Asset)
				return ruleError(ErrForbiddenAsset, str)
			} else {
				str := fmt.Sprintf("checkAssetForbidden: can not extract out pkScript: %v",err)
				return ruleError(ErrInvalidPkScript, str)
			}
		}
		key := txout.Asset.String() + string(addrBytes)
		if _, ok := assetsCache[key]; ok {
			continue
		}
		support, _ := b.contractManager.IsSupport(block, stateDB, common.SupportCheckGas, &txout.Asset, addrBytes)
		if !support {
			str := fmt.Sprintf("checkAssetForbidden: asset %v forbid out %v", txout.Asset, addrBytes)
			return ruleError(ErrForbiddenAsset, str)
		}
		assetsCache[key] = struct{}{}
	}
	return nil
}

// validate asset in the tx.
func (b *BlockChain) CheckTransferValidate(
	stateDB *state.StateDB,
	vtx *virtualtx.VirtualTransaction,
	block *asiutil.Block,
	gasLimit uint64) (leftOverGas uint64, err error) {
	leftOverGas = gasLimit
	assetsCache := make(map[string]interface{})
	support := false
	for _, transfer := range vtx.VTransfer {
		if transfer.Amount == 0 {
			continue
		}
		limit := b.contractManager.IsLimit(block, stateDB, transfer.Asset)
		if limit <= 0 {
			continue
		}
		key := transfer.Asset.String() + string(transfer.From)
		if transfer.VTransferType == virtualtx.VTransferTypeNormal {
			if _, ok := assetsCache[key]; ok {
				continue
			}
			support, leftOverGas = b.contractManager.IsSupport(block, stateDB, gasLimit, transfer.Asset, transfer.From)
			if !support {
				str := fmt.Sprintf("asset %v are forbidden to transferAsset from address %v: it is limited to use as txIn", transfer.Asset, transfer.From)
				return leftOverGas, ruleError(ErrForbiddenAsset, str)
			}
		}
		key = transfer.Asset.String() + string(transfer.To)
		if _, ok := assetsCache[key]; ok {
			continue
		}
		support, leftOverGas = b.contractManager.IsSupport(block, stateDB, leftOverGas, transfer.Asset, transfer.To)
		if !support {
			str := fmt.Sprintf("asset %v are forbidden to transferAsset to address %v: it is limited to use as txOut", transfer.Asset, transfer.To)
			return leftOverGas, ruleError(ErrForbiddenAsset, str)
		}
	}

	return leftOverGas, nil
}

// GenVtxMsg process createAsset, addAsset, transferAsset
func (b *BlockChain) GenVtxMsg(
	block *asiutil.Block,
	view *txo.UtxoViewpoint,
	tx *asiutil.Tx,
	vtx *virtualtx.VirtualTransaction) (*protos.MsgTx, error) {

	txMsg := protos.NewMsgTx(protos.TxVersion)
	if vtx == nil {
		log.Debug("no transfer in vm")
		return txMsg, nil
	}
	//add sig to extinct the virtual tx from different contract calls.
	mintsig, err := txscript.NewScriptBuilder().AddOp(txscript.OP_SPEND).AddData(tx.Hash().Bytes()).Script()
	if err != nil {
		return txMsg, err
	}

	transferInfos := vtx.GetAllTransfers()

	//sorted, keep all peers the same order.
	assetsKeys := make(asiutil.AssetsList, 0, len(transferInfos))
	for asset, _ := range transferInfos {
		assetsKeys = append(assetsKeys, asset)
	}
	sort.Sort(assetsKeys)

	for _, asset := range assetsKeys {
		transferInfo := transferInfos[asset]
		if transferInfo.Mint {
			//make sure there's only one TransferCreationOut in the virtual tx.
			has := false
			for _, in := range txMsg.TxIn {
				if asiutil.IsMintOrCreateInput(in) {
					has = true
				}
			}
			if !has {
				txMsg.AddTxIn(protos.NewTxIn(TransferCreationOut, mintsig))
			}
		}

		//now handle the erc20 transfer.
		if transferInfo.Erc20 {
			//sort by address.
			addressKeys := make(common.AddressList, 0, len(transferInfo.Erc20Change))
			for key := range transferInfo.Erc20Change {
				addressKeys = append(addressKeys, key)
			}
			sort.Sort(addressKeys)
			for _, addr := range addressKeys {
				amount := transferInfo.Erc20Change[addr]
				if amount > 0 { //income.
					pkScript, err := txscript.PayToAddrScript(&addr)
					if err != nil {
						return txMsg, err
					}
					txMsg.AddTxOut(protos.NewTxOut(amount, pkScript, asset))
				} else if amount < 0 { // outcome
					amount = -amount
					outs, err := b.fetchAssetBalance(block, view, addr, &asset)
					if err != nil {
						return txMsg, err
					}

					filled := int64(0)
					for _, out := range outs {
						if filled >= amount {
							break
						}
						filled, err = b.tryUseOneOut(view, txMsg, out, addr, &asset, amount, filled, mintsig)
						if err != nil {
							return txMsg, err
						}
					}

					if filled < amount {
						log.Error("transferAsset: not enough coin")
						errStr := fmt.Sprintf("transferAsset: not enough coin")
						return txMsg, ruleError(ErrSpendTooHigh, errStr)
					}
				}
			}
			//erc 721
		} else {
			addressKeys := make(common.AddressList, 0, len(transferInfo.Erc721Change))
			for key := range transferInfo.Erc721Change {
				addressKeys = append(addressKeys, key)
			}
			sort.Sort(addressKeys)
			for _, addr := range addressKeys {
				info := transferInfo.Erc721Change[addr]

				for voucherId := range info {
					if voucherId > 0 {
						pkScript, err := txscript.PayToAddrScript(&addr)
						if err != nil {
							return txMsg, err
						}
						txMsg.AddTxOut(protos.NewTxOut(voucherId, pkScript, asset))

					} else if voucherId < 0 {
						voucherId = -voucherId
						outs, err := b.fetchAssetBalance(block, view, addr, &asset)
						if err != nil {
							return txMsg, err
						}

						hasVoucherId := false
						for _, out := range outs {
							if hasVoucherId = b.tryAddToTxin(view, txMsg, out, addr, &asset, voucherId, mintsig); hasVoucherId {
								break
							}
						}

						if !hasVoucherId {
							errStr := fmt.Sprintf("handle the erc721 transfer error: no voucherId for asset to transfer.")
							return txMsg, ruleError(ErrBadTxOutValue, errStr)
						}
					}
				}
			}
		}
	}

	for i := 0; i < len(txMsg.TxIn); i++ {
		in := txMsg.TxIn[i]
		in.SignatureScript = mintsig
	}
	txMsg.TxContract.GasLimit = uint32(txMsg.SerializeSize() * common.GasPerByte)

	return txMsg, nil
}

// filter packaged signatures and return remaining signatures.
//
func (b *BlockChain) FilterPackagedSignatures(signList []*asiutil.BlockSign) []*asiutil.BlockSign {
	unpackagedSignatures := make([]*asiutil.BlockSign, 0, len(signList))

	_ = b.db.View(func(dbTx database.Tx) error {
		signatureBucket := dbTx.Metadata().Bucket(signatureSetBucketName)

		for _, sign := range signList {
			buff := signatureBucket.Get(sign.Hash()[:])
			if buff == nil {
				unpackagedSignatures = append(unpackagedSignatures, sign)
			}
		}
		return nil

	})

	return unpackagedSignatures
}

//generate rollback message if any error happens when connecting block.
func (b *BlockChain) GenRollbackMsg(view *txo.UtxoViewpoint, tx *asiutil.Tx) (*protos.MsgTx, error) {
	txMsg := protos.NewMsgTx(0)

	assetsmap := make(map[protos.Asset]int64)
	for idx, txout := range tx.MsgTx().TxOut {
		if txout.Value > 0 {
			assetsmap[txout.Asset] += txout.Value
			//add sig to extinct the virtual tx from different contract calls.
			sig, err := txscript.NewScriptBuilder().AddOp(txscript.OP_SPEND).AddData(tx.Hash().Bytes()).Script()
			if err != nil {
				// unexpected fall through
				return txMsg, err
			}
			prevout := protos.NewOutPoint(tx.Hash(), uint32(idx))
			txMsg.AddTxIn(protos.NewTxIn(prevout, sig))
		}
	}
	if len(tx.MsgTx().TxIn) == 0 {
		return txMsg, nil
	}

	for _, txin := range tx.MsgTx().TxIn {
		entry := view.LookupEntry(txin.PreviousOutPoint)
		if entry == nil {
			// unexpected fall through
			errStr := fmt.Sprintf("GenRollbackMsg error: can not find UtxoEntry by PreviousOutPoint")
			return txMsg, ruleError(ErrMissingTxOut, errStr)
		}
		pkscript := entry.PkScript()
		assets := entry.Asset()
		if amount, exist := assetsmap[*assets]; exist {
			if amount > entry.Amount() {
				assetsmap[*assets] -= entry.Amount()
				txMsg.AddTxOut(protos.NewTxOut(entry.Amount(), pkscript, *assets))
			} else {
				txMsg.AddTxOut(protos.NewTxOut(amount, pkscript, *assets))
				delete(assetsmap, *assets)
			}
		}
	}

	return txMsg, nil
}

// spend one utxo for virutal tx.
func (b *BlockChain) tryAddToTxin(view *txo.UtxoViewpoint, txMsg *protos.MsgTx, out protos.OutPoint,
	addr common.Address, assets *protos.Asset, voucherId int64, mintsig []byte) bool {
	entry := view.LookupEntry(out)
	if entry == nil || entry.IsSpent() {
		return false
	}

	if entry.Amount() == voucherId && entry.Asset().Equal(assets) {
		newOutPoint := protos.OutPoint{}
		newOutPoint.Hash.SetBytes(out.Hash[:])
		newOutPoint.Index = out.Index
		txMsg.AddTxIn(protos.NewTxIn(&newOutPoint, mintsig))
		return true
	}

	return false
}

// spend one utxo for virutal tx.
// if the amount of utxo is larger than needed, generate a change.
func (b *BlockChain) tryUseOneOut(view *txo.UtxoViewpoint, txMsg *protos.MsgTx, out protos.OutPoint,
	addr common.Address, assets *protos.Asset, amount int64, filled int64, mintsig []byte) (int64, error) {
	entry := view.LookupEntry(out)
	if entry == nil || entry.IsSpent() {
		return filled, nil
	}

	for _, in := range txMsg.TxIn {
		if out.Hash.IsEqual(&in.PreviousOutPoint.Hash) && out.Index == in.PreviousOutPoint.Index {
			return filled, nil
		}
	}

	if entry.Asset().Equal(assets) {
		newOutPoint := protos.OutPoint{}
		newOutPoint.Hash.SetBytes(out.Hash[:])
		newOutPoint.Index = out.Index
		txMsg.AddTxIn(protos.NewTxIn(&newOutPoint, mintsig))
		filled += entry.Amount()

		if filled > amount {
			changes := filled - amount

			pkScript, err := txscript.PayToAddrScript(&addr)
			if err != nil {
				return filled, err
			}

			txMsg.AddTxOut(protos.NewTxOut(changes, pkScript, *assets))
		}

		return filled, nil
	}

	return filled, nil
}

// isCurrent returns whether or not the chain believes it is current.  Several
// factors are used to guess, but the key factors that allow the chain to
// believe it is current are:
//  - Latest block height is after the latest checkpoint (if enablevirtual-machine/fvm/core/types/virtual_tx.god)
//  - Latest block has a timestamp newer than 24 hours ago
//
// This function MUST be called with the chain state lock held (for reads).
func (b *BlockChain) isCurrent() bool {
	// Not current if the latest main (best) chain height is before the
	// latest known good checkpoint (when checkpoints are enabled).
	checkpoint := b.LatestCheckpoint()
	if checkpoint != nil && b.bestChain.Tip().height < checkpoint.Height {
		return false
	}

	if chaincfg.ActiveNetParams.Net != common.MainNet {
		return true
	}

	// Not current if the latest best block has a timestamp before a hour
	// ago.
	//
	// The chain appears to be current if none of the checks reported
	// otherwise.
	minusHour := b.timeSource.AdjustedTime() - int64(time.Hour/time.Second)
	return b.bestChain.Tip().timestamp >= minusHour
}

// IsCurrent returns whether or not the chain believes it is current.  Several
// factors are used to guess, but the key factors that allow the chain to
// believe it is current are:
//  - Latest block height is after the latest checkpoint (if enabled)
//  - Latest block has a timestamp newer than 24 hours ago
//
// This function is safe for concurrent access.
func (b *BlockChain) IsCurrent() bool {
	b.chainLock.RLock()
	defer b.chainLock.RUnlock()

	return b.isCurrent()
}

// BestSnapshot returns information about the current best chain block and
// related state as of the current point in time.  The returned instance must be
// treated as immutable since it is shared by all callers.
//
// This function is safe for concurrent access.
func (b *BlockChain) BestSnapshot() *BestState {
	b.stateLock.RLock()
	snapshot := b.stateSnapshot
	b.stateLock.RUnlock()
	return snapshot
}

// FetchHeader returns the block header identified by the given hash or an error
// if it doesn't exist.
func (b *BlockChain) FetchHeader(hash *common.Hash) (protos.BlockHeader, error) {
	// Reconstruct the header from the block index if possible.
	if node := b.index.LookupNode(hash); node != nil {
		return node.Header(), nil
	}

	// Fall back to loading it from the database.
	var header *protos.BlockHeader
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		header, err = dbFetchHeaderByHash(dbTx, hash)
		return err
	})
	if err != nil {
		return protos.BlockHeader{}, err
	}
	return *header, nil
}

// MainChainHasBlock returns whether or not the block with the given hash is in
// the main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) MainChainHasBlock(hash *common.Hash) bool {
	node := b.index.LookupNode(hash)
	return node != nil && b.bestChain.Contains(node)
}

// BlockLocatorFromHash returns a block locator for the passed block hash.
// See BlockLocator for details on the algorithm used to create a block locator.
//
// In addition to the general algorithm referenced above, this function will
// return the block locator for the latest known tip of the main (best) chain if
// the passed hash is not currently known.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockLocatorFromHash(hash *common.Hash) BlockLocator {
	b.chainLock.RLock()
	node := b.index.LookupNode(hash)
	locator := b.bestChain.blockLocator(node)
	b.chainLock.RUnlock()
	return locator
}

// LatestBlockLocator returns a block locator for the latest known tip of the
// main (best) chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) LatestBlockLocator() (BlockLocator, error) {
	b.chainLock.RLock()
	locator := b.bestChain.BlockLocator(nil)
	b.chainLock.RUnlock()
	return locator, nil
}

// BlockHeightByHash returns the height of the block with the given hash in the
// main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockHeightByHash(hash *common.Hash) (int32, error) {
	node := b.index.LookupNode(hash)
	if node == nil || !b.bestChain.Contains(node) {
		str := fmt.Sprintf("block %s is not in the main chain or node is nil", hash)
		return 0, errNotInMainChain(str)
	}

	return node.height, nil
}

// BlockHashByHeight returns the hash of the block at the given height in the
// main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockHashByHeight(blockHeight int32) (*common.Hash, error) {
	node := b.bestChain.NodeByHeight(blockHeight)
	if node == nil {
		str := fmt.Sprintf("BlockHashByHeight: no block at height %d exists", blockHeight)
		return nil, errNotInMainChain(str)

	}
	return &node.hash, nil
}

func (b *BlockChain) BlockTimeByHeight(blockHeight int32) (int64, error) {
	if blockHeight < 0 {
		blockHeight = 0
	}
	node := b.bestChain.NodeByHeight(blockHeight)
	if node == nil {
		str := fmt.Sprintf("BlockTimeByHeight: no block at height %d exists", blockHeight)
		return time.Now().Unix(), errNotInMainChain(str)

	}

	return node.timestamp, nil
}

// HeightToHashRange returns a range of block hashes for the given start height
// and end hash, inclusive on both ends.  The hashes are for all blocks that are
// ancestors of endHash with height greater than or equal to startHeight.  The
// end hash must belong to a block that is known to be valid.
//
// This function is safe for concurrent access.
func (b *BlockChain) HeightToHashRange(startHeight int32,
	endHash *common.Hash, maxResults int) ([]common.Hash, error) {

	endNode := b.index.LookupNode(endHash)
	if endNode == nil {
		str := fmt.Sprintf("no known block node with hash %v", endHash)
		return nil, ruleError(ErrNodeUnknown, str)
	}
	if !b.index.NodeStatus(endNode).KnownValid() {
		str := fmt.Sprintf("block %v is not yet validated", endHash)
		return nil, ruleError(ErrInvalidBlock, str)
	}
	endHeight := endNode.height

	if startHeight < 0 {
		str := fmt.Sprintf("start height (%d) is below 0", startHeight)
		return nil, ruleError(ErrBadHeightToHashRange, str)
	}
	if startHeight > endHeight {
		str := fmt.Sprintf("start height (%d) is past end height (%d)",
			startHeight, endHeight)
		return nil, ruleError(ErrBadHeightToHashRange, str)
	}

	resultsLength := int(endHeight - startHeight + 1)
	if resultsLength > maxResults {
		str := fmt.Sprintf("number of results (%d) would exceed max (%d)",
			resultsLength, maxResults)
		return nil, ruleError(ErrBadHeightToHashRange, str)
	}

	// Walk backwards from endHeight to startHeight, collecting block hashes.
	node := endNode
	hashes := make([]common.Hash, resultsLength)
	for i := resultsLength - 1; i >= 0; i-- {
		hashes[i] = node.hash
		node = node.parent
	}
	return hashes, nil
}

// IntervalBlockHashes returns hashes for all blocks that are ancestors of
// endHash where the block height is a positive multiple of interval.
//
// This function is safe for concurrent access.
func (b *BlockChain) IntervalBlockHashes(endHash *common.Hash, interval int,
) ([]common.Hash, error) {

	endNode := b.index.LookupNode(endHash)
	if endNode == nil {
		str := fmt.Sprintf("no known block node with hash %v", endHash)
		return nil, ruleError(ErrNodeUnknown, str)
	}
	if !b.index.NodeStatus(endNode).KnownValid() {
		str := fmt.Sprintf("block %v is not yet validated", endHash)
		return nil, ruleError(ErrInvalidBlock, str)
	}
	endHeight := endNode.height

	resultsLength := int(endHeight) / interval
	hashes := make([]common.Hash, resultsLength)

	b.bestChain.mtx.Lock()
	defer b.bestChain.mtx.Unlock()

	blockNode := endNode
	for index := int(endHeight) / interval; index > 0; index-- {
		// Use the bestChain chainView for faster lookups once lookup intersects
		// the best chain.
		blockHeight := int32(index * interval)
		if b.bestChain.contains(blockNode) {
			blockNode = b.bestChain.nodeByHeight(blockHeight)
		} else {
			blockNode = blockNode.Ancestor(blockHeight)
		}

		hashes[index-1] = blockNode.hash
	}

	return hashes, nil
}

// locateInventory returns the node of the block after the first known block in
// the locator along with the number of subsequent nodes needed to either reach
// the provided stop hash or the provided max number of entries.
//
// In addition, there are two special cases:
//
// - When no locators are provided, the stop hash is treated as a request for
//   that block, so it will either return the node associated with the stop hash
//   if it is known, or nil if it is unknown
// - When locators are provided, but none of them are known, nodes starting
//   after the genesis block will be returned
//
// This is primarily a helper function for the locateBlocks and locateHeaders
// functions.
//
// This function MUST be called with the chain state lock held (for reads).
func (b *BlockChain) locateInventory(locator BlockLocator, hashStop *common.Hash, maxEntries uint32) (*blockNode, uint32) {
	// There are no block locators so a specific block is being requested
	// as identified by the stop hash.
	stopNode := b.index.LookupNode(hashStop)
	if len(locator) == 0 {
		if stopNode == nil {
			// No blocks with the stop hash were found so there is
			// nothing to do.
			return nil, 0
		}
		return stopNode, 1
	}

	// Find the most recent locator block hash in the main chain.  In the
	// case none of the hashes in the locator are in the main chain, fall
	// back to the genesis block.
	startNode := b.bestChain.Genesis()
	for _, hash := range locator {
		node := b.index.LookupNode(hash)
		if node != nil && b.bestChain.Contains(node) {
			startNode = node
			break
		}
	}

	// Start at the block after the most recently known block.  When there
	// is no next block it means the most recently known block is the tip of
	// the best chain, so there is nothing more to do.
	startNode = b.bestChain.Next(startNode)
	if startNode == nil {
		return nil, 0
	}

	// Calculate how many entries are needed.
	total := uint32((b.bestChain.Tip().height - startNode.height) + 1)
	if stopNode != nil && b.bestChain.Contains(stopNode) &&
		stopNode.height >= startNode.height {

		total = uint32((stopNode.height - startNode.height) + 1)
	}
	if total > maxEntries {
		total = maxEntries
	}

	return startNode, total
}

// locateBlocks returns the hashes of the blocks after the first known block in
// the locator until the provided stop hash is reached, or up to the provided
// max number of block hashes.
//
// See the comment on the exported function for more details on special cases.
//
// This function MUST be called with the chain state lock held (for reads).
func (b *BlockChain) locateBlocks(locator BlockLocator, hashStop *common.Hash, maxHashes uint32) []common.Hash {
	// Find the node after the first known block in the locator and the
	// total number of nodes after it needed while respecting the stop hash
	// and max entries.
	node, total := b.locateInventory(locator, hashStop, maxHashes)
	if total == 0 {
		return nil
	}

	// Populate and return the found hashes.
	hashes := make([]common.Hash, 0, total)
	for i := uint32(0); i < total; i++ {
		hashes = append(hashes, node.hash)
		node = b.bestChain.Next(node)
		// When reorganize chain run, disconnectblock may lead the last node be nil
		if node == nil {
			break
		}
	}
	return hashes
}

// LocateBlocks returns the hashes of the blocks after the first known block in
// the locator until the provided stop hash is reached, or up to the provided
// max number of block hashes.
//
// In addition, there are two special cases:
//
// - When no locators are provided, the stop hash is treated as a request for
//   that block, so it will either return the stop hash itself if it is known,
//   or nil if it is unknown
// - When locators are provided, but none of them are known, hashes starting
//   after the genesis block will be returned
//
// This function is safe for concurrent access.
func (b *BlockChain) LocateBlocks(locator BlockLocator, hashStop *common.Hash, maxHashes uint32) []common.Hash {
	b.chainLock.RLock()
	hashes := b.locateBlocks(locator, hashStop, maxHashes)
	b.chainLock.RUnlock()
	return hashes
}

// locateHeaders returns the headers of the blocks after the first known block
// in the locator until the provided stop hash is reached, or up to the provided
// max number of block headers.
//
// See the comment on the exported function for more details on special cases.
//
// This function MUST be called with the chain state lock held (for reads).
func (b *BlockChain) locateHeaders(locator BlockLocator, hashStop *common.Hash, maxHeaders uint32) []protos.BlockHeader {
	// Find the node after the first known block in the locator and the
	// total number of nodes after it needed while respecting the stop hash
	// and max entries.
	node, total := b.locateInventory(locator, hashStop, maxHeaders)
	if total == 0 {
		return nil
	}

	// Populate and return the found headers.
	headers := make([]protos.BlockHeader, 0, total)
	for i := uint32(0); i < total; i++ {
		headers = append(headers, node.Header())
		node = b.bestChain.Next(node)
	}
	return headers
}

// LocateHeaders returns the headers of the blocks after the first known block
// in the locator until the provided stop hash is reached, or up to a max of
// protos.MaxBlockHeadersPerMsg headers.
//
// In addition, there are two special cases:
//
// - When no locators are provided, the stop hash is treated as a request for
//   that header, so it will either return the header for the stop hash itself
//   if it is known, or nil if it is unknown
// - When locators are provided, but none of them are known, headers starting
//   after the genesis block will be returned
//
// This function is safe for concurrent access.
func (b *BlockChain) LocateHeaders(locator BlockLocator, hashStop *common.Hash) []protos.BlockHeader {
	b.chainLock.RLock()
	headers := b.locateHeaders(locator, hashStop, protos.MaxBlockHeadersPerMsg)
	b.chainLock.RUnlock()
	return headers
}

func (b *BlockChain) GetDepthInActiveChain(height int32) int32 {
	return b.BestSnapshot().Height - height + 1
}

func (b *BlockChain) EthDB() database.Database {
	return b.ethDB
}

func (b *BlockChain) GetVmConfig() *vm.Config {
	return &b.vmConfig
}

// SubscribeLogsEvent registers a subscription of []*types.Log.
func (b *BlockChain) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.scope.Track(b.logsFeed.Subscribe(ch))
}

// SubscribeRemovedLogsEvent registers a subscription of RemovedLogsEvent.
func (b *BlockChain) SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) event.Subscription {
	return b.scope.Track(b.rmLogsFeed.Subscribe(ch))
}

func (b *BlockChain) Scope() *event.SubscriptionScope {
	return &b.scope
}

// PostChainEvents iterates over the events generated by a chain insertion and
// posts them into the event feed.
func (b *BlockChain) PostChainEvents(events []interface{}, logs []*types.Log) {
	// post event logs for further processing
	if logs != nil {
		fmt.Println(">>>>>>>>>>>>>>>>Post Log>>>>>>>>>>>>>>")
		fmt.Println(logs)
		b.logsFeed.Send(logs)
	}
	// for _, event := range events {
	// 	switch ev := event.(type) {
	// 	case ChainEvent:
	// 		bc.chainFeed.Send(ev)
	//
	// 	case ChainHeadEvent:
	// 		bc.chainHeadFeed.Send(ev)
	//
	// 	case ChainSideEvent:
	// 		bc.chainSideFeed.Send(ev)
	// 	}
	// }
}

// FetchTemplate return decoded values for template data.
// It fetches data from tx map first. If not exist, it will search from db.
func (b *BlockChain) FetchTemplate(view *txo.UtxoViewpoint, hash *common.Hash) (uint16, []byte, []byte, []byte, []byte, error) {
	if view != nil {
		txs := view.Txs()
		if m, exist := txs[*hash]; exist {
			if m.Action == txo.ViewRm {
				return 0, nil, nil, nil, nil, ruleError(ErrInvalidTemplate, "template tx roll back")
			}
			if m.Action == txo.ViewAdd && m.MsgTx != nil {
				return DecodeTemplateContractData(m.MsgTx.TxOut[0].Data)
			}
		}
	}
	blockRegion, err := b.templateIndex.FetchBlockRegion(hash[:])
	if err != nil {
		return 0, nil, nil, nil, nil, ruleError(ErrInvalidTemplate, "no template in db "+err.Error())
	}

	var txBytes []byte
	err = b.db.View(func(dbTx database.Tx) error {
		var err error
		txBytes, err = dbTx.FetchBlockRegion(blockRegion)
		return err
	})
	if err != nil {
		return 0, nil, nil, nil, nil, ruleError(ErrInvalidTemplate, "err db region for template "+err.Error())
	}

	return DecodeTemplateContractData(txBytes)
}

// This method is statistic miners per round. It is used only in coinbase tx data.
//
// @return sortedValidatorList validators of pre-round, in order
//         expectedBlocks      expected blocks of pre-round each validator may mined.
//         actualBlocks        actual blocks of pre-round each validator mined.
//         mapping             new candidates for validators
func (b *BlockChain) CountRoundMinerInfo(round uint32, thisnode *blockNode) (sortedValidatorList common.AddressList,
	expectedBlocks []uint16, actualBlocks []uint16, mapping map[string]*ainterface.ValidatorInfo, err error) {
	var preroundLastNode *blockNode
	for node := thisnode; node != nil; node = node.parent {
		if node.round.Round < round {
			preroundLastNode = node
			break
		}
	}
	_, weightMap, err := b.GetValidatorsByNode(round, preroundLastNode)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	mapping, err = b.roundManager.GetHsMappingByRound(round)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for validator, _ := range weightMap {
		sortedValidatorList = append(sortedValidatorList, validator)
	}
	sort.Sort(sortedValidatorList)
	actualWeight := make(map[common.Address]uint16)
	for node := thisnode; node != nil && node.round.Round == round; node = node.parent {
		actualWeight[node.coinbase]++
	}

	expectedBlocks = make([]uint16, len(sortedValidatorList))
	actualBlocks = make([]uint16, len(sortedValidatorList))
	for i, validator := range sortedValidatorList {
		expectedBlocks[i] = weightMap[validator]
		if actualWeight, ok := actualWeight[validator]; ok {
			actualBlocks[i] = actualWeight
		}
	}

	return
}

// Config is a descriptor which specifies the blockchain instance configuration.
type Config struct {
	// DB defines the database which houses the blocks and will be used to
	// store all metadata created by this package such as the utxo set.
	//
	// This field is required.
	DB database.Transactor

	// Interrupt specifies a channel the caller can close to signal that
	// long running operations, such as catching up indexes or performing
	// database migrations, should be interrupted.
	//
	// This field can be nil if the caller does not desire the behavior.
	Interrupt <-chan struct{}

	// ChainParams identifies which chain parameters the chain is associated
	// with.
	//
	// This field is required.
	ChainParams *chaincfg.Params

	// Checkpoints hold caller-defined checkpoints that should be added to
	// the default checkpoints in ChainParams.  Checkpoints must be sorted
	// by height.
	//
	// This field can be nil if the caller does not wish to specify any
	// checkpoints.
	Checkpoints []chaincfg.Checkpoint

	// TimeSource defines the median time source to use for things such as
	// block processing and determining whether or not the chain is current.
	//
	// The caller is expected to keep a reference to the time source as well
	// and add time samples from other peers on the network so the local
	// time is adjusted to be in agreement with other peers.
	TimeSource MedianTimeSource

	// IndexManager defines an index manager to use when initializing the
	// chain and connecting and disconnecting blocks.
	IndexManager IndexManager

	// HashCache defines a transaction hash mid-state cache to use when
	// validating transactions. This cache has the potential to greatly
	// speed up transaction validation as re-using the pre-calculated
	// mid-state eliminates the O(N^2) validation complexity due to the
	// SigHashAll flag.
	//
	// This field can be nil if the caller is not interested in using a
	// signature cache.
	// HashCache *txscript.HashCache

	// State DB
	StateDB database.Database

	TemplateIndex Indexer

	BtcClient ainterface.IBtcClient

	RoundManager ainterface.IRoundManager

	// ContractManager defines a contract manager to use when initializing the
	// chain and validating system state any time.
	ContractManager ainterface.ContractManager

	FeesChan chan interface{}
}

// New returns a BlockChain instance using the provided configuration details.
func New(config *Config, fconfig *chaincfg.FConfig) (*BlockChain, error) {
	// Enforce required config fields.
	if config.DB == nil {
		return nil, common.AssertError("blockchain.New database is nil")
	}
	if config.ChainParams == nil {
		return nil, common.AssertError("blockchain.New chain parameters nil")
	}
	if config.TimeSource == nil {
		return nil, common.AssertError("blockchain.New timesource is nil")
	}
	if config.StateDB == nil {
		return nil, common.AssertError("blockchain.New state database is nil")
	}

	// Generate a checkpoint by height map from the provided checkpoints
	// and assert the provided checkpoints are sorted by height as required.
	var checkpointsByHeight map[int32]*chaincfg.Checkpoint
	var prevCheckpointHeight int32
	if len(config.Checkpoints) > 0 {
		checkpointsByHeight = make(map[int32]*chaincfg.Checkpoint)
		for i := range config.Checkpoints {
			checkpoint := &config.Checkpoints[i]
			if checkpoint.Height <= prevCheckpointHeight {
				return nil, common.AssertError("blockchain.New " +
					"checkpoints are not sorted by height")
			}

			checkpointsByHeight[checkpoint.Height] = checkpoint
			prevCheckpointHeight = checkpoint.Height
		}
	}

	vmConfig := &vm.Config{}

	if fconfig != nil {
		vmConfig.EWASMInterpreter = fconfig.EwasmOptions
		vmConfig.FVMInterpreter = fconfig.EvmOptions
	}

	params := config.ChainParams
	b := BlockChain{
		checkpoints:         config.Checkpoints,
		checkpointsByHeight: checkpointsByHeight,
		db:                  config.DB,
		chainParams:         params,
		timeSource:          config.TimeSource,
		indexManager:        config.IndexManager,
		index:               newBlockIndex(config.DB, params),
		bestChain:           newChainView(nil),
		orphans:             make(map[common.Hash]*orphanBlock),
		prevOrphans:         make(map[common.Hash][]*orphanBlock),
		warningCaches:       newThresholdCaches(vbNumBits),
		deploymentCaches:    newThresholdCaches(chaincfg.DefinedDeployments),
		stateCache:          state.NewDatabase(config.StateDB),
		ethDB:               config.StateDB,
		templateIndex:       config.TemplateIndex,
		roundManager:        config.RoundManager,
		contractManager:     config.ContractManager,
		vmConfig:            *vmConfig,
		feesChan:            config.FeesChan,
	}

	if err := b.contractManager.Init(&b, params.GenesisBlock.Transactions[0].TxOut[0].Data); err != nil {
		errStr := fmt.Sprint("failed to init contractManger: " + err.Error())
		return nil, ruleError(ErrFailedInitContractManager, errStr)
	}

	// Initialize the chain state from the passed database.  When the db
	// does not yet contain any chain state, both it and the chain state
	// will be initialized to contain only the genesis block.
	if err := b.initChainState(int64(config.ChainParams.ChainStartTime)); err != nil {
		return nil, err
	}

	// Initialize and catch up all of the currently active optional indexes
	// as needed.
	if config.IndexManager != nil {
		err := config.IndexManager.Init(&b, config.Interrupt)
		if err != nil {
			return nil, err
		}
	}

	bestNode := b.bestChain.Tip()

	// Initialize round manager
	err := config.RoundManager.Init(bestNode.round.Round, b.db, config.BtcClient)
	if err != nil {
		return nil, err
	}

	log.Infof("Chain state (height %d, hash %v, totaltx %d)",
		bestNode.height, bestNode.hash, b.stateSnapshot.TotalTxns)

	return &b, nil
}

// Create contract protocol is consist of header and content:
// header=[template category] [name length] [name]
//        2bytes              2bytes        255 bytes at most
// content=[header]+[constructor]
func DecodeCreateContractData(data []byte) (uint16, string, []byte, bool) {
	index := 0
	validLen := 2

	// Template Category
	if len(data) < validLen {
		return 0, "", nil, false
	}
	category := binary.BigEndian.Uint16(data[index:validLen])
	index = validLen
	validLen += 4

	// Template Name Length
	if len(data) < validLen {
		return 0, "", nil, false
	}
	nameLength := binary.BigEndian.Uint32(data[index:validLen])
	index = validLen
	validLen += int(nameLength)

	// Template Name
	if len(data) < validLen {
		return 0, "", nil, false
	}
	templateName := string(data[index:validLen])
	index = validLen

	// Constructor
	constructor := data[index:]
	return category, templateName, constructor, true
}

// Template protocol is consist of header and content:
// header=[type] [name length][byte code length][abi length][source length]
//        2bytes 4bytes       4bytes            4bytes      4bytes
// content=[name]+[byte code]+[abi]+[source]
func DecodeTemplateContractData(data []byte) (uint16, []byte, []byte, []byte, []byte, error) {
	// Template Category
	if len(data) < TemplateHeaderLength {
		return 0, nil, nil, nil, nil, ruleError(ErrInvalidTemplate, "no template header")
	}
	offset := 0
	category := binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2
	nameLength := int(binary.BigEndian.Uint32(data[offset : offset+4]))
	offset += 4
	codeLength := int(binary.BigEndian.Uint32(data[offset : offset+4]))
	offset += 4
	abiLength := int(binary.BigEndian.Uint32(data[offset : offset+4]))
	offset += 4
	sourceLength := int(binary.BigEndian.Uint32(data[offset : offset+4]))
	offset += 4
	if nameLength <= 0 ||
		codeLength <= 0 || codeLength > TemplateDataFieldLength ||
		abiLength <= 0 || abiLength > TemplateDataFieldLength ||
		sourceLength <= 0 || sourceLength > TemplateDataFieldLength ||
		len(data) != TemplateHeaderLength+nameLength+codeLength+abiLength+sourceLength {
		return 0, nil, nil, nil, nil, ruleError(ErrInvalidTemplate, "parse template error")
	}
	templateName := data[offset : offset+nameLength]
	offset += nameLength
	bytecode := data[offset : offset+codeLength]
	offset += codeLength
	abi := data[offset : offset+abiLength]
	offset += abiLength
	source := data[offset : offset+sourceLength]
	offset += sourceLength

	return category, templateName, bytecode, abi, source, nil
}

// get the vote value of current vote.
// this method is only invoked when the method is in vote tx.
func getVoteValue(view *txo.UtxoViewpoint, tx *asiutil.Tx, voteId *txo.VoteId, asset *protos.Asset) int64 {
	if view == nil {
		return 0
	}
	if asset.IsIndivisible() {
		return 0
	}
	usedvalue := int64(0)
	for _, txIn := range tx.MsgTx().TxIn {
		utxo := view.LookupEntry(txIn.PreviousOutPoint)
		if utxo == nil {
			continue
		}
		if !utxo.Asset().Equal(asset) {
			continue
		}
		if utxo.LockItem() == nil || utxo.LockItem().Entries == nil {
			continue
		}
		if entry, ok := utxo.LockItem().Entries[*voteId]; ok {
			usedvalue += entry.Amount
		}
	}

	value := int64(0)
	for _, txOut := range tx.MsgTx().TxOut {
		if !txOut.Asset.Equal(asset) {
			continue
		}
		value += txOut.Value
	}
	if usedvalue < value {
		return value - usedvalue
	}
	return 0
}

// Prepare initializes the consensus fields of a block header according to the
// rules of a particular engine. The changes are executed inline.
// This method will lock the chain, and it will be released in Commit or Rollback method.
func (b *BlockChain) Prepare(header *protos.BlockHeader, gasFloor, gasCeil uint64) (
	stateDB *state.StateDB, feepool map[protos.Asset]int32, contractOut *protos.TxOut, err error) {
	b.chainLock.RLock()
	parent := b.GetTip()
	if parent.Round() > header.Round || (parent.Round() == header.Round && parent.slot >= header.SlotIndex) {
		err = errors.New("slot changes when prepare block")
		return
	}
	header.PrevBlock = parent.hash
	header.GasLimit = CalcGasLimit(parent.GasUsed(), parent.GasLimit(), gasFloor, gasCeil)
	header.Height = parent.Height() + 1
	// Calculate the next expected block version based on the state of the
	// rule change deployments.
	header.Version, err = b.calcNextBlockVersion(parent)
	if err != nil {
		return
	}

	if header.Round > parent.Round() {
		contractOut, err = b.createCoinbaseContractOut(parent.Round(), parent)
		if err != nil {
			return
		}
	}

	// Prepare support fee list
	block := asiutil.NewBlock(&protos.MsgBlock{
		Header: parent.Header(),
	})
	stateDB, err = state.New(parent.StateRoot(), b.GetStateCache())
	if err != nil {
		return
	}
	feepool, err = b.GetAcceptFees(block,
		stateDB, chaincfg.ActiveNetParams.FvmParam, header.Height)
	return
}

// unlock the chainLock, this method is use
func (b *BlockChain) ChainRUnlock() {
	b.chainLock.RUnlock()
}
