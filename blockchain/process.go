// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
)

// blockExists determines whether a block with the given hash exists either in
// the main chain or any side chains.
//
// This function is safe for concurrent access.
func (b *BlockChain) blockExists(hash *common.Hash) (bool, error) {
	// Check block index first (could be main chain or side chain blocks).
	if b.index.HaveBlock(hash) {
		return true, nil
	}

	// Check in the database.
	var exists bool
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		key := database.NewNormalBlockKey(hash)
		exists, err = dbTx.HasBlock(key)
		if err != nil || !exists {
			return err
		}

		// Ignore side chain blocks in the database.  This is necessary
		// because there is not currently any record of the associated
		// block index data such as its block height, so it's not yet
		// possible to efficiently load the block and do anything useful
		// with it.
		//
		// Ultimately the entire block index should be serialized
		// instead of only the current main chain so it can be consulted
		// directly.
		_, err = dbFetchHeightByHash(dbTx, hash)
		if isNotInMainChainErr(err) {
			exists = false
			return nil
		}
		return err
	})
	return exists, err
}

// processOrphans determines if there are any orphans which depend on the passed
// block hash (they are no longer orphans if true) and potentially accepts them.
// It repeats the process for the newly accepted blocks (to detect further
// orphans which may no longer be orphans) until there are no more.
//
// The flags do not modify the behavior of this function directly, however they
// are needed to pass along to maybeAcceptBlock.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) processOrphans(hash *common.Hash, flags common.BehaviorFlags) error {
	// Start with processing at least the passed hash.  Leave a little room
	// for additional orphan blocks that need to be processed without
	// needing to grow the array in the common case.
	processHashes := make([]*common.Hash, 0, 10)
	processHashes = append(processHashes, hash)
	for len(processHashes) > 0 {
		// Pop the first hash to process from the slice.
		processHash := processHashes[0]
		processHashes[0] = nil // Prevent GC leak.
		processHashes = processHashes[1:]

		// Look up all orphans that are parented by the block we just
		// accepted.  This will typically only be one, but it could
		// be multiple if multiple blocks are mined and broadcast
		// around the same time.  The one with the most proof of work
		// will eventually win out.  An indexing for loop is
		// intentionally used over a range here as range does not
		// reevaluate the slice on each iteration nor does it adjust the
		// index for the modified slice.
		for i := 0; i < len(b.prevOrphans[*processHash]); i++ {
			orphan := b.prevOrphans[*processHash][i]
			if orphan == nil {
				log.Warnf("Found a nil entry at index %d in the "+
					"orphan dependency list for block %v", i,
					processHash)
				continue
			}

			// Remove the orphan from the orphan pool.
			orphanHash := orphan.block.Hash()
			b.removeOrphanBlock(orphan)
			i--

			// Potentially accept the block into the block chain.
			_, err := b.maybeAcceptBlock(orphan.block, nil, nil, nil, flags)
			if err != nil {
				return err
			}

			// Add this block to the list of blocks to process so
			// any orphan blocks that depend on this block are
			// handled too.
			processHashes = append(processHashes, orphanHash)
		}
	}
	return nil
}

func (b *BlockChain) GetTip() *blockNode {
	return b.bestChain.Tip()
}

func (b *BlockChain) GetNodeByHeight(targetHeight int32) (*blockNode, error) {
	node := b.bestChain.NodeByHeight(targetHeight)
	if node == nil {
		str := fmt.Sprintf("no block node at height %d exists in bestChain", targetHeight)
		return nil, errNotInMainChain(str)
	}
	return node, nil
}

func (b *BlockChain) GetNodeByRoundSlot(round uint32, slot uint16) *blockNode {
	node := b.bestChain.Tip()
	if node.Round() < round || (node.Round() == round && node.Slot() < slot) {
		return nil
	}
	if node.Round() == round || node.Round() == round + 1 {
		for {
			if node.Round() < round {
				return nil
			}
			if node.Round() == round {
				if node.Slot() == slot{
					return node
				}
				if node.Slot() < slot{
					return nil
				}
			}
			node = node.parent
		}
	}
	from, end := int32(0), node.height
	for ; from < end; {
		middle := (from + end) / 2
		node := b.bestChain.NodeByHeight(middle)

		if node.Round() == round && node.Slot() == slot {
			return node
		}
		if node.Round() < round || (node.Round() == round && node.Slot() < slot) {
			from = middle + 1
		} else {
			end = middle
		}
	}

	return nil
}

// ProcessBlock is the main workhorse for handling insertion of new blocks into
// the block chain.  It includes functionality such as rejecting duplicate
// blocks, ensuring blocks follow all rules, orphan handling, and insertion into
// the block chain along with best chain selection and reorganization.
//
// When no errors occurred during processing, the first return value indicates
// whether or not the block is on the main chain and the second indicates
// whether or not the block is an orphan.
//
// This function is safe for concurrent access.
func (b *BlockChain) ProcessBlock(block *asiutil.Block, vblock *asiutil.VBlock,
	receipts types.Receipts, logs []*types.Log,
	flags common.BehaviorFlags) (bool, bool, error) {
	b.chainLock.Lock()
	defer b.chainLock.Unlock()
	log.Debug("ProcessBlock get lock",block.Hash())
	blockHash := block.Hash()

	// The block must not already exist in the main chain or side chains.
	exists, err := b.blockExists(blockHash)
	if err != nil {
		log.Debugf("Processing block err %v", err)
		return false, false, err
	}
	if exists {
		str := fmt.Sprintf("already have block %v", blockHash)
		return false, false, ruleError(ErrDuplicateBlock, str)
	}

	// The block must not already exist as an orphan.
	if _, exists := b.orphans[*blockHash]; exists {
		str := fmt.Sprintf("already have block (orphan) %v", blockHash)
		return false, false, ruleError(ErrDuplicateBlock, str)
	}

	fastAdd := flags&common.BFFastAdd == common.BFFastAdd
	if !fastAdd {
		// parent may results nil
		parent := b.index.LookupNode(&block.MsgBlock().Header.PrevBlock)
		// Perform preliminary sanity checks on the block and its transactions.
		err = checkBlockSanity(block, parent, flags)
		if err != nil {
			log.Debugf("Processing block err %v", err)
			return false, false, err
		}
	}

	// Find the previous checkpoint and perform some additional checks based
	// on the checkpoint.  This provides a few nice properties such as
	// preventing old side chain blocks before the last checkpoint,
	// rejecting easy to mine, but otherwise bogus, blocks that could be
	// used to eat memory, and ensuring expected (versus claimed) proof of
	// work requirements since the previous checkpoint are met.
	blockHeader := &block.MsgBlock().Header
	checkpointNode, err := b.findPreviousCheckpoint()
	if err != nil {
		log.Debugf("Processing block err %v", err)
		return false, false, err
	}
	if checkpointNode != nil {
		// Ensure the block timestamp is after the checkpoint timestamp.
		if blockHeader.Timestamp < checkpointNode.timestamp {
			str := fmt.Sprintf("block %v has timestamp %v before "+
				"last checkpoint timestamp %v", blockHash,
				blockHeader.Timestamp, checkpointNode.timestamp)
			return false, false, ruleError(ErrCheckpointTimeTooOld, str)
		}
	}

	// Handle orphan blocks.
	prevHash := &blockHeader.PrevBlock
	prevHashExists, err := b.blockExists(prevHash)
	if err != nil {
		log.Debugf("Processing block err %v", err)
		return false, false, err
	}
	if !prevHashExists {
		log.Infof("Adding orphan block(%d) %v with parent %v", blockHeader.Height, blockHash, prevHash)
		b.addOrphanBlock(block)
		return false, true, nil
	}

	// The block has passed all context independent checks and appears sane
	// enough to potentially accept it into the block chain.
	isMainChain, err := b.maybeAcceptBlock(block, vblock, receipts, logs, flags)
	if err != nil {
		log.Debugf("Reject block %d %v with parent %v %v", blockHeader.Height, blockHash, prevHash, err)
		return false, false, err
	}

	// Accept any orphan blocks that depend on this block (they are
	// no longer orphans) and repeat for those accepted blocks until
	// there are no more.
	err = b.processOrphans(blockHash, flags)
	if err != nil {
		log.Debugf("processOrphans %d %v with parent %v %v", blockHeader.Height, blockHash, prevHash, err)
		return false, false, err
	}

	log.Debugf("Accepted block main=%v height=%v hash=%v ts=%v prev=%v", isMainChain, blockHeader.Height,
		blockHash, blockHeader.Timestamp, prevHash)

	return isMainChain, false, nil
}

// AddressVerifySignature check whether or not the address match the signature
func AddressVerifySignature(data []byte, expectedAddr *common.Address, signs []byte) error {
	if len(signs) != protos.HashSignLen {
		errStr := fmt.Sprintf("the length of sig do not equal with HashSignLen!")
		return ruleError(ErrBadSigLength, errStr)
	}
	if len(data) != common.HashLength {
		errStr := fmt.Sprintf("the length of sig do not equal with HashSignLen!")
		return ruleError(ErrBadHashLength, errStr)
	}
	pubkey, err := crypto.SigToPub(data, signs)
	if err != nil {
		errStr := fmt.Sprintf("invalid signature data: data is %s, sigs is %s ", hex.EncodeToString(data), hex.EncodeToString(signs))
		return ruleError(ErrInvalidSigData, errStr)
	}

	pubkeyBytes := crypto.CompressPubkey(pubkey)
	address, _ := common.NewAddressWithId(common.PubKeyHashAddrID, common.Hash160(pubkeyBytes))
	if bytes.Equal(address[:], expectedAddr[:]) {
		return nil
	}
	log.Warnf("sig verify failed, address = %v, expectAddr = %v", address, expectedAddr)
	errStr := fmt.Sprintf("sig do not match the key")
	return ruleError(ErrSigAndKeyMismatch, errStr)
}
