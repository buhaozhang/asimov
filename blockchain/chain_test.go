// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"reflect"
	"testing"
)

// TestHaveBlock tests the HaveBlock API to ensure proper functionality.
func TestHaveBlock(t *testing.T) {
	// Load up blocks such that there is a side chain.
	// (genesis block) -> 1 -> 2 -> 3 -> 4 -> 5
	//                          \-> 3a

	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e", //privateKey0
	}
	testRoundSize := uint16(10)
	accList, netParam, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, testRoundSize)
	defer teardownFunc()

	validators, filters, _ := chain.GetValidatorsByNode(1, chain.bestChain.tip())

	mainChainBlkNums := 5
	forkBlkHeightIdx := 3
	orphanBlkHeightIdx := 5

	bestChainHashList := make([]common.Hash, 0)
	node0 := chain.bestChain.NodeByHeight(0)
	bestChainHashList = append(bestChainHashList, node0.hash)
	var forkHash common.Hash
	var orphanHash common.Hash
	curSlot := uint16(0)
	curEpoch := uint32(1)
	//create block:
	for i := 0; i < mainChainBlkNums; i++ {
		if i != 0 {
			if i == int(netParam.RoundSize) {
				curSlot = 0
				curEpoch += 1
				validators, filters, _ = chain.GetValidatorsByNode(curEpoch, chain.bestChain.tip())
			} else {
				curSlot ++
			}
		}

		var block *asiutil.Block
		var frokBlock *asiutil.Block
		if i == (orphanBlkHeightIdx - 1) {
			block, _, err = createAndSignBlock(netParam, accList, validators, filters, chain, uint32(curEpoch),
				uint16(curSlot), chain.bestChain.height(), protos.Asset{0, 0}, 0,
				validators[curSlot], nil, 0, chain.bestChain.tip())
			if err != nil {
				t.Errorf("create block error %v", err)
			}
			//create orphanBlock:
			orphanBlock := block
			orphanBlock.MsgBlock().Header.PrevBlock = common.Hash{0x01,}
			orphanHash = *orphanBlock.Hash()
		} else {
			//create block:
			block, _, err = createAndSignBlock(netParam, accList, validators, filters, chain, uint32(curEpoch),
				uint16(curSlot), chain.bestChain.height(), protos.Asset{0, 0}, 0,
				validators[curSlot], nil, 0, chain.bestChain.tip())
			if err != nil {
				t.Errorf("create block error %v", err)
			}

			if i == int(forkBlkHeightIdx - 1) {
				frokBlock, _, err = createAndSignBlock(netParam, accList, validators, filters, chain, uint32(curEpoch),
					uint16(curSlot), chain.bestChain.height(), protos.Asset{0, 0}, 0,
					validators[curSlot], nil, int32(i+1), chain.bestChain.tip())
				if err != nil {
					t.Errorf("create block error %v", err)
				}
				forkHash = *frokBlock.Hash()
			}
		}
		// Insert the block to bestChain:
		_, isOrphan, err := chain.ProcessBlock(block, nil, nil, nil, common.BFNone)
		if err != nil {
			t.Errorf("ProcessBlock err %v", err)
		}
		log.Infof("isOrphan = %v", isOrphan)
		if i == int(forkBlkHeightIdx - 1) {
			_, forkIsOrphan, err := chain.ProcessBlock(frokBlock, nil, nil, nil, common.BFNone)
			if err != nil {
				t.Errorf("ProcessBlock err %v", err)
			}
			log.Infof("frokBlock isOrphan = %v", forkIsOrphan)
		}

		if !isOrphan {
			node := chain.bestChain.nodeByHeight(chain.bestChain.height())
			bestChainHashList = append(bestChainHashList, node.hash)
		}
	}

	mainblklen := len(bestChainHashList)
	tmpHash := bestChainHashList[mainblklen-1]
	otherHash := append(tmpHash[:1], tmpHash[:common.HashLength-1]...)
	var resultHash common.Hash
	copy(resultHash[:], otherHash)

	tests := []struct {
		hash common.Hash
		want bool
	}{
		//blockHash in bestchain: test0
		{
			bestChainHashList[mainChainBlkNums-2],
			true,
		},
		//block hash in fork chain: test1
		{
			forkHash,
			true,
		},
		//orphan block hash: test2
		{
			orphanHash,
			true,
		},
		//Random hashes should not be available
		{
			resultHash,
			false,
		},
	}

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		t.Logf("start test %d", i)
		result, err := chain.HaveBlock(&test.hash)
		if err != nil {
			t.Errorf("HaveBlock #%d unexpected error: %v", i, err)
		}
		if result != test.want {
			t.Errorf("HaveBlock #%d got %v want %v", i, result, test.want)
			continue
		}
	}
}

// nodeHashes is a convenience function that returns the hashes for all of the
// passed indexes of the provided nodes.  It is used to construct expected hash
// slices in the tests.
func nodeHashes(nodes []*blockNode, indexes ...int) []common.Hash {
	hashes := make([]common.Hash, 0, len(indexes))
	for _, idx := range indexes {
		hashes = append(hashes, nodes[idx].hash)
	}
	return hashes
}

// nodeHeaders is a convenience function that returns the headers for all of
// the passed indexes of the provided nodes.  It is used to construct expected
// located headers in the tests.
func nodeHeaders(nodes []*blockNode, indexes ...int) []protos.BlockHeader {
	headers := make([]protos.BlockHeader, 0, len(indexes))
	for _, idx := range indexes {
		headers = append(headers, nodes[idx].Header())
	}
	return headers
}

// TestLocateInventory ensures that locating inventory via the LocateHeaders and
// LocateBlocks functions behaves as expected.
func TestLocateInventory(t *testing.T) {
	// Construct a synthetic block chain with a block index consisting of
	// the following structure.
	// 	genesis -> 1 -> 2 -> ... -> 15 -> 16  -> 17  -> 18
	// 	                              \-> 16a -> 17a
	tip := tstTip
	chain, teardownFunc, err := newFakeChain(&chaincfg.MainNetParams)
	if err != nil || chain == nil {
		t.Errorf("newFakeChain error %v", err)
	}
	defer teardownFunc()

	//the node in branch1Nodes is also included in branch0Nodes
	branch0Nodes := chainedNodes(chain.bestChain.Genesis(), 18, 0)
	branch1Nodes := chainedNodes(branch0Nodes[14], 2, 1)
	for _, node := range branch0Nodes {
		chain.index.AddNode(node)
	}
	for _, node := range branch1Nodes {
		chain.index.AddNode(node)
	}
	chain.bestChain.SetTip(tip(branch0Nodes))

	// Create chain views for different branches of the overall chain to
	// simulate a local and remote node on different parts of the chain.
	localView := newChainView(tip(branch0Nodes))
	remoteView := newChainView(tip(branch1Nodes))

	// Create a chain view for a completely unrelated block chain to
	// simulate a remote node on a totally different chain.
	unrelatedBranchNodes := chainedNodes(nil, 5, 2)
	unrelatedView := newChainView(tip(unrelatedBranchNodes))

	tests := []struct {
		name       string
		locator    BlockLocator         // locator for requested inventory
		hashStop   common.Hash          // stop hash for locator
		maxAllowed uint32               // max to locate, 0 = protos const
		headers    []protos.BlockHeader // expected located headers
		hashes     []common.Hash        // expected located hashes
	}{
		//test0:
		{
			// Empty block locators and unknown stop hash.  No
			// inventory should be located.
			name:     "no locators, no stop",
			locator:  nil,
			hashStop: common.Hash{},
			headers:  nil,
			hashes:   nil,
		},
		//test1:
		{
			// Empty block locators and stop hash in side chain.
			// The expected result is the requested block.
			name:     "no locators, stop in side",
			locator:  nil,
			hashStop: tip(branch1Nodes).hash,
			headers:  nodeHeaders(branch1Nodes, 1),
			hashes:   nodeHashes(branch1Nodes, 1),
		},
		//test2:
		{
			// Empty block locators and stop hash in main chain.
			// The expected result is the requested block.
			name:     "no locators, stop in main",
			locator:  nil,
			hashStop: branch0Nodes[12].hash,
			headers:  nodeHeaders(branch0Nodes, 12),
			hashes:   nodeHashes(branch0Nodes, 12),
		},
		//test3:
		{
			// Locators based on remote being on side chain and a
			// stop hash local node doesn't know about.  The
			// expected result is the blocks after the fork point in
			// the main chain and the stop hash has no effect.
			name:     "remote side chain, unknown stop",
			locator:  remoteView.BlockLocator(nil),
			hashStop: common.Hash{0x01},
			headers:  nodeHeaders(branch0Nodes, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 15, 16, 17),
		},
		//test4:
		{
			// Locators based on remote being on side chain and a
			// stop hash in side chain.  The expected result is the
			// blocks after the fork point in the main chain and the
			// stop hash has no effect.
			name:     "remote side chain, stop in side",
			locator:  remoteView.BlockLocator(nil),
			hashStop: tip(branch1Nodes).hash,
			headers:  nodeHeaders(branch0Nodes, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 15, 16, 17),
		},
		//test5:
		{
			// Locators based on remote being on side chain and a
			// stop hash in main chain, but before fork point.  The
			// expected result is the blocks after the fork point in
			// the main chain and the stop hash has no effect.
			name:     "remote side chain, stop in main before",
			locator:  remoteView.BlockLocator(nil),
			hashStop: branch0Nodes[13].hash,
			headers:  nodeHeaders(branch0Nodes, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 15, 16, 17),
		},
		//test6:
		{
			// Locators based on remote being on side chain and a
			// stop hash in main chain, but exactly at the fork
			// point.  The expected result is the blocks after the
			// fork point in the main chain and the stop hash has no
			// effect.
			name:     "remote side chain, stop in main exact",
			locator:  remoteView.BlockLocator(nil),
			hashStop: branch0Nodes[14].hash,
			headers:  nodeHeaders(branch0Nodes, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 15, 16, 17),
		},
		//test7:-----------
		{
			// Locators based on remote being on side chain and a
			// stop hash in main chain just after the fork point.
			// The expected result is the blocks after the fork
			// point in the main chain up to and including the stop
			// hash.
			name:     "remote side chain, stop in main after",
			locator:  remoteView.BlockLocator(nil),
			hashStop: branch0Nodes[15].hash,
			headers:  nodeHeaders(branch0Nodes, 15),
			hashes:   nodeHashes(branch0Nodes, 15),
		},
		//test8:------------
		{
			// Locators based on remote being on side chain and a
			// stop hash in main chain some time after the fork
			// point.  The expected result is the blocks after the
			// fork point in the main chain up to and including the
			// stop hash.
			name:     "remote side chain, stop in main after more",
			locator:  remoteView.BlockLocator(nil),
			hashStop: branch0Nodes[16].hash,
			headers:  nodeHeaders(branch0Nodes, 15, 16),
			hashes:   nodeHashes(branch0Nodes, 15, 16),
		},
		//test9:
		{
			// Locators based on remote being on main chain in the
			// past and a stop hash local node doesn't know about.
			// The expected result is the blocks after the known
			// point in the main chain and the stop hash has no
			// effect.
			name:     "remote main chain past, unknown stop",
			locator:  localView.BlockLocator(branch0Nodes[12]),
			hashStop: common.Hash{0x01},
			headers:  nodeHeaders(branch0Nodes, 13, 14, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 13, 14, 15, 16, 17),
		},
		//test10:
		{
			// Locators based on remote being on main chain in the
			// past and a stop hash in a side chain.  The expected
			// result is the blocks after the known point in the
			// main chain and the stop hash has no effect.
			name:     "remote main chain past, stop in side",
			locator:  localView.BlockLocator(branch0Nodes[12]),
			hashStop: tip(branch1Nodes).hash,
			headers:  nodeHeaders(branch0Nodes, 13, 14, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 13, 14, 15, 16, 17),
		},
		//test11:
		{
			// Locators based on remote being on main chain in the
			// past and a stop hash in the main chain before that
			// point.  The expected result is the blocks after the
			// known point in the main chain and the stop hash has
			// no effect.
			name:     "remote main chain past, stop in main before",
			locator:  localView.BlockLocator(branch0Nodes[12]),
			hashStop: branch0Nodes[11].hash,
			headers:  nodeHeaders(branch0Nodes, 13, 14, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 13, 14, 15, 16, 17),
		},
		//test12:
		{
			// Locators based on remote being on main chain in the
			// past and a stop hash in the main chain exactly at that
			// point.  The expected result is the blocks after the
			// known point in the main chain and the stop hash has
			// no effect.
			name:     "remote main chain past, stop in main exact",
			locator:  localView.BlockLocator(branch0Nodes[12]),
			hashStop: branch0Nodes[12].hash,
			headers:  nodeHeaders(branch0Nodes, 13, 14, 15, 16, 17),
			hashes:   nodeHashes(branch0Nodes, 13, 14, 15, 16, 17),
		},
		//test13:
		{
			// Locators based on remote being on main chain in the
			// past and a stop hash in the main chain just after
			// that point.  The expected result is the blocks after
			// the known point in the main chain and the stop hash
			// has no effect.
			name:     "remote main chain past, stop in main after",
			locator:  localView.BlockLocator(branch0Nodes[12]),
			hashStop: branch0Nodes[13].hash,
			headers:  nodeHeaders(branch0Nodes, 13),
			hashes:   nodeHashes(branch0Nodes, 13),
		},
		//test14:-------------
		{
			// Locators based on remote being on main chain in the
			// past and a stop hash in the main chain some time
			// after that point.  The expected result is the blocks
			// after the known point in the main chain and the stop
			// hash has no effect.
			name:     "remote main chain past, stop in main after more",
			locator:  localView.BlockLocator(branch0Nodes[12]),
			hashStop: branch0Nodes[15].hash,
			headers:  nodeHeaders(branch0Nodes, 13, 14, 15),
			hashes:   nodeHashes(branch0Nodes, 13, 14, 15),
		},
		//test15:
		{
			// Locators based on remote being at exactly the same
			// point in the main chain and a stop hash local node
			// doesn't know about.  The expected result is no
			// located inventory.
			name:     "remote main chain same, unknown stop",
			locator:  localView.BlockLocator(nil),
			hashStop: common.Hash{0x01},
			headers:  nil,
			hashes:   nil,
		},
		//test16:
		{
			// Locators based on remote being at exactly the same
			// point in the main chain and a stop hash at exactly
			// the same point.  The expected result is no located
			// inventory.
			name:     "remote main chain same, stop same point",
			locator:  localView.BlockLocator(nil),
			hashStop: tip(branch0Nodes).hash,
			headers:  nil,
			hashes:   nil,
		},
		//test17:
		{
			// Locators from remote that don't include any blocks
			// the local node knows.  This would happen if the
			// remote node is on a completely separate chain that
			// isn't rooted with the same genesis block.  The
			// expected result is the blocks after the genesis
			// block.
			name:     "remote unrelated chain",
			locator:  unrelatedView.BlockLocator(nil),
			hashStop: common.Hash{},
			headers: nodeHeaders(branch0Nodes, 0, 1, 2, 3, 4, 5, 6,
				7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
			hashes: nodeHashes(branch0Nodes, 0, 1, 2, 3, 4, 5, 6,
				7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
		},
		//test18:
		{
			// Locators from remote for second block in main chain
			// and no stop hash, but with an overridden max limit.
			// The expected result is the blocks after the second
			// block limited by the max.
			name:       "remote genesis",
			locator:    locatorHashes(branch0Nodes, 0),
			hashStop:   common.Hash{},
			maxAllowed: 3,
			headers:    nodeHeaders(branch0Nodes, 1, 2, 3),
			hashes:     nodeHashes(branch0Nodes, 1, 2, 3),
		},
		//test19:
		{
			// Poorly formed locator.
			//
			// Locator from remote that only includes a single
			// block on a side chain the local node knows.  The
			// expected result is the blocks after the genesis
			// block since even though the block is known, it is on
			// a side chain and there are no more locators to find
			// the fork point.
			name:     "weak locator, single known side block",
			locator:  locatorHashes(branch1Nodes, 1),
			hashStop: common.Hash{},
			headers: nodeHeaders(branch0Nodes, 0, 1, 2, 3, 4, 5, 6,
				7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
			hashes: nodeHashes(branch0Nodes, 0, 1, 2, 3, 4, 5, 6,
				7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
		},
		//test20:
		{
			// Poorly formed locator.
			//
			// Locator from remote that only includes multiple
			// blocks on a side chain the local node knows however
			// none in the main chain.  The expected result is the
			// blocks after the genesis block since even though the
			// blocks are known, they are all on a side chain and
			// there are no more locators to find the fork point.
			name:     "weak locator, multiple known side blocks",
			locator:  locatorHashes(branch1Nodes, 1),
			hashStop: common.Hash{},
			headers: nodeHeaders(branch0Nodes, 0, 1, 2, 3, 4, 5, 6,
				7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
			hashes: nodeHashes(branch0Nodes, 0, 1, 2, 3, 4, 5, 6,
				7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17),
		},
		//test21:
		{
			// Poorly formed locator.
			//
			// Locator from remote that only includes multiple
			// blocks on a side chain the local node knows however
			// none in the main chain but includes a stop hash in
			// the main chain.  The expected result is the blocks
			// after the genesis block up to the stop hash since
			// even though the blocks are known, they are all on a
			// side chain and there are no more locators to find the
			// fork point.
			name:     "weak locator, multiple known side blocks, stop in main",
			locator:  locatorHashes(branch1Nodes, 1),
			hashStop: branch0Nodes[5].hash,
			headers:  nodeHeaders(branch0Nodes, 0, 1, 2, 3, 4, 5),
			hashes:   nodeHashes(branch0Nodes, 0, 1, 2, 3, 4, 5),
		},
	}
	for i, test := range tests {
		t.Logf("==========test case %d==========", i)
		// Ensure the expected headers are located.
		var headers []protos.BlockHeader
		if test.maxAllowed != 0 {
			// Need to use the unexported function to override the
			// max allowed for headers.
			chain.chainLock.RLock()
			headers = chain.locateHeaders(test.locator, &test.hashStop, test.maxAllowed)
			chain.chainLock.RUnlock()
		} else {
			headers = chain.LocateHeaders(test.locator, &test.hashStop)
		}
		if !reflect.DeepEqual(headers, test.headers) {
			t.Errorf("%s: unxpected headers -- got %v, want %v", test.name, headers, test.headers)
			continue
		}

		// Ensure the expected block hashes are located.
		maxAllowed := uint32(protos.MaxBlocksPerMsg)
		if test.maxAllowed != 0 {
			maxAllowed = test.maxAllowed
		}
		hashes := chain.LocateBlocks(test.locator, &test.hashStop, maxAllowed)
		if !reflect.DeepEqual(hashes, test.hashes) {
			t.Errorf("%s: unxpected hashes -- got %v, want %v", test.name, hashes, test.hashes)
			continue
		}
	}
}

// TestHeightToHashRange ensures that fetching a range of block hashes by start
// height and end hash works as expected.
func TestHeightToHashRange(t *testing.T) {
	// Construct a synthetic block chain with a block index consisting of
	// the following structure.
	// 	genesis -> 1 -> 2 -> ... -> 15 -> 16  -> 17  -> 18
	// 	                              \-> 16a -> 17a -> 18a (unvalidated)
	tip := tstTip
	chain, teardownFunc, err := newFakeChain(&chaincfg.MainNetParams)
	if err != nil || chain == nil {
		t.Errorf("newFakeChain error %v", err)
	}
	defer teardownFunc()

	branch0Nodes := chainedNodes(chain.bestChain.Genesis(), 18, 0)
	branch1Nodes := chainedNodes(branch0Nodes[14], 3, 1)
	for _, node := range branch0Nodes {
		chain.index.SetStatusFlags(node, statusValid)
		chain.index.AddNode(node)
	}
	for _, node := range branch1Nodes {
		if node.height < 18 {
			chain.index.SetStatusFlags(node, statusValid)
		}
		chain.index.AddNode(node)
	}
	chain.bestChain.SetTip(tip(branch0Nodes))

	tests := []struct {
		name        string
		startHeight int32         // locator for requested inventory
		endHash     common.Hash   // stop hash for locator
		maxResults  int           // max to locate, 0 = protos const
		hashes      []common.Hash // expected located hashes
		expectError bool
	}{
		{
			name:        "blocks below tip",
			startHeight: 11,
			endHash:     branch0Nodes[14].hash,
			maxResults:  10,
			hashes:      nodeHashes(branch0Nodes, 10, 11, 12, 13, 14),
		},
		{
			name:        "blocks on main chain",
			startHeight: 15,
			endHash:     branch0Nodes[17].hash,
			maxResults:  10,
			hashes:      nodeHashes(branch0Nodes, 14, 15, 16, 17),
		},
		{
			name:        "blocks on stale chain",
			startHeight: 15,
			endHash:     branch1Nodes[1].hash,
			maxResults:  10,
			hashes: append(nodeHashes(branch0Nodes, 14),
				nodeHashes(branch1Nodes, 0, 1)...),
		},
		{
			name:        "invalid start height",
			startHeight: 19,
			endHash:     branch0Nodes[17].hash,
			maxResults:  10,
			expectError: true,
		},
		{
			name:        "too many results",
			startHeight: 1,
			endHash:     branch0Nodes[17].hash,
			maxResults:  10,
			expectError: true,
		},
		{
			name:        "unvalidated block",
			startHeight: 15,
			endHash:     branch1Nodes[2].hash,
			maxResults:  10,
			expectError: true,
		},
	}
	for _, test := range tests {
		hashes, err := chain.HeightToHashRange(test.startHeight, &test.endHash, test.maxResults)
		if err != nil {
			if !test.expectError {
				t.Errorf("%s: unexpected error: %v", test.name, err)
			}
			continue
		}

		if !reflect.DeepEqual(hashes, test.hashes) {
			t.Errorf("%s: unxpected hashes -- got %v, want %v", test.name, hashes, test.hashes)
		}
	}
}

// TestIntervalBlockHashes ensures that fetching block hashes at specified
// intervals by end hash works as expected.
func TestIntervalBlockHashes(t *testing.T) {
	// Construct a synthetic block chain with a block index consisting of
	// the following structure.
	// 	genesis -> 1 -> 2 -> ... -> 15 -> 16  -> 17  -> 18
	// 	                              \-> 16a -> 17a -> 18a (unvalidated)
	tip := tstTip
	chain, teardownFunc, err := newFakeChain(&chaincfg.MainNetParams)
	if err != nil || chain == nil {
		t.Errorf("newFakeChain error %v", err)
	}
	defer teardownFunc()

	branch0Nodes := chainedNodes(chain.bestChain.Genesis(), 18, 0)
	branch1Nodes := chainedNodes(branch0Nodes[14], 3, 1)
	for _, node := range branch0Nodes {
		chain.index.SetStatusFlags(node, statusValid)
		chain.index.AddNode(node)
	}
	for _, node := range branch1Nodes {
		if node.height < 18 {
			chain.index.SetStatusFlags(node, statusValid)
		}
		chain.index.AddNode(node)
	}
	chain.bestChain.SetTip(tip(branch0Nodes))

	tests := []struct {
		name        string
		endHash     common.Hash
		interval    int
		hashes      []common.Hash
		expectError bool
	}{
		{
			name:     "blocks on main chain",
			endHash:  branch0Nodes[17].hash,
			interval: 8,
			hashes:   nodeHashes(branch0Nodes, 7, 15),
		},
		{
			name:     "blocks on stale chain",
			endHash:  branch1Nodes[1].hash,
			interval: 8,
			hashes: append(nodeHashes(branch0Nodes, 7),
				nodeHashes(branch1Nodes, 0)...),
		},
		{
			name:     "no results",
			endHash:  branch0Nodes[17].hash,
			interval: 20,
			hashes:   []common.Hash{},
		},
		{
			name:        "unvalidated block",
			endHash:     branch1Nodes[2].hash,
			interval:    8,
			expectError: true,
		},
	}
	for _, test := range tests {
		hashes, err := chain.IntervalBlockHashes(&test.endHash, test.interval)
		if err != nil {
			if !test.expectError {
				t.Errorf("%s: unexpected error: %v", test.name, err)
			}
			continue
		}

		if !reflect.DeepEqual(hashes, test.hashes) {
			t.Errorf("%s: unxpected hashes -- got %v, want %v", test.name, hashes, test.hashes)
		}
	}
}


func TestReorganizeChain(t *testing.T) {
	parivateKeyList := []string{
		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e", //privateKey0
		"0xd07f68f78fc58e3dc8ea72ff69784aa9542c452a4ee66b2665fa3cccb48441c2", //privateKey1
		"0x77366e621236e71a77236e0858cd652e92c3af0908527b5bd1542992c4d7cace", //privateKey2
	}
	roundSize := uint16(3)
	accList, netParam, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, roundSize)
	defer teardownFunc()

	genesisNode, _ := chain.GetNodeByHeight(0)
	validators, filters, _ := chain.GetValidatorsByNode(1, chain.bestChain.tip())

	epoch := uint32(1)
	slot := uint16(0)
	//create 2 blocks to bestChain:
	for i := int32(0); i < 2; i++ {
		if i != 0 {
			if i == int32(roundSize) {
				slot = 0
				epoch++
				validators, filters, _ = chain.GetValidatorsByNode(epoch, chain.bestChain.tip())
			} else {
				slot++
			}
		}
		normalTxList := make([]*asiutil.Tx, 0)
		if i == 1 {
			//create a tx using validators[0]:
			index := 0
			node1, _ := chain.GetNodeByHeight(1)
			coinbaseAddr := node1.coinbase
			for k := 0; k < len(netParam.GenesisCandidates); k++ {
				if netParam.GenesisCandidates[k] == coinbaseAddr {
					index = k
					break
				}
			}
			normalTx, _ := chain.createNormalTx(parivateKeyList[index], protos.Asset{0, 0}, *validators[0],
			2000000000, 5000000, 100000, nil)
			normalTxList = append(normalTxList, normalTx)
		}
		block, _, err := createAndSignBlock(netParam, accList, validators, filters, chain, epoch, slot, int32(i),
			protos.Asset{0, 0}, 0, validators[slot], normalTxList,
			0, chain.bestChain.tip())
		if err != nil {
			t.Errorf("create block error %v", err)
		}
		isMain, isOrphan, checkErr := chain.ProcessBlock(block, nil, nil, nil, 1)
		if checkErr != nil {
			t.Errorf("ProcessBlock error %v", checkErr)
		}
		log.Infof("isMain = %v, isOrphan = %v", isMain, isOrphan)
	}

	//create 3 side chain block:
	{
		sideChainBlockList := make([]asiutil.Block,0)
		tmpValidator := validators
		tmpFilters := filters
		tmpEpoch := uint32(1)
		tmpSlotIndex := uint16(0)
		tmpPreHash := genesisNode.hash
		sideChainBestNode := genesisNode
		for i:=0; i<3; i++ {
			if i != 0 {
				if i == int(netParam.RoundSize) {
					tmpSlotIndex = 0
					tmpEpoch += 1
					tmpValidator, tmpFilters, _ = chain.GetValidatorsByNode(tmpEpoch,sideChainBestNode)
					log.Infof("validators2 = %v",tmpValidator)
				} else {
					tmpSlotIndex ++
				}
			}
			sideChainBlock, sideChainNode, err := createAndSignBlock(netParam, accList, tmpValidator, tmpFilters,
				chain, tmpEpoch, tmpSlotIndex, int32(i), protos.Asset{0,0}, 0,
				tmpValidator[tmpSlotIndex],nil,int32(i+1),sideChainBestNode)
			if err != nil {
				t.Errorf("create block error %v", err)
			}
			sideChainBlock.MsgBlock().Header.PrevBlock = tmpPreHash
			sideChainBlockList = append(sideChainBlockList, *sideChainBlock)
			sideChainBestNode = sideChainNode
			tmpPreHash = sideChainBestNode.hash

			//store block:
			err = chain.db.Update(func(dbTx database.Tx) error {
				return dbStoreBlock(dbTx, sideChainBlock)
			})
			if err != nil {
				t.Errorf("dbStoreBlock error %v", err)
			}
		}

		needReorg := sideChainBestNode.weight > chain.bestChain.tip().weight
		if !needReorg {
			t.Errorf("reorganizeChain error: needReorg is false: %v", err)
		} else {
			detachNodes, attachNodes := chain.getReorganizeNodes(sideChainBestNode)

			chain.chainLock.Lock()
			defer chain.chainLock.Unlock()

			// Reorganize the chain.
			log.Infof("REORGANIZE: Block %v is causing a reorganize.", sideChainBestNode.hash)
			err = chain.reorganizeChain(detachNodes, attachNodes)
			if err != nil {
				t.Errorf("reorganizeChain error %v", err)
			}
		}

		//check whether the utxo is correct:
		value := CalcBlockSubsidy(1, chaincfg.ActiveNetParams.Params)
		value = value - int64(float64(value)*common.CoreTeamPercent)
		view := txo.NewUtxoViewpoint()
		var prevOuts *[]protos.OutPoint
		node1, _ := chain.GetNodeByHeight(1)
		coinbaseAddr := node1.coinbase
		prevOuts, err = chain.fetchUtxoViewByAddressAndAsset(view, coinbaseAddr[:], &protos.Asset{0, 0})
		if err != nil {
			t.Errorf("FetchUtxoViewByAddressAndAsset error %v", err)
		}
		for _, prevOut := range *prevOuts {
			entry := view.LookupEntry(prevOut)
			if entry == nil || entry.IsSpent() {
				t.Errorf("LookupEntry error %v", err)
			}
			if entry.Amount() != value {
				t.Errorf("test reorganizeChain error: the result of utxo do not as expect")
			}
		}
	}
}
//
//func TestConnectTransactions(t *testing.T) {
//	parivateKeyList := []string{
//		"0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e", //privateKey0
//	}
//
//	roundSize := uint16(6)
//	accList, _, chain, teardownFunc, err := createFakeChainByPrivateKeys(parivateKeyList, roundSize)
//	defer teardownFunc()
//
//	payAddrPkScript, _ := txscript.PayToAddrScript(accList[0].Address)
//
//	validators, filters, _ := chain.GetValidatorsByNode(1, chain.bestChain.tip())
//	weight := filters[*validators[0]]
//	validatorPrivateKey := parivateKeyList[0]
//
//	var testAsset = []protos.Asset{
//		{0, 0},
//		{1, 1},
//	}
//	var testAmount = []int64{
//		10000000000,
//		111111111,
//	}
//
//	blocks := make([]*asiutil.Block, 0)
//	//add 2 block to bestChain:
//	for i := 0; i < 2; i++ {
//		block, err := createTestBlock(chain, 1, uint16(i), 0, testAsset[i], testAmount[i],
//			validators[i], nil, chain.bestChain.tip())
//		if err != nil {
//			t.Errorf("create block error %v", err)
//		}
//		_, gasUsed := getGasUsedAndStateRoot(chain, block, chain.bestChain.tip())
//		block.MsgBlock().Header.GasUsed = gasUsed
//		block.MsgBlock().Header.Weight = weight
//		view := NewUtxoViewpoint()
//		txNums := len(block.MsgBlock().Transactions)
//		for k := 0; k < txNums; k++ {
//			tx := asiutil.NewTx(block.MsgBlock().Transactions[k])
//			_ = view.AddTxOuts(tx, false, block.Height())
//		}
//		err = chain.db.Update(func(dbTx database.Tx) error {
//			// Update the balance using the state of the utxo view.
//			err = dbPutBalance(dbTx, view)
//			if err != nil {
//				return err
//			}
//			err = dbPutUtxoView(dbTx, view)
//			if err != nil {
//				return err
//			}
//			return nil
//		})
//
//		nodeErr := chain.addBlockNode(block)
//		if nodeErr != nil {
//			t.Errorf("tests error %v", err)
//		}
//		blocks = append(blocks, block)
//	}
//
//	//test spend divisible asset tx:--------------------------------------------------
//	normalTx, _ := chain.createNormalTx(validatorPrivateKey, protos.Asset{0, 0}, *validators[0],
//	2000000000, 5000000, 100000, nil)
//	normalTxList := make([]*asiutil.Tx, 0)
//	normalTxList = append(normalTxList, normalTx)
//	block2, err := createTestBlock(chain, uint32(1), uint16(2), 0, protos.Asset{0, 0},
//		0, validators[2], normalTxList, chain.bestChain.Tip())
//	if err != nil {
//		t.Errorf("createTestBlock error %v", err)
//	}
//	stateRoot, gasUsed := getGasUsedAndStateRoot(chain, block2, chain.bestChain.tip())
//	block2.MsgBlock().Header.GasUsed = gasUsed
//	block2.MsgBlock().Header.StateRoot = *stateRoot
//
//	//test spend indivisible asset tx:------------------------------------------------
//	view := NewUtxoViewpoint()
//	var prevOuts *[]protos.OutPoint
//	prevOuts, err = chain.FetchUtxoViewByAddressAndAsset(view, validators[0].ScriptAddress(), &testAsset[1])
//	if err != nil {
//		t.Errorf("FetchUtxoViewByAddressAndAsset error %v", err)
//	}
//	usePreOut := (*prevOuts)[0]
//	inDivTx, _ := chain.createNormalTx(validatorPrivateKey, testAsset[1], *validators[0], testAmount[1],
//		0, 100000, &usePreOut)
//	inDivTxList := make([]*asiutil.Tx, 0)
//	inDivTxList = append(inDivTxList, inDivTx)
//	block3, err := createTestBlock(chain, uint32(1), uint16(2), 0, protos.Asset{0, 0},
//		0, validators[2], inDivTxList, chain.bestChain.Tip())
//	if err != nil {
//		t.Errorf("createTestBlock error %v", err)
//	}
//	stateRoot3, gasUsed3 := getGasUsedAndStateRoot(chain, block3, chain.bestChain.tip())
//	block3.MsgBlock().Header.GasUsed = gasUsed3
//	block3.MsgBlock().Header.StateRoot = *stateRoot3
//
//	//test signup tx:------------------------------------------------------------------
//	siguUpTx, signUpErr := chain.createSignUpTx(validatorPrivateKey, testAsset[0])
//	if signUpErr != nil {
//		t.Errorf("createSignUpTx error %v", signUpErr)
//	}
//	siguUpTxList := make([]*asiutil.Tx, 0)
//	siguUpTxList = append(siguUpTxList, siguUpTx)
//	block4, err := createTestBlock(chain, uint32(1), uint16(2), 0, protos.Asset{0, 0},
//		0, validators[2], siguUpTxList, chain.bestChain.Tip())
//	if err != nil {
//		t.Errorf("createTestBlock error %v", err)
//	}
//	stateRoot4, gasUsed4 := getGasUsedAndStateRoot(chain, block4, chain.bestChain.tip())
//	block4.MsgBlock().Header.GasUsed = gasUsed4
//	block4.MsgBlock().Header.StateRoot = *stateRoot4
//
//	//test error tx:
//	inputNormalTx2 := []int64{20000010, 20000020}
//	outputNormalTx2 := []int64{20000010, 20000020}
//	inputAssetListTx2 := []*protos.Asset{&testAsset[0], &testAsset[1]}
//	outputAssetListTx2 := inputAssetListTx2
//	errTxMsg, _, _, err := createTestTx(validatorPrivateKey, payAddrPkScript, payAddrPkScript,
//		100000, inputNormalTx2, inputAssetListTx2, outputNormalTx2, outputAssetListTx2)
//	if err != nil {
//		t.Errorf("create createNormalTx error %v", err)
//	}
//	block5, err := createTestBlock(chain, uint32(1), uint16(2), 0, protos.Asset{0, 0},
//		0, validators[2], nil, chain.bestChain.Tip())
//	if err != nil {
//		t.Errorf("createTestBlock error %v", err)
//	}
//	block5.MsgBlock().AddTransaction(errTxMsg)
//	_, gasUsed5 := getGasUsedAndStateRoot(chain, block5, chain.bestChain.tip())
//	block5.MsgBlock().Header.GasUsed = gasUsed5
//
//	tests := []struct {
//		block  *asiutil.Block
//		errStr string
//	}{
//		//block with divisible asset tx:
//		{
//			block2,
//			"",
//		},
//		//block with indivisible asset tx:
//		{
//			block3,
//			"",
//		},
//		//block with contract tx:
//		{
//			block4,
//			"",
//		},
//		//block with error tx:
//		{
//			block5,
//			"ErrMissingTxOut",
//		},
//	}
//	t.Logf("Running %d TestConnectTransactions tests", len(tests))
//	for i, test := range tests {
//		stxos := make([]SpentTxOut, 0, countSpentOutputs(test.block))
//		//contract related statedb info.
//		parentRoot := chain.bestChain.tip().stateRoot
//		stateDB, err := state.New(common.Hash(parentRoot), chain.stateCache)
//		if err != nil {
//			t.Errorf("get stateDB error %v", err)
//		}
//		var receipts types.Receipts
//		var allLogs []*types.Log
//		var msgvblock protos.MsgVBlock
//		var feeLockItems map[protos.Asset]*txo.LockItem
//		gasUsed = uint64(0)
//		view = NewUtxoViewpoint()
//		view.SetBestHash(&chain.bestChain.tip().hash)
//		err = view.fetchInputUtxos(chain.db, test.block)
//		if err != nil {
//			t.Errorf("test the %d connectTransactions error %v", i, err)
//		}
//		receipts, allLogs, err, gasUsed, feeLockItems = chain.connectTransactions(
//			view, test.block, &stxos, &msgvblock, stateDB)
//		if err != nil {
//			if dbErr, ok := err.(RuleError); !ok || dbErr.ErrorCode.String() != test.errStr {
//				t.Errorf("tests the %d error: %v", i, err)
//			}
//		} else {
//			if gasUsed != test.block.MsgBlock().Header.GasUsed {
//				t.Errorf("connectTransactions gasUsed error: expect: %v, got: %v",
//					test.block.MsgBlock().Header.GasUsed, gasUsed)
//			}
//			if len(feeLockItems) != 0 {
//				t.Errorf("connectTransactions feeLockItems error: feeLockItems = %v", feeLockItems)
//			}
//			if len(allLogs) != 0 {
//				t.Errorf("connectTransactions allLogs error: allLogs = %v", allLogs)
//			}
//		}
//		log.Infof("receipts = %v, allLogs = %v, feeLockItems = %v", receipts, allLogs, feeLockItems)
//	}
//}
