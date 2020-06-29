// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"fmt"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
)

// maybeAcceptBlock potentially accepts a block into the block chain and, if
// accepted, returns whether or not it is on the main chain.  It performs
// several validation checks which depend on its position within the block chain
// before adding it.  The block is expected to have already gone through
// ProcessBlock before calling this function with it.
//
// The flags are also passed to checkBlockContext and connectBestChain.  See
// their documentation for how the flags modify their behavior.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) maybeAcceptBlock(block *asiutil.Block, vblock *asiutil.VBlock,
	receipts types.Receipts, logs []*types.Log,
	flags common.BehaviorFlags) (bool, error) {
	// The height of this block is one more than the referenced previous
	// block.
	prevHash := &block.MsgBlock().Header.PrevBlock
	prevNode := b.index.LookupNode(prevHash)
	if prevNode == nil {
		str := fmt.Sprintf("previous block %s is unknown", prevHash)
		return false, ruleError(ErrPreviousBlockUnknown, str)
	} else if b.index.NodeStatus(prevNode).KnownInvalid() {
		str := fmt.Sprintf("previous block %s is known to be invalid", prevHash)
		return false, ruleError(ErrInvalidAncestorBlock, str)
	}

	fastAdd := flags&common.BFFastAdd == common.BFFastAdd

	var err error
	blockHeader := &block.MsgBlock().Header
	round := prevNode.round
	for round.Round < blockHeader.Round {
		round, err = b.roundManager.GetNextRound(round)
		if err != nil {
			return false, err
		}
	}

	if !fastAdd {
		// The block must pass all of the validation rules which depend on the
		// position of the block within the block chain.
		err = b.checkBlockContext(block, prevNode, round, flags)
		if err != nil {
			return false, err
		}
	}

	// Insert the block into the database if it's not already there.  Even
	// though it is possible the block will ultimately fail to connect, it
	// has already passed all proof-of-work and validity tests which means
	// it would be prohibitively expensive for an attacker to fill up the
	// disk with a bunch of blocks that fail to connect.  This is necessary
	// since it allows block download to be decoupled from the much more
	// expensive connection logic.  It also has some other nice properties
	// such as making blocks that never become part of the main chain or
	// blocks that fail to connect available for further analysis.
	err = b.db.Update(func(dbTx database.Tx) error {
		dberr := dbStoreBlock(dbTx, block)
		if dberr != nil {
			return dberr
		}
		if fastAdd && vblock != nil {
			dberr = dbStoreVBlock(dbTx, vblock)
		}
		return dberr
	})
	if err != nil {
		return false, err
	}

	// Create a new block node for the block and add it to the node index. Even
	// if the block ultimately gets connected to the main chain, it starts out
	// on a side chain.
	newNode := newBlockNode(round, blockHeader, prevNode)
	newNode.status = statusDataStored
	if newNode.round.Round != prevNode.round.Round {
		newNode.status |= statusFirstInRound
	}

	b.index.AddNode(newNode)
	err = b.index.flushToDB()
	if err != nil {
		return false, err
	}

	// Connect the passed block to the chain while respecting proper chain
	// selection according to the chain with the most proof of work.  This
	// also handles validation of the transaction scripts.
	log.Debug("enter connectBestChain",block.Hash())
	isMainChain, err := b.connectBestChain(newNode, block, vblock, receipts, logs, flags)
	if err != nil {
		return false, err
	}

	// Notify the caller that the new block was accepted into the block
	// chain.  The caller would typically want to react by relaying the
	// inventory to other peers.
	b.chainLock.Unlock()
	b.sendNotification(NTBlockAccepted, block)
	b.chainLock.Lock()

	return isMainChain, nil
}
