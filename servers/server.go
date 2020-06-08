// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2015-2017 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package servers

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/blockchain/syscontract"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/consensus/satoshiplus/minersync"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/logger"
	"github.com/AsimovNetwork/asimov/rpcs/node"
	"github.com/AsimovNetwork/asimov/rpcs/util"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AsimovNetwork/asimov/addrmgr"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/asiutil/bloom"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/blockchain/indexers"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	fnet "github.com/AsimovNetwork/asimov/common/net"
	"github.com/AsimovNetwork/asimov/connmgr"
	"github.com/AsimovNetwork/asimov/consensus"
	"github.com/AsimovNetwork/asimov/consensus/params"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/mempool"
	"github.com/AsimovNetwork/asimov/mining"
	"github.com/AsimovNetwork/asimov/netsync"
	"github.com/AsimovNetwork/asimov/peer"
	"github.com/AsimovNetwork/asimov/protos"
)

const (
	// defaultServices describes the default services that are supported by
	// the NodeServer.
	defaultServices = common.SFNodeNetwork | common.SFNodeBloom | common.SFNodeCF

	// defaultRequiredServices describes the default services that are
	// required to be supported by outbound peers.
	defaultRequiredServices = common.SFNodeNetwork

	// defaultTargetOutbound is the default number of outbound peers to target.
	defaultTargetOutbound = 8

	// connectionRetryInterval is the base amount of time to wait in between
	// retries when connecting to persistent peers.  It is adjusted by the
	// number of retries such that there is a retry backoff.
	connectionRetryInterval = time.Second * 5
)

var (
	// userAgentName is the user agent name and is used to help identify
	// ourselves to other asimov peers.
	userAgentName = "asimovd"

	// userAgentVersion is the user agent version and is used to help
	// identify ourselves to other asimov peers.
	userAgentVersion = fmt.Sprintf("%d.%d.%d", chaincfg.AppMajor, chaincfg.AppMinor, chaincfg.AppPatch)
)

// broadcastMsg provides the ability to house a bitcoin message to be broadcast
// to all connected peers except specified excluded peers.
type broadcastMsg struct {
	message      protos.Message
	excludePeers []*serverPeer
}

// broadcastInventoryAdd is a type used to declare that the InvVect it contains
// needs to be added to the rebroadcast map
type broadcastInventoryAdd relayMsg

// broadcastInventoryDel is a type used to declare that the InvVect it contains
// needs to be removed from the rebroadcast map
type broadcastInventoryDel *protos.InvVect

// relayMsg packages an inventory vector along with the newly discovered
// inventory so the relay has access to that information.
type relayMsg struct {
	invVect *protos.InvVect
	data    interface{}
}

// updatePeerHeightsMsg is a message sent from the blockmanager to the NodeServer
// after a new block has been accepted. The purpose of the message is to update
// the heights of peers that were known to announce the block before we
// connected it to the main chain or recognized it as an orphan. With these
// updates, peer heights will be kept up to date, allowing for fresh data when
// selecting sync peer candidacy.
type updatePeerHeightsMsg struct {
	newHash    *common.Hash
	newHeight  int32
	originPeer *peer.Peer
}

// peerState maintains state of inbound, persistent, outbound peers as well
// as banned peers and outbound groups.
type peerState struct {
	inboundPeers    map[int32]*serverPeer
	outboundPeers   map[int32]*serverPeer
	persistentPeers map[int32]*serverPeer
	banned          map[string]time.Time
	outboundGroups  map[string]int
}

// Count returns the count of all known peers.
func (ps *peerState) Count() int {
	return len(ps.inboundPeers) + len(ps.outboundPeers) +
		len(ps.persistentPeers)
}

// forAllOutboundPeers is a helper function that runs closure on all outbound
// peers known to peerState.
func (ps *peerState) forAllOutboundPeers(closure func(sp *serverPeer)) {
	for _, e := range ps.outboundPeers {
		closure(e)
	}
	for _, e := range ps.persistentPeers {
		closure(e)
	}
}

// forAllPeers is a helper function that runs closure on all peers known to
// peerState.
func (ps *peerState) forAllPeers(closure func(sp *serverPeer)) {
	for _, e := range ps.inboundPeers {
		closure(e)
	}
	ps.forAllOutboundPeers(closure)
}

// cfHeaderKV is a tuple of a filter header and its associated block hash. The
// struct is used to cache cfcheckpt responses.
type cfHeaderKV struct {
	blockHash    common.Hash
	filterHeader common.Hash
}

// NodeServer provides an asimov Server for handling communications to and from
// asimov peers.
type NodeServer struct {
	// The following variables must only be used atomically.
	// Putting the uint64s first makes them 64-bit aligned for 32-bit systems.
	bytesReceived uint64 // Total bytes received from all peers since start.
	bytesSent     uint64 // Total bytes sent by all peers since start.
	started       int32
	shutdown      int32
	shutdownSched int32
	startupTime   int64

	chainParams          *chaincfg.Params
	addrManager          *addrmgr.AddrManager
	connManager          *connmgr.ConnManager
	roundManger          ainterface.IRoundManager
	syncManager          *netsync.SyncManager
	chain                *blockchain.BlockChain
	txMemPool            *mempool.TxPool
	sigMemPool           *mempool.SigPool
	consensus            ainterface.Consensus
	cfg                  *params.Config
	modifyRebroadcastInv chan interface{}
	newPeers             chan *serverPeer
	donePeers            chan *serverPeer
	banPeers             chan *serverPeer
	query                chan interface{}
	relayInv             chan relayMsg
	broadcast            chan broadcastMsg
	peerHeightsUpdate    chan updatePeerHeightsMsg
	wg                   sync.WaitGroup
	quit                 chan struct{}
	nat                  fnet.NAT
	db                   database.Transactor
	timeSource           blockchain.MedianTimeSource
	services             common.ServiceFlag

	// The following fields are used for optional indexes.  They will be nil
	// if the associated index is not enabled.  These fields are set during
	// initial creation of the NodeServer and never changed afterwards, so they
	// do not need to be protected for concurrent access.
	txIndex       *indexers.TxIndex
	addrIndex     *indexers.AddrIndex
	cfIndex       *indexers.CfIndex
	templateIndex blockchain.Indexer

	// cfCheckptCaches stores a cached slice of filter headers for cfcheckpt
	// messages for each filter type.
	cfCheckptCaches    map[protos.FilterType][]cfHeaderKV
	cfCheckptCachesMtx sync.RWMutex
	stack              *node.Node

	// agentBlacklist is a list of blacklisted substrings by which to filter
	// user agents.
	agentBlacklist []string

	// agentWhitelist is a list of whitelisted user agent substrings, no
	// whitelisting will be applied if the list is empty or nil.
	agentWhitelist []string
}

// serverPeer extends the peer to maintain state shared by the NodeServer and
// the blockmanager.
type serverPeer struct {
	// The following variables must only be used atomically
	priceFilter int32

	*peer.Peer

	connReq        *connmgr.ConnReq
	server         *NodeServer
	persistent     bool
	continueHash   *common.Hash
	relayMtx       sync.Mutex
	disableRelayTx bool
	sentAddrs      bool
	filter         *bloom.Filter
	addressesMtx   sync.RWMutex
	knownAddresses map[string]struct{}
	quit           chan struct{}
	// The following chans are used to sync blockmanager and NodeServer.
	txProcessed    chan struct{}
	sigProcessed   chan struct{}
	blockProcessed chan struct{}
}

// newServerPeer returns a new serverPeer instance. The peer needs to be set by
// the caller.
func newServerPeer(s *NodeServer, isPersistent bool) *serverPeer {
	return &serverPeer{
		server:         s,
		persistent:     isPersistent,
		filter:         bloom.LoadFilter(nil),
		knownAddresses: make(map[string]struct{}),
		quit:           make(chan struct{}),
		txProcessed:    make(chan struct{}, 1),
		sigProcessed:   make(chan struct{}, 1),
		blockProcessed: make(chan struct{}, 1),
	}
}

// newestBlock returns the current best block hash, height and weight using the format
// required by the configuration for the peer package.
func (sp *serverPeer) newestBlock() (*common.Hash, int32, uint64, error) {
	best := sp.server.chain.BestSnapshot()
	return &best.Hash, best.Height, best.Weight, nil
}

// addKnownAddresses adds the given addresses to the set of known addresses to
// the peer to prevent sending duplicate addresses.
func (sp *serverPeer) addKnownAddresses(addresses []*protos.NetAddress) {
	sp.addressesMtx.Lock()
	for _, na := range addresses {
		sp.knownAddresses[addrmgr.NetAddressKey(na)] = struct{}{}
	}
	sp.addressesMtx.Unlock()
}

// addressKnown true if the given address is already known to the peer.
func (sp *serverPeer) addressKnown(na *protos.NetAddress) bool {
	sp.addressesMtx.RLock()
	_, exists := sp.knownAddresses[addrmgr.NetAddressKey(na)]
	sp.addressesMtx.RUnlock()
	return exists
}

// setDisableRelayTx toggles relaying of transactions for the given peer.
// It is safe for concurrent access.
func (sp *serverPeer) setDisableRelayTx(disable bool) {
	sp.relayMtx.Lock()
	sp.disableRelayTx = disable
	sp.relayMtx.Unlock()
}

// relayTxDisabled returns whether or not relaying of transactions for the given
// peer is disabled.
// It is safe for concurrent access.
func (sp *serverPeer) relayTxDisabled() bool {
	sp.relayMtx.Lock()
	isDisabled := sp.disableRelayTx
	sp.relayMtx.Unlock()

	return isDisabled
}

// pushAddrMsg sends an addr message to the connected peer using the provided
// addresses.
func (sp *serverPeer) pushAddrMsg(addresses []*protos.NetAddress) {
	// Filter addresses already known to the peer.
	addrs := make([]*protos.NetAddress, 0, len(addresses))
	for _, addr := range addresses {
		if !sp.addressKnown(addr) {
			addrs = append(addrs, addr)
		}
	}
	known, err := sp.PushAddrMsg(addrs)
	if err != nil {
		peerLog.Errorf("Can't push address message to %s: %v", sp.Peer, err)
		sp.Disconnect()
		return
	}
	sp.addKnownAddresses(known)
}

// hasServices returns whether or not the provided advertised service flags have
// all of the provided desired service flags set.
func hasServices(advertised, desired common.ServiceFlag) bool {
	return advertised&desired == desired
}

// OnVersion is invoked when a peer receives a version bitcoin message
// and is used to negotiate the protocol version details as well as kick start
// the communications.
func (sp *serverPeer) OnVersion(_ *peer.Peer, msg *protos.MsgVersion) *protos.MsgReject {
	// Update the address manager with the advertised services for outbound
	// connections in case they have changed.  This is not done for inbound
	// connections to help prevent malicious behavior and is skipped when
	// running on the simulation test network since it is only intended to
	// connect to specified peers and actively avoids advertising and
	// connecting to discovered peers.
	//
	// NOTE: This is done before rejecting peers that are too old to ensure
	// it is updated regardless in the case a new minimum protocol version is
	// enforced and the remote node has not upgraded yet.
	isInbound := sp.Inbound()
	remoteAddr := sp.NA()
	addrManager := sp.server.addrManager
	if !isInbound {
		addrManager.SetServices(remoteAddr, msg.Services)
	}

	// Ignore peers that have a protcol version that is too old.  The peer
	// negotiation logic will disconnect it after this callback returns.
	if msg.ProtocolVersion < peer.MinAcceptableProtocolVersion {
		return nil
	}

	// Reject outbound peers that are not full nodes.
	wantServices := common.SFNodeNetwork
	if !isInbound && !hasServices(msg.Services, wantServices) {
		missingServices := wantServices & ^msg.Services
		srvrLog.Debugf("Rejecting peer %s with services %v due to not "+
			"providing desired services %v", sp.Peer, msg.Services,
			missingServices)
		reason := fmt.Sprintf("required services %#x not offered",
			uint64(missingServices))
		return protos.NewMsgReject(msg.Command(), protos.RejectNonstandard, reason)
	}

	// Add the remote peer time as a sample for creating an offset against
	// the local clock to keep the network time in sync.
	sp.server.timeSource.AddTimeSample(sp.Addr(), msg.Timestamp)

	// Choose whether or not to relay transactions before a filter command
	// is received.
	sp.setDisableRelayTx(msg.DisableRelayTx)
	return nil
}

// OnVerAck is invoked when a peer receives a verack bitcoin message and is used
// to kick start communication with them.
func (sp *serverPeer) OnVerAck(_ *peer.Peer, _ *protos.MsgVerAck) {
	sp.server.AddPeer(sp)
}

// OnMemPool is invoked when a peer receives a mempool bitcoin message.
// It creates and sends an inventory message with the contents of the memory
// pool up to the maximum inventory allowed per message.  When the peer has a
// bloom filter loaded, the contents are filtered accordingly.
func (sp *serverPeer) OnMemPool(_ *peer.Peer, msg *protos.MsgMemPool) {
	// Only allow mempool requests if the NodeServer has bloom filtering
	// enabled.
	if sp.server.services&common.SFNodeBloom != common.SFNodeBloom {
		peerLog.Debugf("peer %v sent mempool request with bloom "+
			"filtering disabled -- disconnecting", sp)
		sp.Disconnect()
		return
	}

	// A decaying ban score increase is applied to prevent flooding.
	// The ban score accumulates and passes the ban threshold if a burst of
	// mempool messages comes from a peer. The score decays each minute to
	// half of its value.
	sp.AddBanScore(0, 33, "mempool")

	// Generate inventory message with the available transactions in the
	// transaction memory pool.  Limit it to the max allowed inventory
	// per message.  The NewMsgInvSizeHint function automatically limits
	// the passed hint to the maximum allowed, so it's safe to pass it
	// without double checking it here.
	txMemPool := sp.server.txMemPool
	txDescs := txMemPool.TxDescs()
	invMsg := protos.NewMsgInvSizeHint(uint(len(txDescs)))

	for _, txDesc := range txDescs {
		// Either add all transactions when there is no bloom filter,
		// or only the transactions that match the filter when there is
		// one.
		if !sp.filter.IsLoaded() || sp.filter.MatchTxAndUpdate(txDesc.Tx) {
			iv := protos.NewInvVect(protos.InvTypeTx, txDesc.Tx.Hash())
			invMsg.AddInvVect(iv)
			if len(invMsg.InvList)+1 > protos.MaxInvPerMsg {
				break
			}
		}
	}

	// Send the inventory message if there is anything to send.
	if len(invMsg.InvList) > 0 {
		sp.QueueMessage(invMsg, nil)
	}
}

// OnTx is invoked when a peer receives a tx bitcoin message.  It blocks
// until the bitcoin transaction has been fully processed.  Unlock the block
// handler this does not serialize all transactions through a single thread
// transactions don't rely on the previous one in a linear fashion like blocks.
func (sp *serverPeer) OnTx(_ *peer.Peer, msg *protos.MsgTx) {
	if chaincfg.Cfg.BlocksOnly {
		peerLog.Tracef("Ignoring tx %v from %v - blocksonly enabled",
			msg.TxHash(), sp)
		return
	}

	// Add the transaction to the known inventory for the peer.
	// Convert the raw MsgTx to a vvsutil.Tx which provides some convenience
	// methods and things such as hash caching.
	tx := asiutil.NewTx(msg)
	iv := protos.NewInvVect(protos.InvTypeTx, tx.Hash())
	sp.AddKnownInventory(iv)

	// Queue the transaction up to be handled by the sync manager and
	// intentionally block further receives until the transaction is fully
	// processed and known good or bad.  This helps prevent a malicious peer
	// from queuing up a bunch of bad transactions before disconnecting (or
	// being disconnected) and wasting memory.
	sp.server.syncManager.QueueTx(tx, sp.Peer, sp.txProcessed)
	<-sp.txProcessed
}

func (sp *serverPeer) OnSig(_ *peer.Peer, msg *protos.MsgBlockSign) {
	if chaincfg.Cfg.BlocksOnly {
		peerLog.Tracef("Ignoring tx %v from %v - blocksonly enabled",
			msg.SignHash(), sp)
		return
	}

	sig := asiutil.NewBlockSign(msg)
	iv := protos.NewInvVect(protos.InvTypeSignature, sig.Hash())
	sp.AddKnownInventory(iv)

	sp.server.syncManager.QueueSig(sig, sp.Peer, sp.sigProcessed)
	<-sp.sigProcessed
}

// OnBlock is invoked when a peer receives a block bitcoin message.  It
// blocks until the bitcoin block has been fully processed.
func (sp *serverPeer) OnBlock(_ *peer.Peer, msg *protos.MsgBlock, buf []byte) {
	// Convert the raw MsgBlock to a vvsutil.Block which provides some
	// convenience methods and things such as hash caching.
	block := asiutil.NewBlockFromBlockAndBytes(msg, buf)

	// Add the block to the known inventory for the peer.
	iv := protos.NewInvVect(protos.InvTypeBlock, block.Hash())
	sp.AddKnownInventory(iv)

	// Queue the block up to be handled by the block
	// manager and intentionally block further receives
	// until the bitcoin block is fully processed and known
	// good or bad.  This helps prevent a malicious peer
	// from queuing up a bunch of bad blocks before
	// disconnecting (or being disconnected) and wasting
	// memory.  Additionally, this behavior is depended on
	// by at least the block acceptance test tool as the
	// reference implementation processes blocks in the same
	// thread and therefore blocks further messages until
	// the bitcoin block has been fully processed.
	sp.server.syncManager.QueueBlock(block, sp.Peer, sp.blockProcessed)
	<-sp.blockProcessed
}

// OnInv is invoked when a peer receives an inv bitcoin message and is
// used to examine the inventory being advertised by the remote peer and react
// accordingly.  We pass the message down to blockmanager which will call
// QueueMessage with any appropriate responses.
func (sp *serverPeer) OnInv(_ *peer.Peer, msg *protos.MsgInv) {
	if !chaincfg.Cfg.BlocksOnly {
		if len(msg.InvList) > 0 {
			sp.server.syncManager.QueueInv(msg, sp.Peer)
		}
		return
	}

	newInv := protos.NewMsgInvSizeHint(uint(len(msg.InvList)))
	for _, invVect := range msg.InvList {
		if invVect.Type == protos.InvTypeTx {
			peerLog.Tracef("Ignoring tx %v in inv from %v -- "+
				"blocksonly enabled", invVect.Hash, sp)
			continue
		}
		err := newInv.AddInvVect(invVect)
		if err != nil {
			peerLog.Errorf("Failed to add inventory vector: %v", err)
			break
		}
	}

	if len(newInv.InvList) > 0 {
		sp.server.syncManager.QueueInv(newInv, sp.Peer)
	}
}

// OnHeaders is invoked when a peer receives a headers bitcoin
// message.  The message is passed down to the sync manager.
func (sp *serverPeer) OnHeaders(_ *peer.Peer, msg *protos.MsgHeaders) {
	sp.server.syncManager.QueueHeaders(msg, sp.Peer)
}

// handleGetData is invoked when a peer receives a getdata bitcoin message and
// is used to deliver block and transaction information.
func (sp *serverPeer) OnGetData(_ *peer.Peer, msg *protos.MsgGetData) {
	numAdded := 0
	notFound := protos.NewMsgNotFound()

	length := len(msg.InvList)
	// A decaying ban score increase is applied to prevent exhausting resources
	// with unusually large inventory queries.
	// Requesting more than the maximum inventory vector length within a short
	// period of time yields a score above the default ban threshold. Sustained
	// bursts of small requests are not penalized as that would potentially ban
	// peers performing IBD.
	// This incremental score decays each minute to half of its value.
	sp.AddBanScore(0, uint32(length)*99/protos.MaxInvPerMsg, "getdata")

	// We wait on this wait channel periodically to prevent queuing
	// far more data than we can send in a reasonable time, wasting memory.
	// The waiting occurs after the database fetch for the next one to
	// provide a little pipelining.
	var waitChan chan struct{}
	doneChan := make(chan struct{}, 1)

	for i, iv := range msg.InvList {
		var c chan struct{}
		// If this will be the last message we send.
		if i == length-1 && len(notFound.InvList) == 0 {
			c = doneChan
		} else if (i+1)%3 == 0 {
			// Buffered so as to not make the send goroutine block.
			c = make(chan struct{}, 1)
		}
		var err error
		switch iv.Type {
		case protos.InvTypeTx:
			err = sp.server.pushTxMsg(sp, &iv.Hash, c, waitChan, protos.BaseEncoding)
		case protos.InvTypeBlock:
			err = sp.server.pushBlockMsg(sp, &iv.Hash, c, waitChan, protos.BaseEncoding)
		case protos.InvTypeFilteredBlock:
			err = sp.server.pushMerkleBlockMsg(sp, &iv.Hash, c, waitChan, protos.BaseEncoding)
		case protos.InvTypeSignature:
			err = sp.server.pushSigMsg(sp, &iv.Hash, c, waitChan, protos.BaseEncoding)
		case protos.InvTypeTxForbidden:
			if c != nil {
				c <- struct{}{}
			}
			// skip
		default:
			peerLog.Warnf("Unknown type in inventory request %d",
				iv.Type)
			continue
		}
		if err != nil {
			notFound.AddInvVect(iv)

			// When there is a failure fetching the final entry
			// and the done channel was sent in due to there
			// being no outstanding not found inventory, consume
			// it here because there is now not found inventory
			// that will use the channel momentarily.
			if i == len(msg.InvList)-1 && c != nil {
				<-c
			}
		}
		numAdded++
		waitChan = c
	}
	if len(notFound.InvList) != 0 {
		sp.QueueMessage(notFound, doneChan)
	}

	// Wait for messages to be sent. We can send quite a lot of data at this
	// point and this will keep the peer busy for a decent amount of time.
	// We don't process anything else by them in this time so that we
	// have an idea of when we should hear back from them - else the idle
	// timeout could fire when we were only half done sending the blocks.
	if numAdded > 0 {
		<-doneChan
	}
}

// OnGetBlocks is invoked when a peer receives a getblocks bitcoin
// message.
func (sp *serverPeer) OnGetBlocks(_ *peer.Peer, msg *protos.MsgGetBlocks) {
	// Find the most recent known block in the best chain based on the block
	// locator and fetch all of the block hashes after it until either
	// protos.MaxBlocksPerMsg have been fetched or the provided stop hash is
	// encountered.
	//
	// Use the block after the genesis block if no other blocks in the
	// provided locator are known.  This does mean the client will start
	// over with the genesis block if unknown block locators are provided.
	//
	// This mirrors the behavior in the reference implementation.
	chain := sp.server.chain
	hashList := chain.LocateBlocks(msg.BlockLocatorHashes, &msg.HashStop,
		protos.MaxBlocksPerMsg)

	// Generate inventory message.
	invMsg := protos.NewMsgInv()
	for i := range hashList {
		iv := protos.NewInvVect(protos.InvTypeBlock, &hashList[i])
		invMsg.AddInvVect(iv)
	}

	// Send the inventory message if there is anything to send.
	if len(invMsg.InvList) > 0 {
		invListLen := len(invMsg.InvList)
		if invListLen == protos.MaxBlocksPerMsg {
			// Intentionally use a copy of the final hash so there
			// is not a reference into the inventory slice which
			// would prevent the entire slice from being eligible
			// for GC as soon as it's sent.
			continueHash := invMsg.InvList[invListLen-1].Hash
			sp.continueHash = &continueHash
		}
		sp.QueueMessage(invMsg, nil)
	}
}

// OnGetHeaders is invoked when a peer receives a getheaders bitcoin
// message.
func (sp *serverPeer) OnGetHeaders(_ *peer.Peer, msg *protos.MsgGetHeaders) {
	// Ignore getheaders requests if not in sync.
	if !sp.server.syncManager.IsCurrent() {
		return
	}

	// Find the most recent known block in the best chain based on the block
	// locator and fetch all of the headers after it until either
	// protos.MaxBlockHeadersPerMsg have been fetched or the provided stop
	// hash is encountered.
	//
	// Use the block after the genesis block if no other blocks in the
	// provided locator are known.  This does mean the client will start
	// over with the genesis block if unknown block locators are provided.
	//
	// This mirrors the behavior in the reference implementation.
	chain := sp.server.chain
	headers := chain.LocateHeaders(msg.BlockLocatorHashes, &msg.HashStop)

	// Send found headers to the requesting peer.
	blockHeaders := make([]*protos.BlockHeader, len(headers))
	for i := range headers {
		blockHeaders[i] = &headers[i]
	}
	sp.QueueMessage(&protos.MsgHeaders{Headers: blockHeaders}, nil)
}

// OnGetCFilters is invoked when a peer receives a getcfilters bitcoin message.
func (sp *serverPeer) OnGetCFilters(_ *peer.Peer, msg *protos.MsgGetCFilters) {
	// Ignore getcfilters requests if not in sync.
	if !sp.server.syncManager.IsCurrent() {
		return
	}

	// We'll also ensure that the remote party is requesting a set of
	// filters that we actually currently maintain.
	switch msg.FilterType {
	case protos.GCSFilterRegular:
		break

	default:
		peerLog.Debug("Filter request for unknown filter: %v",
			msg.FilterType)
		return
	}

	hashes, err := sp.server.chain.HeightToHashRange(
		int32(msg.StartHeight), &msg.StopHash, protos.MaxGetCFiltersReqRange,
	)
	if err != nil {
		peerLog.Debugf("Invalid getcfilters request: %v", err)
		return
	}

	// Create []*common.Hash from []common.Hash to pass to
	// FiltersByBlockHashes.
	hashPtrs := make([]*common.Hash, len(hashes))
	for i := range hashes {
		hashPtrs[i] = &hashes[i]
	}

	filters, err := sp.server.cfIndex.FiltersByBlockHashes(
		hashPtrs, msg.FilterType,
	)
	if err != nil {
		peerLog.Errorf("Error retrieving cfilters: %v", err)
		return
	}

	for i, filterBytes := range filters {
		if len(filterBytes) == 0 {
			peerLog.Warnf("Could not obtain cfilter for %v",
				hashes[i])
			return
		}

		filterMsg := protos.NewMsgCFilter(
			msg.FilterType, &hashes[i], filterBytes,
		)
		sp.QueueMessage(filterMsg, nil)
	}
}

// OnGetCFHeaders is invoked when a peer receives a getcfheader bitcoin message.
func (sp *serverPeer) OnGetCFHeaders(_ *peer.Peer, msg *protos.MsgGetCFHeaders) {
	// Ignore getcfilterheader requests if not in sync.
	if !sp.server.syncManager.IsCurrent() {
		return
	}

	// We'll also ensure that the remote party is requesting a set of
	// headers for filters that we actually currently maintain.
	switch msg.FilterType {
	case protos.GCSFilterRegular:
		break

	default:
		peerLog.Debug("Filter request for unknown headers for "+
			"filter: %v", msg.FilterType)
		return
	}

	startHeight := int32(msg.StartHeight)
	maxResults := protos.MaxCFHeadersPerMsg

	// If StartHeight is positive, fetch the predecessor block hash so we
	// can populate the PrevFilterHeader field.
	if msg.StartHeight > 0 {
		startHeight--
		maxResults++
	}

	// Fetch the hashes from the block index.
	hashList, err := sp.server.chain.HeightToHashRange(
		startHeight, &msg.StopHash, maxResults,
	)
	if err != nil {
		peerLog.Debugf("Invalid getcfheaders request: %v", err)
	}

	// This is possible if StartHeight is one greater that the height of
	// StopHash, and we pull a valid range of hashes including the previous
	// filter header.
	if len(hashList) == 0 || (msg.StartHeight > 0 && len(hashList) == 1) {
		peerLog.Debug("No results for getcfheaders request")
		return
	}

	// Create []*common.Hash from []common.Hash to pass to
	// FilterHeadersByBlockHashes.
	hashPtrs := make([]*common.Hash, len(hashList))
	for i := range hashList {
		hashPtrs[i] = &hashList[i]
	}

	// Fetch the raw filter hash bytes from the database for all blocks.
	filterHashes, err := sp.server.cfIndex.FilterHashesByBlockHashes(
		hashPtrs, msg.FilterType,
	)
	if err != nil {
		peerLog.Errorf("Error retrieving cfilter hashes: %v", err)
		return
	}

	// Generate cfheaders message and send it.
	headersMsg := protos.NewMsgCFHeaders()

	// Populate the PrevFilterHeader field.
	if msg.StartHeight > 0 {
		prevBlockHash := &hashList[0]

		// Fetch the raw committed filter header bytes from the
		// database.
		headerBytes, err := sp.server.cfIndex.FilterHeaderByBlockHash(
			prevBlockHash, msg.FilterType)
		if err != nil {
			peerLog.Errorf("Error retrieving CF header: %v", err)
			return
		}
		if len(headerBytes) == 0 {
			peerLog.Warnf("Could not obtain CF header for %v", prevBlockHash)
			return
		}

		// Deserialize the hash into PrevFilterHeader.
		headersMsg.PrevFilterHeader.SetBytes(headerBytes)

		hashList = hashList[1:]
		filterHashes = filterHashes[1:]
	}

	// Populate HeaderHashes.
	for i, hashBytes := range filterHashes {
		if len(hashBytes) == 0 {
			peerLog.Warnf("Could not obtain CF hash for %v", hashList[i])
			return
		}

		// Deserialize the hash.
		filterHash := common.BytesToHash(hashBytes)
		headersMsg.AddCFHash(&filterHash)
	}

	headersMsg.FilterType = msg.FilterType
	headersMsg.StopHash = msg.StopHash

	sp.QueueMessage(headersMsg, nil)
}

// OnGetCFCheckpt is invoked when a peer receives a getcfcheckpt bitcoin message.
func (sp *serverPeer) OnGetCFCheckpt(_ *peer.Peer, msg *protos.MsgGetCFCheckpt) {
	// Ignore getcfcheckpt requests if not in sync.
	if !sp.server.syncManager.IsCurrent() {
		return
	}

	// We'll also ensure that the remote party is requesting a set of
	// checkpoints for filters that we actually currently maintain.
	switch msg.FilterType {
	case protos.GCSFilterRegular:
		break

	default:
		peerLog.Debug("Filter request for unknown checkpoints for "+
			"filter: %v", msg.FilterType)
		return
	}

	// Now that we know the client is fetching a filter that we know of,
	// we'll fetch the block hashes et each check point interval so we can
	// compare against our cache, and create new check points if necessary.
	blockHashes, err := sp.server.chain.IntervalBlockHashes(
		&msg.StopHash, protos.CFCheckPTInterval,
	)
	if err != nil {
		peerLog.Debugf("Invalid getcfilters request: %v", err)
		return
	}

	checkptMsg := protos.NewMsgCFCheckpt(
		msg.FilterType, &msg.StopHash, len(blockHashes),
	)

	// Fetch the current existing cache so we can decide if we need to
	// extend it or if its adequate as is.
	sp.server.cfCheckptCachesMtx.RLock()
	checkptCache := sp.server.cfCheckptCaches[msg.FilterType]

	// If the set of block hashes is beyond the current size of the cache,
	// then we'll expand the size of the cache and also retain the write
	// lock.
	var updateCache bool
	if len(blockHashes) > len(checkptCache) {
		// Now that we know we'll need to modify the size of the cache,
		// we'll release the read lock and grab the write lock to
		// possibly expand the cache size.
		sp.server.cfCheckptCachesMtx.RUnlock()

		sp.server.cfCheckptCachesMtx.Lock()
		defer sp.server.cfCheckptCachesMtx.Unlock()

		// Now that we have the write lock, we'll check again as it's
		// possible that the cache has already been expanded.
		checkptCache = sp.server.cfCheckptCaches[msg.FilterType]

		// If we still need to expand the cache, then We'll mark that
		// we need to update the cache for below and also expand the
		// size of the cache in place.
		if len(blockHashes) > len(checkptCache) {
			updateCache = true

			additionalLength := len(blockHashes) - len(checkptCache)
			newEntries := make([]cfHeaderKV, additionalLength)

			peerLog.Infof("Growing size of checkpoint cache from %v to %v"+
				"block hashes", len(checkptCache), len(blockHashes))

			checkptCache = append(
				sp.server.cfCheckptCaches[msg.FilterType],
				newEntries...,
			)
		}
	} else {
		// Otherwise, we'll hold onto the read lock for the remainder
		// of this method.
		defer sp.server.cfCheckptCachesMtx.RUnlock()

		peerLog.Tracef("Serving stale cache of size %v",
			len(checkptCache))
	}

	// Now that we know the cache is of an appropriate size, we'll iterate
	// backwards until the find the block hash. We do this as it's possible
	// a re-org has occurred so items in the db are now in the main china
	// while the cache has been partially invalidated.
	var forkIdx int
	for forkIdx = len(blockHashes); forkIdx > 0; forkIdx-- {
		if checkptCache[forkIdx-1].blockHash == blockHashes[forkIdx-1] {
			break
		}
	}

	// Now that we know the how much of the cache is relevant for this
	// query, we'll populate our check point message with the cache as is.
	// Shortly below, we'll populate the new elements of the cache.
	for i := 0; i < forkIdx; i++ {
		checkptMsg.AddCFHeader(&checkptCache[i].filterHeader)
	}

	// We'll now collect the set of hashes that are beyond our cache so we
	// can look up the filter headers to populate the final cache.
	blockHashPtrs := make([]*common.Hash, 0, len(blockHashes)-forkIdx)
	for i := forkIdx; i < len(blockHashes); i++ {
		blockHashPtrs = append(blockHashPtrs, &blockHashes[i])
	}
	filterHeaders, err := sp.server.cfIndex.FilterHeadersByBlockHashes(
		blockHashPtrs, msg.FilterType,
	)
	if err != nil {
		peerLog.Errorf("Error retrieving cfilter headers: %v", err)
		return
	}

	// Now that we have the full set of filter headers, we'll add them to
	// the checkpoint message, and also update our cache in line.
	for i, filterHeaderBytes := range filterHeaders {
		if len(filterHeaderBytes) == 0 {
			peerLog.Warnf("Could not obtain CF header for %v",
				blockHashPtrs[i])
			return
		}

		filterHeader := common.BytesToHash(filterHeaderBytes)
		checkptMsg.AddCFHeader(&filterHeader)

		// If the new main chain is longer than what's in the cache,
		// then we'll override it beyond the fork point.
		if updateCache {
			checkptCache[forkIdx+i] = cfHeaderKV{
				blockHash:    blockHashes[forkIdx+i],
				filterHeader: filterHeader,
			}
		}
	}

	// Finally, we'll update the cache if we need to, and send the final
	// message back to the requesting peer.
	if updateCache {
		sp.server.cfCheckptCaches[msg.FilterType] = checkptCache
	}

	sp.QueueMessage(checkptMsg, nil)
}

// enforceNodeBloomFlag disconnects the peer if the NodeServer is not configured to
// allow bloom filters.  Additionally, if the peer has negotiated to a protocol
// version  that is high enough to observe the bloom filter service support bit,
// it will be banned since it is intentionally violating the protocol.
func (sp *serverPeer) enforceNodeBloomFlag(cmd string) bool {
	if sp.server.services&common.SFNodeBloom != common.SFNodeBloom {
		// Ban the peer if the protocol version is high enough that the
		// peer is knowingly violating the protocol and banning is
		// enabled.
		//
		// NOTE: Even though the addBanScore function already examines
		// whether or not banning is enabled, it is checked here as well
		// to ensure the violation is logged and the peer is
		// disconnected regardless.
		if !chaincfg.Cfg.DisableBanning {

			// Disconnect the peer regardless of whether it was
			// banned.
			sp.AddBanScore(100, 0, cmd)
			sp.Disconnect()
			return false
		}

		// Disconnect the peer regardless of protocol version or banning
		// state.
		peerLog.Debugf("%s sent an unsupported %s request -- "+
			"disconnecting", sp, cmd)
		sp.Disconnect()
		return false
	}

	return true
}

// OnFeeFilter is invoked when a peer receives a feefilter bitcoin message and
// is used by remote peers to request that no transactions which have a fee rate
// lower than provided value are inventoried to them.  The peer will be
// disconnected if an invalid fee filter value is provided.
func (sp *serverPeer) OnFeeFilter(_ *peer.Peer, msg *protos.MsgFeeFilter) {
	// Check that the passed minimum price is a valid value.
	if msg.MinPrice < 0 || msg.MinPrice > common.MaxPrice {
		peerLog.Debugf("Peer %v sent an invalid feefilter '%v' -- "+
			"disconnecting", sp, common.Amount(msg.MinPrice))
		sp.Disconnect()
		return
	}

	atomic.StoreInt32(&sp.priceFilter, msg.MinPrice)
}

// OnFilterAdd is invoked when a peer receives a filteradd bitcoin
// message and is used by remote peers to add data to an already loaded bloom
// filter.  The peer will be disconnected if a filter is not loaded when this
// message is received or the NodeServer is not configured to allow bloom filters.
func (sp *serverPeer) OnFilterAdd(_ *peer.Peer, msg *protos.MsgFilterAdd) {
	// Disconnect and/or ban depending on the node bloom services flag and
	// negotiated protocol version.
	if !sp.enforceNodeBloomFlag(msg.Command()) {
		return
	}

	if !sp.filter.IsLoaded() {
		peerLog.Debugf("%s sent a filteradd request with no filter "+
			"loaded -- disconnecting", sp)
		sp.Disconnect()
		return
	}

	sp.filter.Add(msg.Data)
}

// OnFilterClear is invoked when a peer receives a filterclear bitcoin
// message and is used by remote peers to clear an already loaded bloom filter.
// The peer will be disconnected if a filter is not loaded when this message is
// received  or the NodeServer is not configured to allow bloom filters.
func (sp *serverPeer) OnFilterClear(_ *peer.Peer, msg *protos.MsgFilterClear) {
	// Disconnect and/or ban depending on the node bloom services flag and
	// negotiated protocol version.
	if !sp.enforceNodeBloomFlag(msg.Command()) {
		return
	}

	if !sp.filter.IsLoaded() {
		peerLog.Debugf("%s sent a filterclear request with no "+
			"filter loaded -- disconnecting", sp)
		sp.Disconnect()
		return
	}

	sp.filter.Unload()
}

// OnFilterLoad is invoked when a peer receives a filterload bitcoin
// message and it used to load a bloom filter that should be used for
// delivering merkle blocks and associated transactions that match the filter.
// The peer will be disconnected if the NodeServer is not configured to allow bloom
// filters.
func (sp *serverPeer) OnFilterLoad(_ *peer.Peer, msg *protos.MsgFilterLoad) {
	// Disconnect and/or ban depending on the node bloom services flag and
	// negotiated protocol version.
	if !sp.enforceNodeBloomFlag(msg.Command()) {
		return
	}

	sp.setDisableRelayTx(false)

	sp.filter.Reload(msg)
}

// OnGetAddr is invoked when a peer receives a getaddr bitcoin message
// and is used to provide the peer with known addresses from the address
// manager.
func (sp *serverPeer) OnGetAddr(_ *peer.Peer, msg *protos.MsgGetAddr) {

	// Do not accept getaddr requests from outbound peers.  This reduces
	// fingerprinting attacks.
	if !sp.Inbound() {
		peerLog.Debugf("Ignoring getaddr request from outbound peer ",
			"%v", sp)
		return
	}

	// Only allow one getaddr request per connection to discourage
	// address stamping of inv announcements.
	if sp.sentAddrs {
		peerLog.Debugf("Ignoring repeated getaddr request from peer ",
			"%v", sp)
		return
	}
	sp.sentAddrs = true

	// Get the current known addresses from the address manager.
	addrCache := sp.server.addrManager.AddressCache()

	// Push the addresses.
	sp.pushAddrMsg(addrCache)
}

// OnAddr is invoked when a peer receives an addr bitcoin message and is
// used to notify the NodeServer about advertised addresses.
func (sp *serverPeer) OnAddr(_ *peer.Peer, msg *protos.MsgAddr) {

	// A message that has no addresses is invalid.
	if len(msg.AddrList) == 0 {
		peerLog.Errorf("Command [%s] from %s does not contain any addresses",
			msg.Command(), sp.Peer)
		sp.Disconnect()
		return
	}

	for _, na := range msg.AddrList {
		// Don't add more address if we're disconnecting.
		if !sp.Connected() {
			return
		}

		// Set the timestamp to 5 days ago if it's more than 24 hours
		// in the future so this address is one of the first to be
		// removed when space is needed.
		now := time.Now()
		if na.Timestamp.After(now.Add(time.Minute * 10)) {
			na.Timestamp = now.Add(-1 * time.Hour * 24 * 5)
		}

		// Add address to known addresses for this peer.
		sp.addKnownAddresses([]*protos.NetAddress{na})
	}

	// Add addresses to NodeServer address manager.  The address manager handles
	// the details of things such as preventing duplicate addresses, max
	// addresses, and last seen updates.
	// XXX bitcoind gives a 2 hour time penalty here, do we want to do the
	// same?
	sp.server.addrManager.AddAddresses(msg.AddrList, sp.NA())
}

// OnRead is invoked when a peer receives a message and it is used to update
// the bytes received by the NodeServer.
func (sp *serverPeer) OnRead(_ *peer.Peer, bytesRead int, msg protos.Message, err error) {
	sp.server.AddBytesReceived(uint64(bytesRead))
}

// OnWrite is invoked when a peer sends a message and it is used to update
// the bytes sent by the NodeServer.
func (sp *serverPeer) OnWrite(_ *peer.Peer, bytesWritten int, msg protos.Message, err error) {
	sp.server.AddBytesSent(uint64(bytesWritten))
}

// randomUint16Number returns a random uint16 in a specified input range.  Note
// that the range is in zeroth ordering; if you pass it 1800, you will get
// values from 0 to 1800.
func randomUint16Number(max uint16) uint16 {
	// In order to avoid modulo bias and ensure every possible outcome in
	// [0, max) has equal probability, the random number must be sampled
	// from a random source that has a range limited to a multiple of the
	// modulus.
	var randomNumber uint16
	var limitRange = (math.MaxUint16 / max) * max
	for {
		binary.Read(rand.Reader, binary.LittleEndian, &randomNumber)
		if randomNumber < limitRange {
			return randomNumber % max
		}
	}
}

// AddRebroadcastInventory adds 'iv' to the list of inventories to be
// rebroadcasted at random intervals until they show up in a block.
func (s *NodeServer) AddRebroadcastInventory(iv *protos.InvVect, data interface{}) {
	// Ignore if shutting down.
	if atomic.LoadInt32(&s.shutdown) != 0 {
		return
	}

	s.modifyRebroadcastInv <- broadcastInventoryAdd{invVect: iv, data: data}
}

// RemoveRebroadcastInventory removes 'iv' from the list of items to be
// rebroadcasted if present.
func (s *NodeServer) RemoveRebroadcastInventory(iv *protos.InvVect) {
	// Ignore if shutting down.
	if atomic.LoadInt32(&s.shutdown) != 0 {
		return
	}

	s.modifyRebroadcastInv <- broadcastInventoryDel(iv)
}

// relayTransactions generates and relays inventory vectors for all of the
// passed transactions to all connected peers.
func (s *NodeServer) relayTransactions(txns []*mining.TxDesc) {
	for _, txD := range txns {
		iv := protos.NewInvVect(protos.InvTypeTx, txD.Tx.Hash())
		s.RelayInventory(iv, txD)
	}
}

// AnnounceNewTransactions generates and relays inventory vectors and notifies
// both websocket and getblocktemplate long poll clients of the passed
// transactions.  This function should be called whenever new transactions
// are added to the mempool.
func (s *NodeServer) AnnounceNewTransactions(txns []*mining.TxDesc) {
	// Generate and relay inventory vectors for all newly accepted
	// transactions.
	s.relayTransactions(txns)
}

// Transaction has one confirmation on the main chain. Now we can mark it as no
// longer needing rebroadcasting.
func (s *NodeServer) TransactionConfirmed(tx *asiutil.Tx) {
	iv := protos.NewInvVect(protos.InvTypeTx, tx.Hash())
	s.RemoveRebroadcastInventory(iv)
}

// pushTxMsg sends a tx message for the provided transaction hash to the
// connected peer.  An error is returned if the transaction hash is not known.
func (s *NodeServer) pushTxMsg(sp *serverPeer, hash *common.Hash, doneChan chan<- struct{},
	waitChan <-chan struct{}, encoding protos.MessageEncoding) error {

	// Attempt to fetch the requested transaction from the pool.  A
	// call could be made to check for existence first, but simply trying
	// to fetch a missing transaction results in the same behavior.
	tx, forbidden, err := s.txMemPool.FetchTransaction(hash)
	if forbidden {
		iv := protos.NewInvVect(protos.InvTypeTxForbidden, hash)

		// Queue the inventory to be relayed with the next batch.
		// It will be ignored if the peer is already known to
		// have the inventory.
		sp.QueueInventory(iv)

		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return err
	}
	if err != nil {
		peerLog.Tracef("Unable to fetch tx %v from transaction "+
			"pool: %v", hash, err)

		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return err
	}

	// Once we have fetched data wait for any previous operation to finish.
	if waitChan != nil {
		<-waitChan
	}

	sp.QueueMessageWithEncoding(tx.MsgTx(), doneChan, encoding)

	return nil
}

func (s *NodeServer) pushSigMsg(sp *serverPeer, hash *common.Hash, doneChan chan<- struct{},
	waitChan <-chan struct{}, encoding protos.MessageEncoding) error {

	sig, packageheight, err := s.sigMemPool.FetchSignature(hash)
	if err != nil {
		peerLog.Tracef("Unable to fetch sig %v from signature "+
			"pool: %v", hash, err)
		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return err
	}
	if packageheight > 0 {
		peerLog.Tracef("sig %v already packaged in block %v", hash, packageheight)
		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return nil
	}

	if waitChan != nil {
		<-waitChan
	}

	sp.QueueMessageWithEncoding(sig.MsgSign, doneChan, encoding)
	return nil
}

func (s *NodeServer) relaySignatures(sig *asiutil.BlockSign) {
	iv := protos.NewInvVect(protos.InvTypeSignature, sig.Hash())
	s.RelayInventory(iv, sig)
}

func (s *NodeServer) AnnounceNewSignature(sig *asiutil.BlockSign) {
	// Generate and relay inventory vectors for all newly accepted
	// transactions.
	s.relaySignatures(sig)

	// Notify both websocket and getblocktemplate long poll clients of all
	// newly accepted transactions.
	//TODO:
	//if s.rpcServer != nil {
	//	s.rpcServer.NotifyNewTransactions(txns)
	//}
}

// pushBlockMsg sends a block message for the provided block hash to the
// connected peer.  An error is returned if the block hash is not known.
func (s *NodeServer) pushBlockMsg(sp *serverPeer, hash *common.Hash, doneChan chan<- struct{},
	waitChan <-chan struct{}, encoding protos.MessageEncoding) error {

	// Fetch the raw block bytes from the database.
	var blockBytes []byte
	err := sp.server.db.View(func(dbTx database.Tx) error {
		var err error
		blockBytes, err = dbTx.FetchBlock(database.NewNormalBlockKey(hash))
		return err
	})
	if err != nil {
		peerLog.Tracef("Unable to fetch requested block hash %v: %v",
			hash, err)

		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return err
	}

	// Deserialize the block.
	var msgBlock protos.MsgBlock
	err = msgBlock.Deserialize(bytes.NewReader(blockBytes))
	if err != nil {
		peerLog.Tracef("Unable to deserialize requested block hash "+
			"%v: %v", hash, err)

		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return err
	}

	// Once we have fetched data wait for any previous operation to finish.
	if waitChan != nil {
		<-waitChan
	}

	// We only send the channel for this message if we aren't sending
	// an inv straight after.
	var dc chan<- struct{}
	continueHash := sp.continueHash
	sendInv := continueHash != nil && continueHash.IsEqual(hash)
	if !sendInv {
		dc = doneChan
	}
	sp.QueueMessageWithEncoding(&msgBlock, dc, encoding)

	// When the peer requests the final block that was advertised in
	// response to a getblocks message which requested more blocks than
	// would fit into a single message, send it a new inventory message
	// to trigger it to issue another getblocks message for the next
	// batch of inventory.
	if sendInv {
		best := sp.server.chain.BestSnapshot()
		invMsg := protos.NewMsgInvSizeHint(1)
		iv := protos.NewInvVect(protos.InvTypeBlock, &best.Hash)
		invMsg.AddInvVect(iv)
		sp.QueueMessage(invMsg, doneChan)
		sp.continueHash = nil
	}
	return nil
}

// pushMerkleBlockMsg sends a merkleblock message for the provided block hash to
// the connected peer.  Since a merkle block requires the peer to have a filter
// loaded, this call will simply be ignored if there is no filter loaded.  An
// error is returned if the block hash is not known.
func (s *NodeServer) pushMerkleBlockMsg(sp *serverPeer, hash *common.Hash,
	doneChan chan<- struct{}, waitChan <-chan struct{}, encoding protos.MessageEncoding) error {

	// Do not send a response if the peer doesn't have a filter loaded.
	if !sp.filter.IsLoaded() {
		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return nil
	}

	// Fetch the raw block bytes from the database.
	blk, err := sp.server.chain.BlockByHash(hash)
	if err != nil {
		peerLog.Tracef("Unable to fetch requested block hash %v: %v",
			hash, err)

		if doneChan != nil {
			doneChan <- struct{}{}
		}
		return err
	}

	// Generate a merkle block by filtering the requested block according
	// to the filter for the peer.
	merkle, matchedTxIndices := bloom.NewMerkleBlock(blk, sp.filter)

	// Once we have fetched data wait for any previous operation to finish.
	if waitChan != nil {
		<-waitChan
	}

	// Send the merkleblock.  Only send the done channel with this message
	// if no transactions will be sent afterwards.
	var dc chan<- struct{}
	if len(matchedTxIndices) == 0 {
		dc = doneChan
	}
	sp.QueueMessage(merkle, dc)

	// Finally, send any matched transactions.
	blkTransactions := blk.MsgBlock().Transactions
	for i, txIndex := range matchedTxIndices {
		// Only send the done channel on the final transaction.
		var dc chan<- struct{}
		if i == len(matchedTxIndices)-1 {
			dc = doneChan
		}
		if txIndex < uint32(len(blkTransactions)) {
			sp.QueueMessageWithEncoding(blkTransactions[txIndex], dc,
				encoding)
		}
	}

	return nil
}

// handleUpdatePeerHeight updates the heights of all peers who were known to
// announce a block we recently accepted.
func (s *NodeServer) handleUpdatePeerHeights(state *peerState, umsg updatePeerHeightsMsg) {
	state.forAllPeers(func(sp *serverPeer) {
		// The origin peer should already have the updated height.
		if sp.Peer == umsg.originPeer {
			return
		}

		// This is a pointer to the underlying memory which doesn't
		// change.
		latestBlkHash := sp.LastAnnouncedBlock()

		// Skip this peer if it hasn't recently announced any new blocks.
		if latestBlkHash == nil {
			return
		}

		// If the peer has recently announced a block, and this block
		// matches our newly accepted block, then update their block
		// height.
		if *latestBlkHash == *umsg.newHash {
			sp.UpdateLastBlockHeight(umsg.newHeight)
			sp.UpdateLastAnnouncedBlock(nil)
		}
	})
}

// handleAddPeerMsg deals with adding new peers.  It is invoked from the
// peerHandler goroutine.
func (s *NodeServer) handleAddPeerMsg(state *peerState, sp *serverPeer) bool {
	if sp == nil || !sp.Connected() {
		return false
	}

	// Disconnect peers with unwanted user agents.
	if sp.HasUndesiredUserAgent(s.agentBlacklist, s.agentWhitelist) {
		sp.Disconnect()
		return false
	}

	// Ignore new peers if we're shutting down.
	if atomic.LoadInt32(&s.shutdown) != 0 {
		srvrLog.Infof("New peer %s ignored - NodeServer is shutting down", sp)
		sp.Disconnect()
		return false
	}

	// Disconnect banned peers.
	host, _, err := net.SplitHostPort(sp.Addr())
	if err != nil {
		srvrLog.Debugf("can't split hostport %v", err)
		sp.Disconnect()
		return false
	}
	if banEnd, ok := state.banned[host]; ok {
		if time.Now().Before(banEnd) {
			srvrLog.Debugf("Peer %s is banned for another %v - disconnecting",
				host, time.Until(banEnd))
			sp.Disconnect()
			return false
		}

		srvrLog.Infof("Peer %s is no longer banned", host)
		delete(state.banned, host)
	}

	// TODO: Check for max peers from a single IP.

	// Limit max number of total peers.
	if state.Count() >= chaincfg.Cfg.MaxPeers {
		srvrLog.Infof("Max peers reached [%d] - disconnecting peer %s",
			chaincfg.Cfg.MaxPeers, sp)
		sp.Disconnect()
		// TODO: how to handle permanent peers here?
		// they should be rescheduled.
		return false
	}

	// Add the new peer and start it.
	srvrLog.Debugf("New peer %s", sp)
	if sp.Inbound() {
		state.inboundPeers[sp.ID()] = sp
	} else {
		state.outboundGroups[addrmgr.GroupKey(sp.NA())]++
		if sp.persistent {
			state.persistentPeers[sp.ID()] = sp
		} else {
			state.outboundPeers[sp.ID()] = sp
		}
	}

	// Update the address' last seen time if the peer has acknowledged
	// our version and has sent us its version as well.
	if sp.VerAckReceived() && sp.VersionKnown() && sp.NA() != nil {
		s.addrManager.Connected(sp.NA())
	}

	// Signal the sync manager this peer is a new sync candidate.
	s.syncManager.NewPeer(sp.Peer)

	// Update the address manager and request known addresses from the
	// remote peer for outbound connections. This is skipped when running on
	// the simulation test network since it is only intended to connect to
	// specified peers and actively avoids advertising and connecting to
	// discovered peers.
	if !sp.Inbound() {
		// Advertise the local address when the server accepts incoming
		// connections and it believes itself to be close to the best
		// known tip.
		if !chaincfg.Cfg.DisableListen && s.syncManager.GetCurrent() {
			// Get address that best matches.
			lna := s.addrManager.GetBestLocalAddress(sp.NA())
			if addrmgr.IsRoutable(lna) {
				// Filter addresses the peer already knows about.
				addresses := []*protos.NetAddress{lna}
				sp.pushAddrMsg(addresses)
			}
		}

		// Request known addresses if the server address manager needs
		// more and the peer has a protocol version new enough to
		// include a timestamp with addresses.
		if s.addrManager.NeedMoreAddresses() {
			sp.QueueMessage(protos.NewMsgGetAddr(), nil)
		}

		// Mark the address as a known good address.
		s.addrManager.Good(sp.NA())
	}

	return true
}

// handleDonePeerMsg deals with peers that have signalled they are done.  It is
// invoked from the peerHandler goroutine.
func (s *NodeServer) handleDonePeerMsg(state *peerState, sp *serverPeer) {
	var list map[int32]*serverPeer
	if sp.persistent {
		list = state.persistentPeers
	} else if sp.Inbound() {
		list = state.inboundPeers
	} else {
		list = state.outboundPeers
	}
	// Regardless of whether the peer was found in our list, we'll inform
	// our connection manager about the disconnection. This can happen if we
	// process a peer's `done` message before its `add`.
	if !sp.Inbound() {
		if sp.persistent {
			s.connManager.Disconnect(sp.connReq.ID())
		} else {
			s.connManager.Remove(sp.connReq.ID())
			go s.connManager.NewConnReq()
		}
	}

	if _, ok := list[sp.ID()]; ok {
		if !sp.Inbound() && sp.VersionKnown() {
			state.outboundGroups[addrmgr.GroupKey(sp.NA())]--
		}
		delete(list, sp.ID())
		srvrLog.Debugf("Removed peer %s", sp)
		return
	}
}

// handleBanPeerMsg deals with banning peers.  It is invoked from the
// peerHandler goroutine.
func (s *NodeServer) handleBanPeerMsg(state *peerState, sp *serverPeer) {
	host, _, err := net.SplitHostPort(sp.Addr())
	if err != nil {
		srvrLog.Debugf("can't split ban peer %s %v", sp.Addr(), err)
		return
	}
	direction := fnet.DirectionString(sp.Inbound())
	srvrLog.Infof("Banned peer %s (%s) for %v", host, direction,
		chaincfg.Cfg.BanDuration)
	state.banned[host] = time.Now().Add(chaincfg.Cfg.BanDuration)
}

// handleRelayInvMsg deals with relaying inventory to peers that are not already
// known to have it.  It is invoked from the peerHandler goroutine.
func (s *NodeServer) handleRelayInvMsg(state *peerState, msg relayMsg) {
	state.forAllPeers(func(sp *serverPeer) {
		if !sp.Connected() {
			return
		}

		// If the inventory is a block and the peer prefers headers,
		// generate and send a headers message instead of an inventory
		// message.
		if msg.invVect.Type == protos.InvTypeBlock && sp.WantsHeaders() {
			blockHeader, ok := msg.data.(protos.BlockHeader)
			if !ok {
				peerLog.Warnf("Underlying data for headers" +
					" is not a block header")
				return
			}
			msgHeaders := protos.NewMsgHeaders()
			if err := msgHeaders.AddBlockHeader(&blockHeader); err != nil {
				peerLog.Errorf("Failed to add block"+
					" header: %v", err)
				return
			}
			sp.QueueMessage(msgHeaders, nil)
			return
		}

		if msg.invVect.Type == protos.InvTypeTx {
			// Don't relay the transaction to the peer when it has
			// transaction relaying disabled.
			if sp.relayTxDisabled() {
				return
			}

			txD, ok := msg.data.(*mining.TxDesc)
			if !ok {
				peerLog.Warnf("Underlying data for tx inv "+
					"relay is not a *mempool.TxDesc: %T",
					msg.data)
				return
			}

			// Don't relay the transaction if the transaction fee-per-kb
			// is less than the peer's feefilter.
			priceFilter := atomic.LoadInt32(&sp.priceFilter)
			if priceFilter > 0 && txD.GasPrice < float64(priceFilter) {
				return
			}

			// Don't relay the transaction if there is a bloom
			// filter loaded and the transaction doesn't match it.
			if sp.filter.IsLoaded() {
				if !sp.filter.MatchTxAndUpdate(txD.Tx) {
					return
				}
			}
		}

		// Queue the inventory to be relayed with the next batch.
		// It will be ignored if the peer is already known to
		// have the inventory.
		sp.QueueInventory(msg.invVect)
	})
}

// handleBroadcastMsg deals with broadcasting messages to peers.  It is invoked
// from the peerHandler goroutine.
func (s *NodeServer) handleBroadcastMsg(state *peerState, bmsg *broadcastMsg) {
	state.forAllPeers(func(sp *serverPeer) {
		if !sp.Connected() {
			return
		}

		for _, ep := range bmsg.excludePeers {
			if sp == ep {
				return
			}
		}

		sp.QueueMessage(bmsg.message, nil)
	})
}

type getConnCountMsg struct {
	reply chan int32
}

type getPeersMsg struct {
	reply chan []*serverPeer
}

type getOutboundGroup struct {
	key   string
	reply chan int
}

type getAddedNodesMsg struct {
	reply chan []*serverPeer
}

type disconnectNodeMsg struct {
	cmp   func(*serverPeer) bool
	reply chan error
}

type connectNodeMsg struct {
	addr      string
	permanent bool
	reply     chan error
}

type removeNodeMsg struct {
	cmp   func(*serverPeer) bool
	reply chan error
}

// handleQuery is the central handler for all queries and commands from other
// goroutines related to peer state.
func (s *NodeServer) handleQuery(state *peerState, querymsg interface{}) {
	switch msg := querymsg.(type) {
	case getConnCountMsg:
		nconnected := int32(0)
		state.forAllPeers(func(sp *serverPeer) {
			if sp.Connected() {
				nconnected++
			}
		})
		msg.reply <- nconnected

	case getPeersMsg:
		peers := make([]*serverPeer, 0, state.Count())
		state.forAllPeers(func(sp *serverPeer) {
			if !sp.Connected() {
				return
			}
			peers = append(peers, sp)
		})
		msg.reply <- peers

	case connectNodeMsg:
		// TODO: duplicate oneshots?
		// Limit max number of total peers.
		if state.Count() >= chaincfg.Cfg.MaxPeers {
			msg.reply <- errors.New("max peers reached")
			return
		}
		for _, peer := range state.persistentPeers {
			if peer.Addr() == msg.addr {
				if msg.permanent {
					msg.reply <- errors.New("peer already connected")
				} else {
					msg.reply <- errors.New("peer exists as a permanent peer")
				}
				return
			}
		}

		netAddr, err := s.addrManager.AddrStringToNetAddr(msg.addr)
		if err != nil {
			msg.reply <- err
			return
		}

		// TODO: if too many, nuke a non-perm peer.
		go s.connManager.Connect(&connmgr.ConnReq{
			Addr:      netAddr,
			Permanent: msg.permanent,
		})
		msg.reply <- nil
	case removeNodeMsg:
		found := disconnectPeer(state.persistentPeers, msg.cmp, func(sp *serverPeer) {
			// Keep group counts ok since we remove from
			// the list now.
			state.outboundGroups[addrmgr.GroupKey(sp.NA())]--
		})

		if found {
			msg.reply <- nil
		} else {
			msg.reply <- errors.New("peer not found")
		}
	case getOutboundGroup:
		count, ok := state.outboundGroups[msg.key]
		if ok {
			msg.reply <- count
		} else {
			msg.reply <- 0
		}
		// Request a list of the persistent (added) peers.
	case getAddedNodesMsg:
		// Respond with a slice of the relevant peers.
		peers := make([]*serverPeer, 0, len(state.persistentPeers))
		for _, sp := range state.persistentPeers {
			peers = append(peers, sp)
		}
		msg.reply <- peers
	case disconnectNodeMsg:
		// Check inbound peers. We pass a nil callback since we don't
		// require any additional actions on disconnect for inbound peers.
		found := disconnectPeer(state.inboundPeers, msg.cmp, nil)
		if found {
			msg.reply <- nil
			return
		}

		// Check outbound peers.
		found = disconnectPeer(state.outboundPeers, msg.cmp, func(sp *serverPeer) {
			// Keep group counts ok since we remove from
			// the list now.
			state.outboundGroups[addrmgr.GroupKey(sp.NA())]--
		})
		if found {
			// If there are multiple outbound connections to the same
			// ip:port, continue disconnecting them all until no such
			// peers are found.
			for found {
				found = disconnectPeer(state.outboundPeers, msg.cmp, func(sp *serverPeer) {
					state.outboundGroups[addrmgr.GroupKey(sp.NA())]--
				})
			}
			msg.reply <- nil
			return
		}

		msg.reply <- errors.New("peer not found")
	}
}

// disconnectPeer attempts to drop the connection of a targeted peer in the
// passed peer list. Targets are identified via usage of the passed
// `compareFunc`, which should return `true` if the passed peer is the target
// peer. This function returns true on success and false if the peer is unable
// to be located. If the peer is found, and the passed callback: `whenFound'
// isn't nil, we call it with the peer as the argument before it is removed
// from the peerList, and is disconnected from the NodeServer.
func disconnectPeer(peerList map[int32]*serverPeer, compareFunc func(*serverPeer) bool, whenFound func(*serverPeer)) bool {
	for addr, peer := range peerList {
		if compareFunc(peer) {
			if whenFound != nil {
				whenFound(peer)
			}

			// This is ok because we are not continuing
			// to iterate so won't corrupt the loop.
			delete(peerList, addr)
			peer.Disconnect()
			return true
		}
	}
	return false
}

// newPeerConfig returns the configuration for the given serverPeer.
func newPeerConfig(sp *serverPeer) *peer.Config {
	return &peer.Config{
		Listeners: peer.MessageListeners{
			OnVersion:      sp.OnVersion,
			OnVerAck:       sp.OnVerAck,
			OnMemPool:      sp.OnMemPool,
			OnTx:           sp.OnTx,
			OnSig:          sp.OnSig,
			OnBlock:        sp.OnBlock,
			OnInv:          sp.OnInv,
			OnHeaders:      sp.OnHeaders,
			OnGetData:      sp.OnGetData,
			OnGetBlocks:    sp.OnGetBlocks,
			OnGetHeaders:   sp.OnGetHeaders,
			OnGetCFilters:  sp.OnGetCFilters,
			OnGetCFHeaders: sp.OnGetCFHeaders,
			OnGetCFCheckpt: sp.OnGetCFCheckpt,
			OnFeeFilter:    sp.OnFeeFilter,
			OnFilterAdd:    sp.OnFilterAdd,
			OnFilterClear:  sp.OnFilterClear,
			OnFilterLoad:   sp.OnFilterLoad,
			OnGetAddr:      sp.OnGetAddr,
			OnAddr:         sp.OnAddr,
			OnRead:         sp.OnRead,
			OnWrite:        sp.OnWrite,
			OnBan:          sp.OnBan,
		},
		NewestBlock:       sp.newestBlock,
		HostToNetAddress:  sp.server.addrManager.HostToNetAddress,
		Proxy:             chaincfg.Cfg.Proxy,
		UserAgentName:     userAgentName,
		UserAgentVersion:  userAgentVersion,
		UserAgentComments: chaincfg.Cfg.UserAgentComments,
		ChainParams:       sp.server.chainParams,
		Services:          sp.server.services,
		DisableRelayTx:    chaincfg.Cfg.BlocksOnly,
		ProtocolVersion:   peer.MaxProtocolVersion,
	}
}

// inboundPeerConnected is invoked by the connection manager when a new inbound
// connection is established.  It initializes a new inbound NodeServer peer
// instance, associates it with the connection, and starts a goroutine to wait
// for disconnection.
func (s *NodeServer) inboundPeerConnected(conn net.Conn) {
	sp := newServerPeer(s, false)
	sp.Peer = peer.NewInboundPeer(newPeerConfig(sp))
	sp.AssociateConnection(conn)
	go s.peerDoneHandler(sp)
}

// outboundPeerConnected is invoked by the connection manager when a new
// outbound connection is established.  It initializes a new outbound NodeServer
// peer instance, associates it with the relevant state such as the connection
// request instance and the connection itself, and finally notifies the address
// manager of the attempt.
func (s *NodeServer) outboundPeerConnected(c *connmgr.ConnReq, conn net.Conn) {
	sp := newServerPeer(s, c.Permanent)
	p, err := peer.NewOutboundPeer(newPeerConfig(sp), c.Addr.String())
	if err != nil {
		srvrLog.Debugf("Cannot create outbound peer %s: %v", c.Addr, err)
		if c.Permanent {
			s.connManager.Disconnect(c.ID())
		} else {
			s.connManager.Remove(c.ID())
			go s.connManager.NewConnReq()
		}
		return
	}
	sp.Peer = p
	sp.connReq = c
	sp.AssociateConnection(conn)
	go s.peerDoneHandler(sp)
}

// peerDoneHandler handles peer disconnects by notifiying the NodeServer that it's
// done along with other performing other desirable cleanup.
func (s *NodeServer) peerDoneHandler(sp *serverPeer) {
	sp.WaitForDisconnect()
	s.donePeers <- sp

	// Only tell sync manager we are gone if we ever told it we existed.
	if sp.VerAckReceived() {
		s.syncManager.DonePeer(sp.Peer)

		// Evict any remaining orphans that were sent by the peer.
		numEvicted := s.txMemPool.RemoveOrphansByTag(mempool.Tag(sp.ID()))
		if numEvicted > 0 {
			txmpLog.Debugf("Evicted %d %s from peer %v (id %d)",
				numEvicted, fnet.PickNoun(numEvicted, "orphan",
					"orphans"), sp, sp.ID())
		}
	}
	close(sp.quit)
}

// peerHandler is used to handle peer operations such as adding and removing
// peers to and from the NodeServer, banning peers, and broadcasting messages to
// peers.  It must be run in a goroutine.
func (s *NodeServer) peerHandler() {
	// Start the address manager and sync manager, both of which are needed
	// by peers.  This is done here since their lifecycle is closely tied
	// to this handler and rather than adding more channels to sychronize
	// things, it's easier and slightly faster to simply start and stop them
	// in this handler.
	s.addrManager.Start()
	s.syncManager.Start()

	srvrLog.Tracef("Starting peer handler")

	state := &peerState{
		inboundPeers:    make(map[int32]*serverPeer),
		persistentPeers: make(map[int32]*serverPeer),
		outboundPeers:   make(map[int32]*serverPeer),
		banned:          make(map[string]time.Time),
		outboundGroups:  make(map[string]int),
	}

	if !chaincfg.Cfg.DisableDNSSeed {
		// Add peers discovered through DNS to the address manager.
		s.addrManager.SeedFromDNS(chaincfg.ActiveNetParams.Params, defaultRequiredServices)
	}
	go s.connManager.Start()

out:
	for {
		select {
		// New peers connected to the NodeServer.
		case p := <-s.newPeers:
			s.handleAddPeerMsg(state, p)

			// Disconnected peers.
		case p := <-s.donePeers:
			s.handleDonePeerMsg(state, p)

			// Block accepted in mainchain or orphan, update peer height.
		case umsg := <-s.peerHeightsUpdate:
			s.handleUpdatePeerHeights(state, umsg)

			// Peer to ban.
		case p := <-s.banPeers:
			s.handleBanPeerMsg(state, p)

			// New inventory to potentially be relayed to other peers.
		case invMsg := <-s.relayInv:
			s.handleRelayInvMsg(state, invMsg)

			// Message to broadcast to all connected peers except those
			// which are excluded by the message.
		case bmsg := <-s.broadcast:
			s.handleBroadcastMsg(state, &bmsg)

		case qmsg := <-s.query:
			s.handleQuery(state, qmsg)

		case <-s.quit:
			// Disconnect all peers on NodeServer shutdown.
			state.forAllPeers(func(sp *serverPeer) {
				srvrLog.Tracef("Shutdown peer %s", sp)
				sp.Disconnect()
			})
			break out
		}
	}

	s.connManager.Stop()
	s.syncManager.Stop()
	s.addrManager.Stop()

	// Drain channels before exiting so nothing is left waiting around
	// to send.
cleanup:
	for {
		select {
		case <-s.newPeers:
		case <-s.donePeers:
		case <-s.peerHeightsUpdate:
		case <-s.relayInv:
		case <-s.broadcast:
		case <-s.query:
		default:
			break cleanup
		}
	}
	s.wg.Done()
	srvrLog.Tracef("Peer handler done")
}

// AddPeer adds a new peer that has already been connected to the NodeServer.
func (s *NodeServer) AddPeer(sp *serverPeer) {
	s.newPeers <- sp
}

// BanPeer bans a peer that has already been connected to the NodeServer by ip.
func (s *NodeServer) BanPeer(sp *serverPeer) {
	s.banPeers <- sp
}

// RelayInventory relays the passed inventory vector to all connected peers
// that are not already known to have it.
func (s *NodeServer) RelayInventory(invVect *protos.InvVect, data interface{}) {
	s.relayInv <- relayMsg{invVect: invVect, data: data}
}

// BroadcastMessage sends msg to all peers currently connected to the NodeServer
// except those in the passed peers to exclude.
func (s *NodeServer) BroadcastMessage(msg protos.Message, exclPeers ...*serverPeer) {
	// XXX: Need to determine if this is an alert that has already been
	// broadcast and refrain from broadcasting again.
	bmsg := broadcastMsg{message: msg, excludePeers: exclPeers}
	s.broadcast <- bmsg
}

// ConnectedCount returns the number of currently connected peers.
func (s *NodeServer) ConnectedCount() int32 {
	replyChan := make(chan int32)

	s.query <- getConnCountMsg{reply: replyChan}

	return <-replyChan
}

// OutboundGroupCount returns the number of peers connected to the given
// outbound group key.
func (s *NodeServer) OutboundGroupCount(key string) int {
	replyChan := make(chan int)
	s.query <- getOutboundGroup{key: key, reply: replyChan}
	return <-replyChan
}

// AddBytesSent adds the passed number of bytes to the total bytes sent counter
// for the NodeServer.  It is safe for concurrent access.
func (s *NodeServer) AddBytesSent(bytesSent uint64) {
	atomic.AddUint64(&s.bytesSent, bytesSent)
}

// AddBytesReceived adds the passed number of bytes to the total bytes received
// counter for the NodeServer.  It is safe for concurrent access.
func (s *NodeServer) AddBytesReceived(bytesReceived uint64) {
	atomic.AddUint64(&s.bytesReceived, bytesReceived)
}

// NetTotals returns the sum of all bytes received and sent across the network
// for all peers.  It is safe for concurrent access.
func (s *NodeServer) NetTotals() (uint64, uint64) {
	return atomic.LoadUint64(&s.bytesReceived),
		atomic.LoadUint64(&s.bytesSent)
}

// UpdatePeerHeights updates the heights of all peers who have have announced
// the latest connected main chain block, or a recognized orphan. These height
// updates allow us to dynamically refresh peer heights, ensuring sync peer
// selection has access to the latest block heights for each peer.
func (s *NodeServer) UpdatePeerHeights(latestBlkHash *common.Hash, latestHeight int32, updateSource *peer.Peer) {
	s.peerHeightsUpdate <- updatePeerHeightsMsg{
		newHash:    latestBlkHash,
		newHeight:  latestHeight,
		originPeer: updateSource,
	}
}

// rebroadcastHandler keeps track of user submitted inventories that we have
// sent out but have not yet made it into a block. We periodically rebroadcast
// them in case our peers restarted or otherwise lost track of them.
func (s *NodeServer) rebroadcastHandler() {
	// Wait 5 min before first tx rebroadcast.
	timer := time.NewTimer(5 * time.Minute)
	pendingInvs := make(map[protos.InvVect]interface{})

out:
	for {
		select {
		case riv := <-s.modifyRebroadcastInv:
			switch msg := riv.(type) {
			// Incoming InvVects are added to our map of RPC txs.
			case broadcastInventoryAdd:
				pendingInvs[*msg.invVect] = msg.data

				// When an InvVect has been added to a block, we can
				// now remove it, if it was present.
			case broadcastInventoryDel:
				if _, ok := pendingInvs[*msg]; ok {
					delete(pendingInvs, *msg)
				}
			}

		case <-timer.C:
			// Any inventory we have has not made it into a block
			// yet. We periodically resubmit them until they have.
			for iv, data := range pendingInvs {
				ivCopy := iv
				s.RelayInventory(&ivCopy, data)
			}

			// Process at a random time up to 30mins (in seconds)
			// in the future.
			timer.Reset(time.Second *
				time.Duration(randomUint16Number(1800)))

		case <-s.quit:
			break out
		}
	}

	timer.Stop()

	// Drain channels before exiting so nothing is left waiting around
	// to send.
cleanup:
	for {
		select {
		case <-s.modifyRebroadcastInv:
		default:
			break cleanup
		}
	}
	s.wg.Done()
}

// Start begins accepting connections from peers.
func (s *NodeServer) Start() {
	// Already started?
	if atomic.AddInt32(&s.started, 1) != 1 {
		return
	}

	srvrLog.Trace("Starting NodeServer")

	// Server startup time. Used for the uptime command for uptime calculation.
	s.startupTime = time.Now().Unix()

	// Make sure round manager start firstly
	s.roundManger.Start()

	s.txMemPool.Start()

	// Start the peer handler which in turn starts the address and block
	// managers.
	s.wg.Add(1)
	go s.peerHandler()

	if s.nat != nil {
		s.wg.Add(1)
		go s.upnpUpdateThread()
	}

	if !chaincfg.Cfg.DisableRPC {
		s.wg.Add(1)

		// Start the rebroadcastHandler, which ensures user tx received by
		// the RPC server are rebroadcast until being included in a block.
		go s.rebroadcastHandler()

		util.StartNode(s.stack)
	}

	// Start the consensus server.
	if err := s.consensus.Start(); err != nil {
		panic(err)
	}
}

// Stop gracefully shuts down the NodeServer by stopping and disconnecting all
// peers and the main listener.
func (s *NodeServer) Stop() {
	// Make sure this only happens once.
	if atomic.AddInt32(&s.shutdown, 1) != 1 {
		srvrLog.Infof("Server is already in the process of shutting down")
		return
	}

	srvrLog.Warnf("Server shutting down")

	// Stop the consensus server if needed
	if s.consensus != nil {
		s.consensus.Halt()
	}

	//// Shutdown the RPC server if it's not disabled.
	//if !configuration.Cfg.DisableRPC {
	//	s.rpcServer.Stop()
	//}

	// close scope
	s.chain.Scope().Close()

	s.roundManger.Halt()

	s.txMemPool.Halt()

	// Signal the remaining goroutines to quit.
	close(s.quit)
	return
}

// WaitForShutdown blocks until the main listener and peer handlers are stopped.
func (s *NodeServer) WaitForShutdown() {
	s.wg.Wait()
	srvrLog.Infof("Server shutdown complete")
}

// ScheduleShutdown schedules a NodeServer shutdown after the specified duration.
// It also dynamically adjusts how often to warn the NodeServer is going down based
// on remaining duration.
func (s *NodeServer) ScheduleShutdown(duration time.Duration) {
	// Don't schedule shutdown more than once.
	if atomic.AddInt32(&s.shutdownSched, 1) != 1 {
		return
	}
	srvrLog.Warnf("Server shutdown in %v", duration)
	go func() {
		remaining := duration
		tickDuration := dynamicTickDuration(remaining)
		done := time.After(remaining)
		ticker := time.NewTicker(tickDuration)
	out:
		for {
			select {
			case <-done:
				ticker.Stop()
				s.Stop()
				break out
			case <-ticker.C:
				remaining = remaining - tickDuration
				if remaining < time.Second {
					continue
				}

				// Change tick duration dynamically based on remaining time.
				newDuration := dynamicTickDuration(remaining)
				if tickDuration != newDuration {
					tickDuration = newDuration
					ticker.Stop()
					ticker = time.NewTicker(tickDuration)
				}
				srvrLog.Warnf("Server shutdown in %v", remaining)
			}
		}
	}()
}

func (s *NodeServer) upnpUpdateThread() {
	// Go off immediately to prevent code duplication, thereafter we renew
	// lease every 15 minutes.
	timer := time.NewTimer(0 * time.Second)
	lport, _ := strconv.ParseInt(chaincfg.ActiveNetParams.DefaultPort, 10, 16)
	first := true
out:
	for {
		select {
		case <-timer.C:
			// TODO: pick external port  more cleverly
			// TODO: know which ports we are listening to on an external net.
			// TODO: if specific listen port doesn't work then ask for wildcard
			// listen port?
			// XXX this assumes timeout is in seconds.
			listenPort, err := s.nat.AddPortMapping("tcp", int(lport), int(lport),
				"btcd listen port", 20*60)
			if err != nil {
				srvrLog.Warnf("can't add UPnP port mapping: %v", err)
			}
			if first && err == nil {
				// TODO: look this up periodically to see if upnp domain changed
				// and so did ip.
				externalip, err := s.nat.GetExternalAddress()
				if err != nil {
					srvrLog.Warnf("UPnP can't get external address: %v", err)
					continue out
				}
				na := protos.NewNetAddressIPPort(externalip, uint16(listenPort),
					s.services)
				err = s.addrManager.AddLocalAddress(na, addrmgr.UpnpPrio)
				if err != nil {
					// XXX DeletePortMapping?
				}
				srvrLog.Warnf("Successfully bound via UPnP to %s", addrmgr.NetAddressKey(na))
				first = false
			}
			timer.Reset(time.Minute * 15)
		case <-s.quit:
			break out
		}
	}

	timer.Stop()

	if err := s.nat.DeletePortMapping("tcp", int(lport), int(lport)); err != nil {
		srvrLog.Warnf("unable to remove UPnP port mapping: %v", err)
	} else {
		srvrLog.Debugf("successfully disestablished UPnP port mapping")
	}

	s.wg.Done()
}

// newServer returns a new btcd NodeServer configured to listen on addr for the
// flow network type specified by chainParams.  Use start to begin accepting
// connections from peers.
func NewServer(db database.Transactor, stateDB database.Database, agentBlacklist, agentWhitelist []string,
	chainParams *chaincfg.Params, interrupt <-chan struct{}, shutdownRequestChannel chan struct{}) (*NodeServer, error) {
	UseLogger()
	services := defaultServices
	if chaincfg.Cfg.NoPeerBloomFilters {
		services &^= common.SFNodeBloom
	}
	if chaincfg.Cfg.NoCFilters {
		services &^= common.SFNodeCF
	}

	cfg := chaincfg.Cfg

	genesisBlock, err := asiutil.LoadBlockFromFile(cfg.GenesisBlockFile)
	if err != nil {
		strErr := "Load genesis block error, " + err.Error()
		return nil, errors.New(strErr)
	}
	genesisHash := asiutil.NewBlock(genesisBlock).Hash()
	if *chaincfg.ActiveNetParams.GenesisHash != *genesisHash {
		strErr := fmt.Sprintf("Load genesis block genesis hash mismatch expected %s, but %s",
			chaincfg.ActiveNetParams.GenesisHash.String(), genesisHash.String())
		return nil, errors.New(strErr)
	}
	chaincfg.ActiveNetParams.GenesisBlock = genesisBlock

	nap := fnet.NewNetAdapter(cfg.Proxy, cfg.ProxyUser, cfg.ProxyPass,
		cfg.OnionProxy, cfg.OnionProxyUser, cfg.ProxyPass, cfg.TorIsolation, cfg.NoOnion)

	amgr := addrmgr.New(chaincfg.Cfg.DataDir, nap)

	var listeners []net.Listener
	var nat fnet.NAT
	if !chaincfg.Cfg.DisableListen {
		var err error
		listeners, nat, err = initListeners(amgr, chaincfg.Cfg.Listeners, services)
		if err != nil {
			return nil, err
		}
		if len(listeners) == 0 {
			return nil, errors.New("no valid listen address")
		}
	}

	if len(agentBlacklist) > 0 {
		srvrLog.Infof("User-agent blacklist %s", agentBlacklist)
	}
	if len(agentWhitelist) > 0 {
		srvrLog.Infof("User-agent whitelist %s", agentWhitelist)
	}

	s := NodeServer{
		chainParams:          chainParams,
		addrManager:          amgr,
		newPeers:             make(chan *serverPeer, chaincfg.Cfg.MaxPeers),
		donePeers:            make(chan *serverPeer, chaincfg.Cfg.MaxPeers),
		banPeers:             make(chan *serverPeer, chaincfg.Cfg.MaxPeers),
		query:                make(chan interface{}),
		relayInv:             make(chan relayMsg, chaincfg.Cfg.MaxPeers*2),
		broadcast:            make(chan broadcastMsg, chaincfg.Cfg.MaxPeers),
		quit:                 make(chan struct{}),
		modifyRebroadcastInv: make(chan interface{}),
		peerHeightsUpdate:    make(chan updatePeerHeightsMsg),
		nat:                  nat,
		db:                   db,
		timeSource:           blockchain.NewMedianTime(),
		services:             services,
		cfCheckptCaches:      make(map[protos.FilterType][]cfHeaderKV),
		agentBlacklist:       agentBlacklist,
		agentWhitelist:       agentWhitelist,
		startupTime:          time.Now().Unix(),
	}

	// Create the transaction and address indexes.
	var indexes []blockchain.Indexer
	s.txIndex = indexers.NewTxIndex(db)
	indexes = append(indexes, s.txIndex)
	s.templateIndex = indexers.NewTemplateIndex(db)
	indexes = append(indexes, s.templateIndex)
	s.addrIndex = indexers.NewAddrIndex(db)
	indexes = append(indexes, s.addrIndex)
	// Create cf index if needed
	if !chaincfg.Cfg.NoCFilters {
		s.cfIndex = indexers.NewCfIndex(db)
		indexes = append(indexes, s.cfIndex)
	}

	// Create an index manager if any of the optional indexes are enabled.
	var indexManager blockchain.IndexManager
	if len(indexes) > 0 {
		indexManager = indexers.NewManager(db, indexes)
	}

	// Create a contract manager.
	contractManager := syscontract.NewContractManager()

	// Create a btc rpc client
	btcClient, err := minersync.NewBtcClient()
	if err != nil {
		return nil, err
	}

	// Merge given checkpoints with the default ones unless they are disabled.
	var checkpoints []chaincfg.Checkpoint
	if !chaincfg.Cfg.DisableCheckpoints {
		checkpoints = mergeCheckpoints(s.chainParams.Checkpoints, chaincfg.Cfg.AddCheckpoints)
	}

	// Create a new block chain instance with the appropriate configuration.
	acc, err := crypto.NewAccount(chaincfg.Cfg.Privatekey)
	if acc == nil {
		srvrLog.Warn("No miner configuration")
	} else {
		srvrLog.Infof("miner address=%v", acc.Address.String())
	}

	// Create a round manager.
	roundManger := consensus.NewRoundManager(cfg.Consensustype, acc)
	if roundManger == nil {
		return nil, fmt.Errorf("new %v round manager error", cfg.Consensustype)
	}

	// create fees chan
	feesChan := make(chan interface{})

	s.chain, err = blockchain.New(&blockchain.Config{
		DB:              s.db,
		Interrupt:       interrupt,
		ChainParams:     s.chainParams,
		Checkpoints:     checkpoints,
		TimeSource:      s.timeSource,
		IndexManager:    indexManager,
		StateDB:         stateDB,
		TemplateIndex:   s.templateIndex,
		BtcClient:       btcClient,
		RoundManager:    roundManger,
		ContractManager: contractManager,
		FeesChan:        feesChan,
	}, chaincfg.Cfg)
	if err != nil {
		return nil, err
	}

	txC := mempool.Config{
		Policy: mempool.Policy{
			MaxOrphanTxs:      chaincfg.Cfg.MaxOrphanTxs,
			MaxOrphanTxSize:   chaincfg.Cfg.MaxOrphanTxSize,
			MinRelayTxPrice:   chaincfg.Cfg.MinTxPrice,
			MaxTxVersion:      2,
			RejectReplacement: cfg.RejectReplacement,
		},
		FetchUtxoView:  s.chain.FetchUtxoView,
		Chain:          s.chain,
		BestHeight:     func() int32 { return s.chain.BestSnapshot().Height },
		MedianTimePast: func() int64 { return s.chain.BestSnapshot().TimeStamp },
		CalcSequenceLock: func(tx *asiutil.Tx, view *txo.UtxoViewpoint) (*blockchain.SequenceLock, error) {
			return s.chain.CalcSequenceLock(tx, view, true)
		},
		AddrIndex:              s.addrIndex,
		FeesChan:               feesChan,
		CheckTransactionInputs: blockchain.CheckTransactionInputs,
	}
	s.txMemPool = mempool.New(&txC)
	s.sigMemPool = mempool.NewSigPool()

	s.syncManager, err = netsync.New(&netsync.Config{
		PeerNotifier:       &s,
		Chain:              s.chain,
		TxMemPool:          s.txMemPool,
		SigMemPool:         s.sigMemPool,
		ChainParams:        s.chainParams,
		DisableCheckpoints: chaincfg.Cfg.DisableCheckpoints,
		MaxPeers:           chaincfg.Cfg.MaxPeers,
		Account:            acc,
		BroadcastMessage: func(msg protos.Message, exclPeers ...interface{}) {
			s.BroadcastMessage(msg)
		},
	})
	if err != nil {
		return nil, err
	}

	// Create the mining policy and block template generator based on the
	// configuration options.
	policy := mining.Policy{
		TxMinPrice: chaincfg.Cfg.MinTxPrice,
		BlockProductedTimeOut: cfg.BlkProductedTimeOut,
		TxConnectTimeOut: cfg.TxConnectTimeOut,
		UtxoValidateTimeOut: cfg.UtxoValidateTimeOut,
	}
	blockTemplateGenerator := mining.NewBlkTmplGenerator(&policy,
		s.txMemPool, s.sigMemPool, s.chain)

	consensusConfig := params.Config{
		BlockTemplateGenerator: blockTemplateGenerator,
		ProcessBlock:           s.syncManager.ProcessBlock,
		IsCurrent:              s.syncManager.IsCurrentAndCheckAccepted,
		ProcessSig:             s.sigMemPool.ProcessSig,
		Chain:                  s.chain,
		GasFloor:     common.GasFloor,
		GasCeil:      common.GasCeil,
		RoundManager: roundManger,
		Account:      acc,
	}

	s.consensus, err = consensus.NewConsensusService(chaincfg.Cfg.Consensustype, &consensusConfig)
	if err != nil {
		return nil, err
	}

	// Only setup a function to return new addresses to connect to when
	// not running in connect-only mode.  The simulation network is always
	// in connect-only mode since it is only intended to connect to
	// specified peers and actively avoid advertising and connecting to
	// discovered peers in order to prevent it from becoming a public test
	// network.
	var newAddressFunc func() (net.Addr, error)
	if !chaincfg.Cfg.TestNet &&
		!chaincfg.Cfg.DevelopNet && len(chaincfg.Cfg.ConnectPeers) == 0 {
		newAddressFunc = func() (net.Addr, error) {
			for tries := 0; tries < 100; tries++ {
				addr := s.addrManager.GetAddress()
				if addr == nil {
					break
				}

				// Address will not be invalid, local or unroutable
				// because addrmanager rejects those on addition.
				// Just check that we don't already have an address
				// in the same group so that we are not connecting
				// to the same network segment at the expense of
				// others.
				key := addrmgr.GroupKey(addr.NetAddress())
				if s.OutboundGroupCount(key) != 0 {
					continue
				}

				// only allow recent nodes (10mins) after we failed 30
				// times
				if tries < 30 && time.Since(addr.LastAttempt()) < 10*time.Minute {
					continue
				}

				// allow nondefault ports after 50 failed tries.
				if tries < 50 && fmt.Sprintf("%d", addr.NetAddress().Port) !=
					chaincfg.ActiveNetParams.DefaultPort {
					continue
				}

				// Mark an attempt for the valid address.
				s.addrManager.Attempt(addr.NetAddress())

				addrString := addrmgr.NetAddressKey(addr.NetAddress())
				return amgr.AddrStringToNetAddr(addrString)
			}

			return nil, errors.New("no valid connect address")
		}
	}

	// Create a connection manager.
	targetOutbound := defaultTargetOutbound
	if chaincfg.Cfg.MaxPeers < targetOutbound {
		targetOutbound = chaincfg.Cfg.MaxPeers
	}
	cmgr, err := connmgr.New(&connmgr.Config{
		Listeners:      listeners,
		OnAccept:       s.inboundPeerConnected,
		RetryDuration:  connectionRetryInterval,
		TargetOutbound: uint32(targetOutbound),
		OnConnection:   s.outboundPeerConnected,
		GetNewAddress:  newAddressFunc,
	}, nap)
	if err != nil {
		return nil, err
	}
	s.connManager = cmgr

	s.roundManger = roundManger

	// Start up persistent peers.
	permanentPeers := chaincfg.Cfg.ConnectPeers
	if len(permanentPeers) == 0 {
		permanentPeers = chaincfg.Cfg.AddPeers
	}
	for _, addr := range permanentPeers {
		netAddr, err := amgr.AddrStringToNetAddr(addr)
		if err != nil {
			return nil, err
		}

		go s.connManager.Connect(&connmgr.ConnReq{
			Addr:      netAddr,
			Permanent: true,
		})
	}

	if !chaincfg.Cfg.DisableRPC {
		// Setup listeners for the configured RPC listen addresses and
		// TLS settings.
		serverConfig := &rpcserverConfig{
			ShutdownRequestChannel:shutdownRequestChannel,
			Listeners:       nil,
			StartupTime:     s.startupTime,
			ConnMgr:         &rpcConnManager{&s},
			SyncMgr:         &rpcSyncMgr{&s, s.syncManager},
			TimeSource:      s.timeSource,
			Chain:           s.chain,
			ChainParams:     chainParams,
			DB:              db,
			TxMemPool:       s.txMemPool,
			TxIndex:         s.txIndex,
			AddrIndex:       s.addrIndex,
			CfIndex:         s.cfIndex,
			Nap:             nap,
			ConsensusServer: s.consensus,
			ContractMgr:     contractManager,

			BlockTemplateGenerator: blockTemplateGenerator,
		}

		nodeCfg := node.Config{
			DataDir:      chaincfg.DefaultAppDataDir,
			HTTPEndpoint: cfg.HTTPEndpoint,
			HTTPModules:  append(cfg.HTTPModules, "eth", "shh", "asimov"),
			HTTPTimeouts: cfg.HTTPTimeouts,
			WSEndpoint:   cfg.WSEndpoint,
			WSOrigins:    cfg.WSOrigins,
			WSModules:    append(cfg.WSModules, "eth", "shh", "asimov"),
		}
		nodeCfg.IPCPath = "asimov.ipc"
		nodeCfg.HTTPCors = []string{"*"}
		nodeCfg.HTTPVirtualHosts = []string{"*"}
		// cfg.WSOrigins = []string{"*"}
		nodeCfg.Logger = logger.GetLogger("RPCS")
		nodeCfg.NoUSB = true

		s.stack, err = node.New(&nodeCfg)
		if err != nil {
			srvrLog.Errorf("Failed to create the protocol stack: %v", err)
			os.Exit(1)
		}

		s.stack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
			fullNode, err := NewAsimovRpcService(ctx, s.stack, serverConfig)

			if err != nil {
				srvrLog.Errorf("Failed to register the RPC service: %v", err)
				os.Exit(1)
			}
			return fullNode, err
		})

		//util.RegisterEthService(s.stack, s.chain)
	}

	return &s, nil
}

// initListeners initializes the configured net listeners and adds any bound
// addresses to the address manager. Returns the listeners and a NAT interface,
// which is non-nil if UPnP is in use.
func initListeners(amgr *addrmgr.AddrManager, listenAddrs []string, services common.ServiceFlag) ([]net.Listener, fnet.NAT, error) {
	// Listen for TCP connections at the configured addresses
	netAddrs, err := addrmgr.ParseListeners(listenAddrs)
	if err != nil {
		return nil, nil, err
	}

	listeners := make([]net.Listener, 0, len(netAddrs))
	for _, addr := range netAddrs {
		listener, err := net.Listen(addr.Network(), addr.String())
		if err != nil {
			srvrLog.Warnf("Can't listen on %s: %v", addr, err)
			continue
		}
		listeners = append(listeners, listener)
	}

	var nat fnet.NAT
	if len(chaincfg.Cfg.ExternalIPs) != 0 {
		defaultPort, err := strconv.ParseUint(chaincfg.ActiveNetParams.DefaultPort, 10, 16)
		if err != nil {
			srvrLog.Errorf("Can not parse default port %s for active chain: %v",
				chaincfg.ActiveNetParams.DefaultPort, err)
			return nil, nil, err
		}

		for _, sip := range chaincfg.Cfg.ExternalIPs {
			eport := uint16(defaultPort)
			host, portstr, err := net.SplitHostPort(sip)
			if err != nil {
				// no port, use default.
				host = sip
			} else {
				port, err := strconv.ParseUint(portstr, 10, 16)
				if err != nil {
					srvrLog.Warnf("Can not parse port from %s for "+
						"externalip: %v", sip, err)
					continue
				}
				eport = uint16(port)
			}
			na, err := amgr.HostToNetAddress(host, eport, services)
			if err != nil {
				srvrLog.Warnf("Not adding %s as externalip: %v", sip, err)
				continue
			}

			err = amgr.AddLocalAddress(na, addrmgr.ManualPrio)
			if err != nil {
				amgrLog.Warnf("Skipping specified external IP: %v", err)
			}
		}
	} else {
		if chaincfg.Cfg.Upnp {
			var err error
			nat, err = fnet.Discover()
			if err != nil {
				srvrLog.Warnf("Can't discover upnp: %v", err)
			}
			// nil nat here is fine, just means no upnp on network.
		}

		// Add bound addresses to address manager to be advertised to peers.
		for _, listener := range listeners {
			addr := listener.Addr().String()
			err := addLocalAddress(amgr, addr, services)
			if err != nil {
				amgrLog.Warnf("Skipping bound address %s: %v", addr, err)
			}
		}
	}

	return listeners, nat, nil
}

// addLocalAddress adds an address that this node is listening on to the
// address manager so that it may be relayed to peers.
func addLocalAddress(addrMgr *addrmgr.AddrManager, addr string, services common.ServiceFlag) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return err
	}

	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		// If bound to unspecified address, advertise all local interfaces
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return err
		}

		for _, addr := range addrs {
			ifaceIP, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}

			// If bound to 0.0.0.0, do not add IPv6 interfaces and if bound to
			// ::, do not add IPv4 interfaces.
			if (ip.To4() == nil) != (ifaceIP.To4() == nil) {
				continue
			}

			netAddr := protos.NewNetAddressIPPort(ifaceIP, uint16(port), services)
			addrMgr.AddLocalAddress(netAddr, addrmgr.BoundPrio)
		}
	} else {
		netAddr, err := addrMgr.HostToNetAddress(host, uint16(port), services)
		if err != nil {
			return err
		}

		addrMgr.AddLocalAddress(netAddr, addrmgr.BoundPrio)
	}

	return nil
}

// dynamicTickDuration is a convenience function used to dynamically choose a
// tick duration based on remaining time.  It is primarily used during
// NodeServer shutdown to make shutdown warnings more frequent as the shutdown time
// approaches.
func dynamicTickDuration(remaining time.Duration) time.Duration {
	switch {
	case remaining <= time.Second*5:
		return time.Second
	case remaining <= time.Second*15:
		return time.Second * 5
	case remaining <= time.Minute:
		return time.Second * 15
	case remaining <= time.Minute*5:
		return time.Minute
	case remaining <= time.Minute*15:
		return time.Minute * 5
	case remaining <= time.Hour:
		return time.Minute * 15
	}
	return time.Hour
}

// checkpointSorter implements sort.Interface to allow a slice of checkpoints to
// be sorted.
type checkpointSorter []chaincfg.Checkpoint

// Len returns the number of checkpoints in the slice.  It is part of the
// sort.Interface implementation.
func (s checkpointSorter) Len() int {
	return len(s)
}

// Swap swaps the checkpoints at the passed indices.  It is part of the
// sort.Interface implementation.
func (s checkpointSorter) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// Less returns whether the checkpoint with index i should sort before the
// checkpoint with index j.  It is part of the sort.Interface implementation.
func (s checkpointSorter) Less(i, j int) bool {
	return s[i].Height < s[j].Height
}

// mergeCheckpoints returns two slices of checkpoints merged into one slice
// such that the checkpoints are sorted by height.  In the case the additional
// checkpoints contain a checkpoint with the same height as a checkpoint in the
// default checkpoints, the additional checkpoint will take precedence and
// overwrite the default one.
func mergeCheckpoints(defaultCheckpoints, additional []chaincfg.Checkpoint) []chaincfg.Checkpoint {
	// Create a map of the additional checkpoints to remove duplicates while
	// leaving the most recently-specified checkpoint.
	extra := make(map[int32]chaincfg.Checkpoint)
	for _, checkpoint := range additional {
		extra[checkpoint.Height] = checkpoint
	}

	// Add all default checkpoints that do not have an override in the
	// additional checkpoints.
	numDefault := len(defaultCheckpoints)
	checkpoints := make([]chaincfg.Checkpoint, 0, numDefault+len(extra))
	for _, checkpoint := range defaultCheckpoints {
		if _, exists := extra[checkpoint.Height]; !exists {
			checkpoints = append(checkpoints, checkpoint)
		}
	}

	// Append the additional checkpoints and return the sorted results.
	for _, checkpoint := range extra {
		checkpoints = append(checkpoints, checkpoint)
	}
	sort.Sort(checkpointSorter(checkpoints))
	return checkpoints
}

// HasUndesiredUserAgent determines whether the server should continue to pursue
// a connection with this peer based on its advertised user agent. It performs
// the following steps:
// 1) Reject the peer if it contains a blacklisted agent.
// 2) If no whitelist is provided, accept all user agents.
// 3) Accept the peer if it contains a whitelisted agent.
// 4) Reject all other peers.
func (sp *serverPeer) HasUndesiredUserAgent(blacklistedAgents, whitelistedAgents []string) bool {

	agent := sp.UserAgent()

	// First, if peer's user agent contains any blacklisted substring, we
	// will ignore the connection request.
	for _, blacklistedAgent := range blacklistedAgents {
		if strings.Contains(agent, blacklistedAgent) {
			srvrLog.Debugf("Ignoring peer %s, user agent "+
				"contains blacklisted user agent: %s", sp,
				agent)
			return true
		}
	}

	// If no whitelist is provided, we will accept all user agents.
	if len(whitelistedAgents) == 0 {
		return false
	}

	// Peer's user agent passed blacklist. Now check to see if it contains
	// one of our whitelisted user agents, if so accept.
	for _, whitelistedAgent := range whitelistedAgents {
		if strings.Contains(agent, whitelistedAgent) {
			return false
		}
	}

	// Otherwise, the peer's user agent was not included in our whitelist.
	// Ignore just in case it could stall the initial block download.
	srvrLog.Debugf("Ignoring peer %s, user agent: %s not found in "+
		"whitelist", sp, agent)

	return true
}

func (sp *serverPeer) OnBan(_ *peer.Peer) {
	sp.server.BanPeer(sp)
}
