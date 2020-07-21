// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package servers

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/rpcs/rpcjson"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math/rand"
	"net"
	"time"

	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/blockchain/indexers"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	fnet "github.com/AsimovNetwork/asimov/common/net"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/mempool"
	"github.com/AsimovNetwork/asimov/mining"
	"github.com/AsimovNetwork/asimov/peer"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/txscript"
)

const (
	// maxProtocolVersion is the max protocol version the NodeServer supports.
	maxProtocolVersion = 1
)

// internalRPCError is a convenience function to convert an internal error to
// an RPC error with the appropriate code set.  It also logs the error to the
// RPC NodeServer subsystem since internal errors really should not occur.  The
// context parameter is only used in the log message and may be empty if it's
// not needed.
func internalRPCError(errStr, context string) *rpcjson.RPCError {
	logStr := errStr
	if context != "" {
		logStr = context + ": " + errStr
	}
	rpcsLog.Error(logStr)
	return rpcjson.NewRPCError(rpcjson.ErrRPCInternal.Code, logStr)
}

// rpcDecodeHexError is a convenience function for returning a nicely formatted
// RPC error which indicates the provided hex string failed to decode.
func rpcDecodeHexError(gotHex string) *rpcjson.RPCError {
	return rpcjson.NewRPCError(rpcjson.ErrRPCDecodeHexString,
		fmt.Sprintf("Argument must be hexadecimal string (not %q)",
			gotHex))
}

// rpcNoTxInfoError is a convenience function for returning a nicely formatted
// RPC error which indicates there is no information available for the provided
// transaction hash.
func rpcNoTxInfoError(txHash *common.Hash) *rpcjson.RPCError {
	return rpcjson.NewRPCError(rpcjson.ErrRPCNoTxInfo,
		fmt.Sprintf("No information available about transaction %v",
			txHash))
}

// messageToHex serializes a message to the protos protocol encoding using the
// latest protocol version and returns a hex-encoded string of the result.
func messageToHex(msg protos.Message) (string, error) {
	var buf bytes.Buffer
	if err := msg.VVSEncode(&buf, maxProtocolVersion, protos.BaseEncoding); err != nil {
		context := fmt.Sprintf("Failed to encode msg of type %T", msg)
		return "", internalRPCError(err.Error(), context)
	}

	return hex.EncodeToString(buf.Bytes()), nil
}

// createVinList returns a slice of JSON objects for the inputs of the passed
// transaction.
func createVinList(mtx *protos.MsgTx) []rpcjson.Vin {
	// Coinbase transactions only have a single txin by definition.
	vinList := make([]rpcjson.Vin, len(mtx.TxIn))
	if blockchain.IsCoinBaseTx(mtx) {
		txIn := mtx.TxIn[0]
		vinList[0].Coinbase = hex.EncodeToString(txIn.SignatureScript)
		vinList[0].Sequence = txIn.Sequence
		return vinList
	}

	for i, txIn := range mtx.TxIn {
		// The disassembled string will contain [error] inline
		// if the script doesn't fully parse, so ignore the
		// error here.
		disbuf, _ := txscript.DisasmString(txIn.SignatureScript)

		vinEntry := &vinList[i]
		vinEntry.Txid = txIn.PreviousOutPoint.Hash.String()
		vinEntry.Vout = txIn.PreviousOutPoint.Index
		vinEntry.Sequence = txIn.Sequence
		vinEntry.ScriptSig = &rpcjson.ScriptSig{
			Asm: disbuf,
			Hex: hex.EncodeToString(txIn.SignatureScript),
		}
	}

	return vinList
}

// createVoutList returns a slice of JSON objects for the outputs of the passed
// transaction.
func createVoutList(mtx *protos.MsgTx, filterAddrMap map[string]struct{}) []rpcjson.Vout {
	voutList := make([]rpcjson.Vout, 0, len(mtx.TxOut))
	for i, v := range mtx.TxOut {
		// The disassembled string will contain [error] inline if the
		// script doesn't fully parse, so ignore the error here.
		disbuf, _ := txscript.DisasmString(v.PkScript)

		// Ignore the error here since an error means the script
		// couldn't parse and there is no additional information about
		// it anyways.
		scriptClass, addrs, reqSigs, _ := txscript.ExtractPkScriptAddrs(v.PkScript)

		// Encode the addresses while checking if the address passes the
		// filter when needed.
		passesFilter := len(filterAddrMap) == 0
		encodedAddrs := make([]string, len(addrs))
		for j, addr := range addrs {
			encodedAddr := addr.EncodeAddress()
			encodedAddrs[j] = encodedAddr

			// No need to check the map again if the filter already
			// passes.
			if passesFilter {
				continue
			}
			if _, exists := filterAddrMap[encodedAddr]; exists {
				passesFilter = true
			}
		}

		if !passesFilter {
			continue
		}

		var vout rpcjson.Vout
		vout.N = uint32(i)
		vout.Value = v.Value
		vout.ScriptPubKey.Addresses = encodedAddrs
		vout.ScriptPubKey.Asm = disbuf
		vout.ScriptPubKey.Hex = hex.EncodeToString(v.PkScript)
		vout.ScriptPubKey.Type = scriptClass.String()
		vout.ScriptPubKey.ReqSigs = int32(reqSigs)
		vout.Data = hex.EncodeToString(v.Data)
		vout.Asset = hex.EncodeToString(v.Asset.Bytes())

		voutList = append(voutList, vout)
	}

	return voutList
}

//calculateTransactionFee returns a slice of JSON objects for fees of passed transaction
func calculateTransactionFee(tx *protos.MsgTx, originTxOuts []protos.TxOut) ([]rpcjson.FeeResult, error) {
	var fees = make([]rpcjson.FeeResult, 0)
	var vinAssetMap = make(map[string]int64)

	if blockchain.IsCoinBaseTx(tx) || blockchain.IsTransferCreateOrMintTx(tx) {
		return fees, nil
	}

	for _, vin := range originTxOuts {
		asset := hex.EncodeToString(vin.Asset.Bytes())
		if _, ok := vinAssetMap[asset]; !ok {
			vinAssetMap[asset] = int64(0)
		}

		vinAssetMap[asset] += vin.Value
	}

	for _, vout := range tx.TxOut {
		asset := hex.EncodeToString(vout.Asset.Bytes())
		if _, ok := vinAssetMap[asset]; ok {
			vinAssetMap[asset] -= vout.Value
		} else {
			return nil, &rpcjson.RPCError{
				Code:    rpcjson.ErrRPCInvalidTxVout,
				Message: "output asset type does not match the input asset type",
			}
		}
	}

	for asset, value := range vinAssetMap {
		if value > 0 {
			fees = append(fees, rpcjson.FeeResult{Value: value, Asset: asset})
		} else if value < 0 {
			return nil, &rpcjson.RPCError{
				Code:    rpcjson.ErrRPCInvalidTxVout,
				Message: "output amount is larger than input",
			}
		}
	}

	return fees, nil
}

func createMsgSignResult(msgSig *protos.MsgBlockSign) (*rpcjson.MsgSignResult, error) {
	msgSignReply := &rpcjson.MsgSignResult{
		BlockHeight:   msgSig.BlockHeight,
		BlockHash:     msgSig.BlockHash.String(),
		Signer:        msgSig.Signer.String(),
		Signature:     hex.EncodeToString(msgSig.Signature[:]),
	}
	return msgSignReply, nil
}

// createTxRawResult converts the passed transaction and associated parameters
// to a raw transaction JSON object.
func createTxResult(rpcCfg rpcserverConfig, mtx *protos.MsgTx,
	txHash string, blkHeader *protos.BlockHeader, blkHash string,
	blkHeight int32, chainHeight int32, isvtx bool) (*rpcjson.TxResult, error) {

	mtxHex, err := messageToHex(mtx)
	if err != nil {
		return nil, err
	}

	vins, originTxouts, err := createVinListPrevOut(rpcCfg, mtx, true, nil)
	vouts := createVoutList(mtx, nil)

	if err != nil {
		return nil, err
	}

	var fee []rpcjson.FeeResult
	if !isvtx {
		fee, err = calculateTransactionFee(mtx, originTxouts)
		if err != nil {
			return nil, err
		}
	}

	txReply := &rpcjson.TxResult{
		Hex:      mtxHex,
		Txid:     txHash,
		Hash:     mtx.TransactionHash().String(),
		Size:     int32(mtx.SerializeSize()),
		Vin:      vins,
		Vout:     vouts,
		Version:  mtx.Version,
		LockTime: mtx.LockTime,
		Fee:      fee,
		GasLimit: int64(mtx.TxContract.GasLimit),
	}

	if blkHeader != nil {
		// This is not a typo, they are identical in bitcoind as well.
		txReply.Time = blkHeader.Timestamp
		txReply.Blocktime = blkHeader.Timestamp
		txReply.BlockHash = blkHash
		txReply.Confirmations = int64(1 + chainHeight - blkHeight)
	}

	return txReply, nil
}

// createTxRawResult converts the passed transaction and associated parameters
// to a raw transaction JSON object.
func createTxRawResult(mtx *protos.MsgTx,
	txHash string, blkHeader *protos.BlockHeader, blkHash string,
	blkHeight int32, chainHeight int32) (*rpcjson.TxRawResult, error) {

	mtxHex, err := messageToHex(mtx)
	if err != nil {
		return nil, err
	}

	txReply := &rpcjson.TxRawResult{
		Hex:      mtxHex,
		Txid:     txHash,
		Hash:     mtx.TransactionHash().String(),
		Size:     int32(mtx.SerializeSize()),
		Vin:      createVinList(mtx),
		Vout:     createVoutList(mtx, nil),
		Version:  mtx.Version,
		LockTime: mtx.LockTime,
		GasLimit: int64(mtx.TxContract.GasLimit),
	}

	if blkHeader != nil {
		// This is not a typo, they are identical in bitcoind as well.
		txReply.Time = blkHeader.Timestamp
		txReply.Blocktime = blkHeader.Timestamp
		txReply.BlockHash = blkHash
		txReply.Confirmations = int64(1 + chainHeight - blkHeight)
	}

	return txReply, nil
}

// retrievedTx represents a transaction that was either loaded from the
// transaction memory pool or from the database.  When a transaction is loaded
// from the database, it is loaded with the raw serialized bytes while the
// mempool has the fully deserialized structure.  This structure therefore will
// have one of the two fields set depending on where is was retrieved from.
// This is mainly done for efficiency to avoid extra serialization steps when
// possible.
type retrievedTx struct {
	txBytes []byte
	blkHash *common.Hash // Only set when transaction is in a block.
	blkType byte
	tx      *asiutil.Tx
}

// fetchInputTxos fetches the outpoints from all transactions referenced by the
// inputs to the passed transaction by checking the transaction mempool first
// then the transaction index for those already mined into blocks.
func fetchInputTxos(rpcCfg rpcserverConfig, tx *protos.MsgTx) (map[protos.OutPoint]protos.TxOut, error) {
	mp := rpcCfg.TxMemPool
	originOutputs := make(map[protos.OutPoint]protos.TxOut)
	for txInIndex, txIn := range tx.TxIn {
		if asiutil.IsMintOrCreateInput(txIn) {
			continue
		}
		// Attempt to fetch and use the referenced transaction from the
		// memory pool.
		origin := &txIn.PreviousOutPoint
		originTx, _, err := mp.FetchTransaction(&origin.Hash)
		if originTx != nil {
			txOuts := originTx.MsgTx().TxOut
			if origin.Index >= uint32(len(txOuts)) {
				errStr := fmt.Sprintf("unable to find output "+
					"%v referenced from transaction %s:%d",
					origin, tx.TxHash(), txInIndex)
				return nil, internalRPCError(errStr, "")
			}

			originOutputs[*origin] = *txOuts[origin.Index]
			continue
		}

		// Look up the location of the transaction.
		blockRegion, err := rpcCfg.TxIndex.FetchBlockRegion(origin.Hash[:])
		if err != nil {
			context := "Failed to retrieve transaction location"
			return nil, internalRPCError(err.Error(), context)
		}
		//Look up the location of the
		if blockRegion == nil {
			return nil, rpcNoTxInfoError(&origin.Hash)
		}

		// Load the raw transaction bytes from the database.
		var txBytes []byte
		err = rpcCfg.DB.View(func(dbTx database.Tx) error {
			var err error
			txBytes, err = dbTx.FetchBlockRegion(blockRegion)
			return err
		})

		if err != nil {
			return nil, internalRPCError(err.Error(), origin.Hash.String())
		}

		// Deserialize the transaction
		var msgTx protos.MsgTx
		err = msgTx.Deserialize(bytes.NewReader(txBytes))
		if err != nil {
			context := "Failed to deserialize transaction"
			return nil, internalRPCError(err.Error(), context)
		}

		// Add the referenced output to the map.
		if origin.Index >= uint32(len(msgTx.TxOut)) {
			errStr := fmt.Sprintf("unable to find output %v "+
				"referenced from transaction %s:%d", origin,
				tx.TxHash(), txInIndex)
			return nil, internalRPCError(errStr, "")
		}

		originOutputs[*origin] = *msgTx.TxOut[origin.Index]
	}

	return originOutputs, nil
}

// createVinListPrevOut returns a slice of JSON objects for the inputs of the
// passed transaction.
func createVinListPrevOut(rpcCfg rpcserverConfig, mtx *protos.MsgTx, vinExtra bool, filterAddrMap map[string]struct{}) ([]rpcjson.VinPrevOut, []protos.TxOut, error) {
	// Coinbase transactions only have a single txin by definition.

	if blockchain.IsCoinBaseTx(mtx) || blockchain.IsTransferCreateOrMintTx(mtx) {
		// Only include the transaction if the filter map is empty
		// because a coinbase input has no addresses and so would never
		// match a non-empty filter.
		if len(filterAddrMap) != 0 {
			return nil, nil, nil
		}

		txIn := mtx.TxIn[0]
		vinList := make([]rpcjson.VinPrevOut, 1)
		vinList[0].Coinbase = hex.EncodeToString(txIn.SignatureScript)
		vinList[0].Sequence = txIn.Sequence
		return vinList, nil, nil
	}

	// Use a dynamically sized list to accommodate the address filter.
	vinList := make([]rpcjson.VinPrevOut, 0, len(mtx.TxIn))
	originTxOutList := make([]protos.TxOut, 0)
	// Lookup all of the referenced transaction outputs needed to populate
	// the previous output information if requested.
	var originOutputs map[protos.OutPoint]protos.TxOut
	if vinExtra || len(filterAddrMap) > 0 {
		var err error
		originOutputs, err = fetchInputTxos(rpcCfg, mtx)

		if err != nil {
			return nil, nil, err
		}
	}

	for _, txIn := range mtx.TxIn {
		// The disassembled string will contain [error] inline
		// if the script doesn't fully parse, so ignore the
		// error here.
		disbuf, _ := txscript.DisasmString(txIn.SignatureScript)

		// Create the basic input entry without the additional optional
		// previous output details which will be added later if
		// requested and available.
		prevOut := &txIn.PreviousOutPoint

		if asiutil.IsMintOrCreateInput(txIn) {
			vinList = append(vinList, rpcjson.VinPrevOut{
				Coinbase: hex.EncodeToString(txIn.SignatureScript),
				Sequence: txIn.Sequence,
			})
			continue
		}
		vinEntry := rpcjson.VinPrevOut{
			Txid:     prevOut.Hash.String(),
			Vout:     prevOut.Index,
			Sequence: txIn.Sequence,
			ScriptSig: &rpcjson.ScriptSig{
				Asm: disbuf,
				Hex: hex.EncodeToString(txIn.SignatureScript),
			},
		}

		// Add the entry to the list now if it already passed the filter
		// since the previous output might not be available.
		passesFilter := len(filterAddrMap) == 0
		if passesFilter {
			vinList = append(vinList, vinEntry)
		}

		// Only populate previous output information if requested and
		// available.
		if len(originOutputs) == 0 {
			continue
		}
		originTxOut, ok := originOutputs[*prevOut]
		if !ok {
			continue
		}
		originTxOutList = append(originTxOutList, originTxOut)

		// Ignore the error here since an error means the script
		// couldn't parse and there is no additional information about
		// it anyways.
		_, addrs, _, _ := txscript.ExtractPkScriptAddrs(
			originTxOut.PkScript)

		// Encode the addresses while checking if the address passes the
		// filter when needed.
		encodedAddrs := make([]string, len(addrs))
		for j, addr := range addrs {
			encodedAddr := addr.EncodeAddress()
			encodedAddrs[j] = encodedAddr
			// No need to check the map again if the filter already
			// passes.
			if passesFilter {
				continue
			}
			if _, exists := filterAddrMap[encodedAddr]; exists {
				passesFilter = true
			}
		}

		// Ignore the entry if it doesn't pass the filter.
		if !passesFilter {
			continue
		}

		// Add entry to the list if it wasn't already done above.
		if len(filterAddrMap) != 0 {
			vinList = append(vinList, vinEntry)
		}

		// Update the entry with previous output information if
		// requested.
		if vinExtra {
			vinListEntry := &vinList[len(vinList)-1]
			vinListEntry.PrevOut = &rpcjson.PrevOut{
				Addresses: encodedAddrs,
				Value:     originTxOut.Value,
				Asset:     hex.EncodeToString(originTxOut.Asset.Bytes()),
				Data:      hex.EncodeToString(originTxOut.Data),
			}
		}
	}

	return vinList, originTxOutList, nil
}

// fetchMempoolTxnsForAddress queries the address index for all unconfirmed
// transactions that involve the provided address.  The results will be limited
// by the number to skip and the number requested.
func fetchMempoolTxnsForAddress(cfg *rpcserverConfig, addr common.IAddress, numToSkip, numRequested uint32) ([]*asiutil.Tx, uint32) {
	// There are no entries to return when there are less available than the
	// number being skipped.
	mpTxns := cfg.AddrIndex.UnconfirmedTxnsForAddress(addr)
	numAvailable := uint32(len(mpTxns))
	if numToSkip > numAvailable {
		return nil, numAvailable
	}

	// Filter the available entries based on the number to skip and number
	// requested.
	rangeEnd := numToSkip + numRequested
	if rangeEnd > numAvailable {
		rangeEnd = numAvailable
	}
	return mpTxns[numToSkip:rangeEnd], numToSkip
}

// rpcserverPeer represents a peer for use with the RPC NodeServer.
//
// The interface contract requires that all of these methods are safe for
// concurrent access.
type rpcserverPeer interface {
	// ToPeer returns the underlying peer instance.
	ToPeer() *peer.Peer

	// IsTxRelayDisabled returns whether or not the peer has disabled
	// transaction relay.
	IsTxRelayDisabled() bool

	// BanScore returns the current integer value that represents how close
	// the peer is to being banned.
	BanScore() uint32

	// FeeFilter returns the requested current minimum fee rate for which
	// transactions should be announced.
	FeeFilter() int32
}

// rpcserverConnManager represents a connection manager for use with the RPC
// NodeServer.
//
// The interface contract requires that all of these methods are safe for
// concurrent access.
type rpcserverConnManager interface {
	// Connect adds the provided address as a new outbound peer.  The
	// permanent flag indicates whether or not to make the peer persistent
	// and reconnect if the connection is lost.  Attempting to connect to an
	// already existing peer will return an error.
	Connect(addr string, permanent bool) error

	// RemoveByID removes the peer associated with the provided id from the
	// list of persistent peers.  Attempting to remove an id that does not
	// exist will return an error.
	RemoveByID(id int32) error

	// RemoveByAddr removes the peer associated with the provided address
	// from the list of persistent peers.  Attempting to remove an address
	// that does not exist will return an error.
	RemoveByAddr(addr string) error

	// DisconnectByID disconnects the peer associated with the provided id.
	// This applies to both inbound and outbound peers.  Attempting to
	// remove an id that does not exist will return an error.
	DisconnectByID(id int32) error

	// DisconnectByAddr disconnects the peer associated with the provided
	// address.  This applies to both inbound and outbound peers.
	// Attempting to remove an address that does not exist will return an
	// error.
	DisconnectByAddr(addr string) error

	// ConnectedCount returns the number of currently connected peers.
	ConnectedCount() int32

	// NetTotals returns the sum of all bytes received and sent across the
	// network for all peers.
	NetTotals() (uint64, uint64)

	// ConnectedPeers returns an array consisting of all connected peers.
	ConnectedPeers() []rpcserverPeer

	// PersistentPeers returns an array consisting of all the persistent
	// peers.
	PersistentPeers() []rpcserverPeer

	// BroadcastMessage sends the provided message to all currently
	// connected peers.
	BroadcastMessage(msg protos.Message)

	// AddRebroadcastInventory adds the provided inventory to the list of
	// inventories to be rebroadcast at random intervals until they show up
	// in a block.
	AddRebroadcastInventory(iv *protos.InvVect, data interface{})

	// RelayTransactions generates and relays inventory vectors for all of
	// the passed transactions to all connected peers.
	RelayTransactions(txns []*mining.TxDesc)
}

// rpcserverSyncManager represents a sync manager for use with the RPC NodeServer.
//
// The interface contract requires that all of these methods are safe for
// concurrent access.
type rpcserverSyncManager interface {
	// IsCurrent returns whether or not the sync manager believes the chain
	// is current as compared to the rest of the network.
	IsCurrent() bool

	// Pause pauses the sync manager until the returned channel is closed.
	Pause() chan<- struct{}

	// SyncPeerID returns the ID of the peer that is currently the peer being
	// used to sync from or 0 if there is none.
	SyncPeerID() int32

	// LocateHeaders returns the headers of the blocks after the first known
	// block in the provided locators until the provided stop hash or the
	// current tip is reached, up to a max of protos.MaxBlockHeadersPerMsg
	// hashes.
	LocateHeaders(locators []*common.Hash, hashStop *common.Hash) []protos.BlockHeader

	// ListPeerStates returns peer addresses formatted string
	ListPeerStates() []string
}

// rpcserverContractManager represents a contract manager for use with the RPC NodeServer.
//
// The interface contract requires that all of these methods are safe for
// concurrent access.
type rpcserverContractManager interface {

	// Get contract information at a given block height
	GetActiveContractByHeight(height int32, contractAddr common.Address) *chaincfg.ContractInfo

	// Get issuing contract address of a given asset
	GetContractAddressByAsset(
		gas uint64,
		block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		assets []string) ([]common.Address, bool, uint64)

	// Get asset information by asset id
	GetAssetInfoByAssetId(
		block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		assets []string) ([]ainterface.AssetInfo, error)

	// Get assets which can be used as transaction fees on Asimov
	GetFees(block *asiutil.Block,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig) (map[protos.Asset]int32, error)

	// Get information of a template
	GetTemplate(
		block *asiutil.Block,
		gas uint64,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		category uint16,
		name string) (ainterface.TemplateWarehouseContent, bool, uint64)

	// Get information of templates
	GetTemplates(
		block *asiutil.Block,
		gas uint64,
		stateDB vm.StateDB,
		chainConfig *params.ChainConfig,
		getCountFunc string,
		getTemplatesFunc string,
		category uint16,
		pageNo int,
		pageSize int) (int, []ainterface.TemplateWarehouseContent, error, uint64)
}

// rpcserverConfig is a descriptor containing the RPC NodeServer configuration.
type rpcserverConfig struct {
	ShutdownRequestChannel chan struct{}
	// Listeners defines a slice of listeners for which the RPC NodeServer will
	// take ownership of and accept connections.  Since the RPC NodeServer takes
	// ownership of these listeners, they will be closed when the RPC NodeServer
	// is stopped.
	Listeners []net.Listener

	// StartupTime is the unix timestamp for when the NodeServer that is hosting
	// the RPC NodeServer started.
	StartupTime int64

	// ConnMgr defines the connection manager for the RPC NodeServer to use.  It
	// provides the RPC NodeServer with a means to do things such as add,
	// remove, connect, disconnect, and query peers as well as other
	// connection-related data and tasks.
	ConnMgr rpcserverConnManager

	// SyncMgr defines the sync manager for the RPC NodeServer to use.
	SyncMgr rpcserverSyncManager

	// ContractMgr defines the contract manager for the RPC NodeServer to use.
	ContractMgr rpcserverContractManager

	// These fields allow the RPC NodeServer to interface with the local block
	// chain data and state.
	TimeSource  blockchain.MedianTimeSource
	Chain       *blockchain.BlockChain
	ChainParams *chaincfg.Params
	DB          database.Transactor

	// TxMemPool defines the transaction memory pool to interact with.
	TxMemPool *mempool.TxPool

	// These fields define any optional indexes the RPC NodeServer can make use
	// of to provide additional data when queried.
	TxIndex   *indexers.TxIndex
	AddrIndex *indexers.AddrIndex
	CfIndex   *indexers.CfIndex

	Nap fnet.NetAdapter

	//consensus server
	ConsensusServer ainterface.Consensus

	BlockTemplateGenerator *mining.BlkTmplGenerator
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
