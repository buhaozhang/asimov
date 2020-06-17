// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package blockchain

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"math/big"
	"sync"
)

const (
	// blockHdrSize is the size of a block header.  This is simply the
	// constant from protos and is only provided here for convenience since
	// protos.BlockHeaderPayload is quite long.
	blockHdrSize = protos.BlockHeaderPayload

	// size of ainterface.Round
	roundHdrDeltaSize = ainterface.RoundPayLoad
)

var (
	// blockIndexBucketName is the name of the db bucket used to house to the
	// block headers and contextual information.
	blockIndexBucketName = []byte("blockheaderidx")

	// roundIndexBucketName is the name of the db bucket used to house to the
	// round and contextual information.
	roundIndexBucketName = []byte("roundidx")

	// hashIndexBucketName is the name of the db bucket used to house to the
	// block hash -> block height index.
	hashIndexBucketName = []byte("hashidx")

	// chainStateKeyName is the name of the db key used to store the best
	// chain state.
	chainStateKeyName = []byte("chainstate")

	// spendJournalBucketName is the name of the db bucket used to house
	// transactions outputs that are spent in each block.
	spendJournalBucketName = []byte("spendjournal")

	// utxoSetBucketName is the name of the db bucket used to house the
	// unspent transaction output set.
	utxoSetBucketName = []byte("utxoset")

	// balanceBucketName is the name of the db bucket used to house the
	// total unspend transaction balance indexed by public key
	balanceBucketName = []byte("balance")

	// lockSetBucketName is the name of the db bucket used to house the
	// lock item indexed by outpoint
	lockSetBucketName = []byte("lockset")

	// byteOrder is the preferred byte order used for serializing numeric
	// fields for storage in the database.
	byteOrder = binary.LittleEndian

	//assetsSetBucketName is the nane of the db bucket used to house the created asset data
	assetsSetBucketName = []byte("assetsSet")

	signatureSetBucketName = []byte("signatureSet")
)

// errNotInMainChain signifies that a block hash or height that is not in the
// main chain was requested.
type errNotInMainChain string

// Error implements the error interface.
func (e errNotInMainChain) Error() string {
	return string(e)
}

// isNotInMainChainErr returns whether or not the passed error is an
// errNotInMainChain error.
func isNotInMainChainErr(err error) bool {
	_, ok := err.(errNotInMainChain)
	return ok
}

// isDbBucketNotFoundErr returns whether or not the passed error is a
// database.Error with an error code of database.ErrBucketNotFound.
func isDbBucketNotFoundErr(err error) bool {
	dbErr, ok := err.(database.Error)
	return ok && dbErr.ErrorCode == database.ErrBucketNotFound
}

// dbFetchVersion use an individual version with the given key from the metadata bucket.
// It is primarily used to track versions on entities such as buckets.
// It returns zero if the provided key does not exist.
func dbFetchVersion(dbTx database.Tx, key []byte) uint32 {
	serialized := dbTx.Metadata().Get(key)
	if serialized == nil {
		return 0
	}

	return byteOrder.Uint32(serialized[:])
}

// dbPutVersion uses an existing database transaction to update the provided
// key in the metadata bucket to the given version.  It is primarily used to
// track versions on entities such as buckets.
func dbPutVersion(dbTx database.Tx, key []byte, version uint32) error {
	var serialized [4]byte
	byteOrder.PutUint32(serialized[:], version)
	return dbTx.Metadata().Put(key, serialized[:])
}

// dbFetchOrCreateVersion uses an existing database transaction to attempt to
// fetch the provided key from the metadata bucket as a version and in the case
// it doesn't exist, it adds the entry with the provided default version and
// returns that.  This is useful during upgrades to automatically handle loading
// and adding version keys as necessary.
func dbFetchOrCreateVersion(dbTx database.Tx, key []byte, defaultVersion uint32) (uint32, error) {
	version := dbFetchVersion(dbTx, key)
	if version == 0 {
		version = defaultVersion
		err := dbPutVersion(dbTx, key, version)
		if err != nil {
			return 0, err
		}
	}

	return version, nil
}


// FetchSpendJournal attempts to retrieve the spend journal, or the set of
// outputs spent for the target block. This provides a view of all the outputs
// that will be consumed once the target block is connected to the end of the
// main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) FetchSpendJournal(targetBlock *asiutil.Block, targetvblock *asiutil.VBlock) ([]txo.SpentTxOut, error) {
	b.chainLock.RLock()
	defer b.chainLock.RUnlock()

	var spendEntries []txo.SpentTxOut
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		spendEntries, err = dbFetchSpendJournalEntry(dbTx, targetBlock, targetvblock)
		return err
	})
	if err != nil {
		return nil, err
	}

	return spendEntries, nil
}

// SpentTxOutHeaderCode returns the calculated header code to be used when
// serializing the provided stxo entry.
func SpentTxOutHeaderCode(stxo *txo.SpentTxOut) uint64 {
	// As described in the serialization format comments, the header code
	// encodes the height shifted over one bit and the coinbase flag in the
	// lowest bit.
	headerCode := uint64(stxo.Height) << 1
	if stxo.IsCoinBase {
		headerCode |= 0x01
	}

	return headerCode
}

// SpentTxOutSerializeSize returns the number of bytes it would take to
// serialize the passed stxo according to the format described above.
func SpentTxOutSerializeSize(stxo *txo.SpentTxOut) int {
	size := serializeSizeVLQ(SpentTxOutHeaderCode(stxo))
	return size + compressedTxOutSize(uint64(stxo.Amount), stxo.PkScript, stxo.Asset)
}

// putSpentTxOut serializes the passed stxo according to the format described
// above directly into the passed target byte slice.  The target byte slice must
// be at least large enough to handle the number of bytes returned by the
// SpentTxOutSerializeSize function or it will panic.
func putSpentTxOut(target []byte, stxo *txo.SpentTxOut) int {
	headerCode := SpentTxOutHeaderCode(stxo)
	offset := putVLQ(target, headerCode)
	return offset + putCompressedTxOut(target[offset:], uint64(stxo.Amount),
		stxo.PkScript, stxo.Asset)
}

// decodeSpentTxOut decodes the passed serialized stxo entry, possibly followed
// by other data, into the passed stxo struct.  It returns the number of bytes
// read.
func decodeSpentTxOut(serialized []byte, stxo *txo.SpentTxOut) (int, error) {
	// Ensure there are bytes to decode.
	if len(serialized) == 0 {
		return 0, common.DeserializeError("no serialized bytes")
	}

	// Deserialize the header code.
	code, offset := deserializeVLQ(serialized)
	if offset >= len(serialized) {
		return offset, common.DeserializeError("unexpected end of data after " +
			"header code")
	}

	// Decode the header code.
	//
	// Bit 0 indicates containing transaction is a coinbase.
	// Bits 1-x encode height of containing transaction.
	stxo.IsCoinBase = code&0x01 != 0
	stxo.Height = int32(code >> 1)

	// Decode the compressed txout.
	amount, pkScript, assetNo, bytesRead, err := decodeCompressedTxOut(
		serialized[offset:])
	offset += bytesRead
	if err != nil {
		return offset, common.DeserializeError(fmt.Sprintf("unable to decode "+
			"txout: %v", err))
	}
	stxo.Amount = int64(amount)
	stxo.PkScript = pkScript
	stxo.Asset = assetNo
	return offset, nil
}

// deserializeSpendJournalEntry decodes the passed serialized byte slice into a
// slice of spent txouts according to the format described in detail above.
//
// Since the serialization format is not self describing, as noted in the
// format comments, this function also requires the transactions that spend the
// txouts.
func deserializeSpendJournalEntry(serialized []byte, txns []*protos.MsgTx, vtxns []*protos.MsgTx) ([]txo.SpentTxOut, error) {
	// Calculate the total number of stxos.
	var numStxos int
	for _, tx := range txns {
		numStxos += len(tx.TxIn)
	}
	for _, tx := range vtxns {
		for _, txin := range tx.TxIn {
			if asiutil.IsMintOrCreateInput(txin) {
				continue
			}
			numStxos++
		}
	}

	// When a block has no spent txouts there is nothing to serialize.
	if len(serialized) == 0 {
		// Ensure the block actually has no stxos.  This should never
		// happen unless there is database corruption or an empty entry
		// erroneously made its way into the database.
		if numStxos != 0 {
			return nil, common.AssertError(fmt.Sprintf("mismatched spend "+
				"journal serialization - no serialization for "+
				"expected %d stxos", numStxos))
		}

		return nil, nil
	}

	// Loop backwards through all transactions so everything is read in
	// reverse order to match the serialization order.
	offset := 0
	stxos := make([]txo.SpentTxOut, numStxos)
	for stxoIdx := numStxos - 1; stxoIdx > -1; stxoIdx-- {
		stxo := &stxos[stxoIdx]
		n, err := decodeSpentTxOut(serialized[offset:], stxo)
		offset += n
		if err != nil {
			return nil, common.DeserializeError(fmt.Sprintf("unable "+
				"to decode stxo: %v", err))
		}
	}

	return stxos, nil
}

// serializeSpendJournalEntry serializes all of the passed spent txouts into a
// single byte slice according to the format described in detail above.
func serializeSpendJournalEntry(stxos []txo.SpentTxOut) []byte {
	if len(stxos) == 0 {
		return nil
	}

	// Calculate the size needed to serialize the entire journal entry.
	var size int
	for i := range stxos {
		size += SpentTxOutSerializeSize(&stxos[i])
	}
	serialized := make([]byte, size)

	// Serialize each individual stxo directly into the slice in reverse
	// order one after the other.
	var offset int
	for i := len(stxos) - 1; i > -1; i-- {
		offset += putSpentTxOut(serialized[offset:], &stxos[i])
	}

	return serialized
}

// dbFetchSpendJournalEntry fetches the spend journal entry for the passed block
// and deserializes it into a slice of spent txout entries.
//
// NOTE: Legacy entries will not have the coinbase flag or height set unless it
// was the final output spend in the containing transaction.  It is up to the
// caller to handle this properly by looking the information up in the utxo set.
func dbFetchSpendJournalEntry(dbTx database.Tx, block *asiutil.Block, vblock *asiutil.VBlock) ([]txo.SpentTxOut, error) {
	// Exclude the coinbase transaction since it can't spend anything.
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)
	serialized := spendBucket.Get(block.Hash()[:])
	coinbaseIdx := len(block.Transactions())-1
	blockTxns := block.MsgBlock().Transactions[:coinbaseIdx]
	vTxns := vblock.MsgVBlock().VTransactions

	stxos, err := deserializeSpendJournalEntry(serialized, blockTxns, vTxns)
	if err != nil {
		// Ensure any deserialization errors are returned as database
		// corruption errors.
		if common.IsDeserializeErr(err) {
			return nil, database.Error{
				ErrorCode: database.ErrCorruption,
				Description: fmt.Sprintf("corrupt spend "+
					"information for %v: %v", block.Hash(),
					err),
			}
		}

		return nil, err
	}

	return stxos, nil
}

// dbPutSpendJournalEntry uses an existing database transaction to update the
// spend journal entry for the given block hash using the provided slice of
// spent txouts.   The spent txouts slice must contain an entry for every txout
// the transactions in the block spend in the order they are spent.
func dbPutSpendJournalEntry(dbTx database.Tx, blockHash *common.Hash, stxos []txo.SpentTxOut) error {
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)
	serialized := serializeSpendJournalEntry(stxos)
	return spendBucket.Put(blockHash[:], serialized)
}

// dbRemoveSpendJournalEntry uses an existing database transaction to remove the
// spend journal entry for the passed block hash.
func dbRemoveSpendJournalEntry(dbTx database.Tx, blockHash *common.Hash) error {
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)
	return spendBucket.Delete(blockHash[:])
}

// -----------------------------------------------------------------------------
// The unspent transaction output (utxo) set consists of an entry for each
// unspent output using a format that is optimized to reduce space using domain
// specific compression algorithms.  This format is a slightly modified version
// of the format used in Bitcoin Core.
//
// Each entry is keyed by an outpoint as specified below.  It is important to
// note that the key encoding uses a VLQ, which employs an MSB encoding so
// iteration of utxos when doing byte-wise comparisons will produce them in
// order.
//
// The serialized key format is:
//   <hash><output index>
//
//   Field                Type             Size
//   hash                 common.Hash   common.HashLength
//   output index         VLQ              variable
//
// The serialized value format is:
//
//   <header code><compressed txout>
//
//   Field                Type     Size
//   header code          VLQ      variable
//   compressed txout
//     compressed amount  VLQ      variable
//     compressed script  []byte   variable
//
// The serialized header code format is:
//   bit 0 - containing transaction is a coinbase
//   bits 1-x - height of the block that contains the unspent txout
//
// Example 1:
// From tx in main blockchain:
// Blk 1, 0e3e2357e806b6cdb1f70b54c3a3a17b6714ee1f0e68bebb44a74b1efd512098:0
//
//    03320496b538e853519c726a2c91e61ec11600ae1390813a627c66fb8be7947be63c52
//    <><------------------------------------------------------------------>
//     |                                          |
//   header code                         compressed txout
//
//  - header code: 0x03 (coinbase, height 1)
//  - compressed txout:
//    - 0x32: VLQ-encoded compressed amount for 5000000000 (50 BTC)
//    - 0x04: special script type pay-to-pubkey
//    - 0x96...52: x-coordinate of the pubkey
//
// Example 2:
// From tx in main blockchain:
// Blk 113931, 4a16969aa4764dd7507fc1de7f0baa4850a246de90c45e59a3207f9a26b5036f:2
//
//    8cf316800900b8025be1b3efc63b0ad48e7f9f10e87544528d58
//    <----><------------------------------------------>
//      |                             |
//   header code             compressed txout
//
//  - header code: 0x8cf316 (not coinbase, height 113931)
//  - compressed txout:
//    - 0x8009: VLQ-encoded compressed amount for 15000000 (0.15 BTC)
//    - 0x00: special script type pay-to-pubkey-hash
//    - 0xb8...58: pubkey hash
//
// Example 3:
// From tx in main blockchain:
// Blk 338156, 1b02d1c8cfef60a189017b9a420c682cf4a0028175f2f563209e4ff61c8c3620:22
//
//    a8a2588ba5b9e763011dd46a006572d820e448e12d2bbb38640bc718e6
//    <----><-------------------------------------------------->
//      |                             |
//   header code             compressed txout
//
//  - header code: 0xa8a258 (not coinbase, height 338156)
//  - compressed txout:
//    - 0x8ba5b9e763: VLQ-encoded compressed amount for 366875659 (3.66875659 BTC)
//    - 0x01: special script type pay-to-script-hash
//    - 0x1d...e6: script hash
// -----------------------------------------------------------------------------

// maxUint32VLQSerializeSize is the maximum number of bytes a max uint32 takes
// to serialize as a VLQ.
var maxUint32VLQSerializeSize = serializeSizeVLQ(1<<32 - 1)

// outpointKeyPool defines a concurrent safe free list of byte slices used to
// provide temporary buffers for outpoint database keys.
var outpointKeyPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, common.HashLength+maxUint32VLQSerializeSize)
		return &b // Pointer to slice to avoid boxing alloc.
	},
}

// outpointKey returns a key suitable for use as a database key in the utxo set
// while making use of a free list.  A new buffer is allocated if there are not
// already any available on the free list.  The returned byte slice should be
// returned to the free list by using the recycleOutpointKey function when the
// caller is done with it _unless_ the slice will need to live for longer than
// the caller can calculate such as when used to write to the database.
func outpointKey(outpoint protos.OutPoint) *[]byte {
	// A VLQ employs an MSB encoding, so they are useful not only to reduce
	// the amount of storage space, but also so iteration of utxos when
	// doing byte-wise comparisons will produce them in order.
	key := outpointKeyPool.Get().(*[]byte)
	idx := uint64(outpoint.Index)

	idxSize := serializeSizeVLQ(idx)
	if cap(*key) < common.HashLength+idxSize {
		key = outpointKeyPool.New().(*[]byte)
	}
	if cap(*key) < common.HashLength+idxSize {
		newone := make([]byte, common.HashLength+idxSize)
		key = &newone
	}
	*key = (*key)[:common.HashLength+serializeSizeVLQ(idx)]
	copy(*key, outpoint.Hash[:])

	putVLQ((*key)[common.HashLength:], idx)
	return key
}

// recycleOutpointKey puts the provided byte slice, which should have been
// obtained via the outpointKey function, back on the free list.
func recycleOutpointKey(key *[]byte) {
	outpointKeyPool.Put(key)
}

// utxoEntryHeaderCode returns the calculated header code to be used when
// serializing the provided utxo entry.
func utxoEntryHeaderCode(entry *txo.UtxoEntry) (uint64, error) {
	if entry.IsSpent() {
		return 0, common.AssertError("entry has already been spent")
	}

	// As described in the serialization format comments, the header code
	// encodes the height shifted over one bit and the coinbase flag in the
	// lowest bit.
	headerCode := uint64(entry.BlockHeight()) << 1
	if entry.IsCoinBase() {
		headerCode |= 0x01
	}

	return headerCode, nil
}

// serializeUtxoEntry returns the entry serialized to a format that is suitable
// for long-term storage.  The format is described in detail above.
func serializeUtxoEntry(entry *txo.UtxoEntry) ([]byte, error) {
	// Spent outputs have no serialization.
	if entry.IsSpent() {
		return nil, nil
	}

	// Encode the header code.
	headerCode, err := utxoEntryHeaderCode(entry)
	if err != nil {
		return nil, err
	}

	// Calculate the size needed to serialize the entry.
	size := serializeSizeVLQ(headerCode) +
		compressedTxOutSize(uint64(entry.Amount()), entry.PkScript(), entry.Asset())

	// Serialize the header code followed by the compressed unspent
	// transaction output.
	serialized := make([]byte, size)
	offset := putVLQ(serialized, headerCode)
	offset += putCompressedTxOut(serialized[offset:], uint64(entry.Amount()),
		entry.PkScript(), entry.Asset())

	return serialized, nil
}

// DeserializeUtxoEntry decodes a utxo entry from the passed serialized byte
// slice into a new UtxoEntry using a format that is suitable for long-term
// storage.  The format is described in detail above.
func DeserializeUtxoEntry(serialized []byte) (*txo.UtxoEntry, error) {
	// Deserialize the header code.
	code, offset := deserializeVLQ(serialized)
	if offset >= len(serialized) {
		return nil, common.DeserializeError("unexpected end of data after header")
	}

	// Decode the header code.
	//
	// Bit 0 indicates whether the containing transaction is a coinbase.
	// Bits 1-x encode height of containing transaction.
	isCoinBase := code&0x01 != 0
	blockHeight := int32(code >> 1)

	// Decode the compressed unspent transaction output.
	amount, pkScript, asset, _, err := decodeCompressedTxOut(serialized[offset:])
	if err != nil {
		return nil, common.DeserializeError(fmt.Sprintf("unable to decode "+
			"utxo: %v", err))
	}

	entry := txo.NewUtxoEntry(int64(amount), pkScript, blockHeight, isCoinBase, asset, nil)

	return entry, nil
}

// dbFetchUtxoEntry uses an existing database transaction to fetch the specified
// transaction output from the utxo set.
//
// When there is no entry for the provided output, nil will be returned for both
// the entry and the error.
func dbFetchUtxoEntry(dbTx database.Tx, outpoint protos.OutPoint) (*txo.UtxoEntry, error) {
	// Fetch the unspent transaction output information for the passed
	// transaction output.  Return now when there is no entry.
	key := outpointKey(outpoint)
	utxoBucket := dbTx.Metadata().Bucket(utxoSetBucketName)
	serializedUtxo := utxoBucket.Get(*key)
	recycleOutpointKey(key)
	if serializedUtxo == nil {
		return nil, nil
	}

	// A non-nil zero-length entry means there is an entry in the database
	// for a spent transaction output which should never be the case.
	if len(serializedUtxo) == 0 {
		return nil, common.AssertError(fmt.Sprintf("database contains entry "+
			"for spent tx output %v", outpoint))
	}

	// Deserialize the utxo entry and return it.
	entry, err := DeserializeUtxoEntry(serializedUtxo)
	if err != nil {
		// Ensure any deserialization errors are returned as database
		// corruption errors.
		if common.IsDeserializeErr(err) {
			return nil, database.Error{
				ErrorCode: database.ErrCorruption,
				Description: fmt.Sprintf("corrupt utxo entry "+
					"for %v: %v", outpoint, err),
			}
		}

		return nil, err
	}

	lockItem, err := dbFetchLockItem(dbTx, outpoint)
	if err != nil {
		return nil, err
	}
	entry.SetLockItem(lockItem)

	return entry, nil
}

// dbPutUtxoView uses an existing database transaction to update the utxo set
// in the database based on the provided utxo view contents and state.  In
// particular, only the entries that have been marked as modified are written
// to the database.
func dbPutUtxoView(dbTx database.Tx, view *txo.UtxoViewpoint) error {
	utxoBucket := dbTx.Metadata().Bucket(utxoSetBucketName)
	for outpoint, entry := range view.Entries() {
		// No need to update the database if the entry was not modified.
		if entry == nil || !entry.IsModified() {
			continue
		}

		// Remove the utxo entry if it is spent.
		if entry.IsSpent() {
			key := outpointKey(outpoint)
			err := utxoBucket.Delete(*key)
			recycleOutpointKey(key)
			if err != nil {
				return err
			}

			continue
		}

		// Serialize and store the utxo entry.
		serialized, err := serializeUtxoEntry(entry)
		if err != nil {
			return err
		}
		key := outpointKey(outpoint)
		err = utxoBucket.Put(*key, serialized)
		// NOTE: The key is intentionally not recycled here since the
		// database interface contract prohibits modifications.  It will
		// be garbage collected normally when the database is done with
		// it.
		if err != nil {
			return err
		}
	}

	return nil
}

// dbFetchLockItem uses an existing database transaction to fetch the specified
// transaction output from the utxo set.
//
// When there is no entry for the provided output, nil will be returned for both
// the entry and the error.
func dbFetchLockItem(dbTx database.Tx, outpoint protos.OutPoint) (*txo.LockItem, error) {
	key := outpointKey(outpoint)
	lockItem, err := dbFetchLockItemWithKey(dbTx, *key)
	recycleOutpointKey(key)
	return lockItem, err
}

// dbFetchLockItemWithKey uses an existing database transaction to fetch the specified
// transaction output from the utxo set.
//
// When there is no entry for the provided output, nil will be returned for both
// the entry and the error.
func dbFetchLockItemWithKey(dbTx database.Tx, key []byte) (*txo.LockItem, error) {
	// Fetch the unspent transaction output information for the passed
	// transaction output.  Return now when there is no entry.
	lockBucket := dbTx.Metadata().Bucket(lockSetBucketName)
	serializedLockItem := lockBucket.Get(key)
	if serializedLockItem == nil {
		return nil, nil
	}

	// A non-nil zero-length entry means there is an entry in the database
	if len(serializedLockItem) == 0 {
		return nil, nil
	}

	// Deserialize the utxo entry and return it.
	lockItem, err := txo.DeserializeLockItem(serializedLockItem)
	if err != nil {
		// Ensure any deserialization errors are returned as database
		// corruption errors.
		if common.IsDeserializeErr(err) {
			return nil, database.Error{
				ErrorCode: database.ErrCorruption,
				Description: fmt.Sprintf("corrupt utxo lockitem "+
					"for %v: %v", key, err),
			}
		}

		return nil, err
	}

	return lockItem, nil
}

// dbPutUtxoView uses an existing database transaction to update the utxo set
// in the database based on the provided utxo view contents and state.  In
// particular, only the entries that have been marked as modified are written
// to the database.
func dbPutLockItem(dbTx database.Tx, view *txo.UtxoViewpoint) error {
	lockBucket := dbTx.Metadata().Bucket(lockSetBucketName)

	for outpoint, utxoentry := range view.Entries() {
		if !utxoentry.IsSpent() && utxoentry.LockItem() != nil {
			key := outpointKey(outpoint)
			err := lockBucket.Put(*key, utxoentry.LockItem().Bytes())
			recycleOutpointKey(key)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// save the utxo set to database for every address in the viewpoint.
// put utxo type for unique pkScript
func dbPutBalance(dbTx database.Tx, view *txo.UtxoViewpoint) error {
	//log.Debug("dbPutBalance enter", len( view.entries ) )
	balanceBucket := dbTx.Metadata().Bucket(balanceBucketName)
	for outpoint, entry := range view.Entries() {
		// No need to update the database if the entry was not modified.
		if entry == nil || !entry.IsModified() {
			continue
		}

		// create balance bucket if not exists
		if balanceBucket == nil {
			bucket, err := dbTx.Metadata().CreateBucketIfNotExists(balanceBucketName)
			if err != nil {
				return err
			}

			balanceBucket = bucket
		}

		// get addrs from entry and check whether it valid
		scriptClass, addrs, _, err := txscript.ExtractPkScriptAddrs(entry.PkScript())
		if err != nil || len(addrs) <= 0 {
			log.Info("get addr error,", scriptClass, addrs, entry.PkScript())
			continue
		}

		address := addrs[0].ScriptAddress()

		// get bucket of specified address
		utxosBucket := balanceBucket.Bucket(address)
		if utxosBucket == nil {
			bucket, err := balanceBucket.CreateBucketIfNotExists(address)
			if err != nil {
				return err
			}
			utxosBucket = bucket
		}
		key := outpointKey(outpoint)

		// Remove the utxo entry if it is spent.
		if entry.IsSpent() {
			err := utxosBucket.Delete(*key)
			recycleOutpointKey(key)
			if err != nil {
				return err
			}
			continue
		}
		// Serialize and store the utxo entry.
		serialized, err := serializeUtxoEntry(entry)
		if err != nil {
			return err
		}

		err = utxosBucket.Put(*key, serialized)
		// NOTE: The key is intentionally not recycled here since the
		// database interface contract prohibits modifications.  It will
		// be garbage collected normally when the database is done with
		// it.
		if err != nil {
			return err
		}
	}
	return nil
}

//check if the signature already exists in the chain.
func dbHasSignature(dbTx database.Tx, sign *asiutil.BlockSign) bool {
	signatureBucket := dbTx.Metadata().Bucket(signatureSetBucketName)
	if signatureBucket == nil {
		return false
	}

	buff := signatureBucket.Get(sign.Hash()[:])
	if buff != nil {
		return true
	}

	return false
}

//save all signatures in the block to chain.
func dbPutSignViewPoint(dbTx database.Tx, signs []*asiutil.BlockSign) error {
	signatureBucket := dbTx.Metadata().Bucket(signatureSetBucketName)
	if signatureBucket == nil {
		bucket, err := dbTx.Metadata().CreateBucketIfNotExists(signatureSetBucketName)
		if err != nil {
			return err
		}

		signatureBucket = bucket
	}

	for _, msg := range signs {
		buf := bytes.NewBuffer(make([]byte, 0, msg.MsgSign.SerializeSize()))
		_ = msg.MsgSign.Serialize(buf)
		err := signatureBucket.Put(msg.Hash()[:], buf.Bytes())
		if err != nil {
			return err
		}
	}

	return nil
}

//remove the signatures when disconnect block.
func dbRemoveSignature(dbTx database.Tx, hash *common.Hash) error {
	signatureBucket := dbTx.Metadata().Bucket(signatureSetBucketName)
	if signatureBucket == nil {
		return database.Error{
			ErrorCode:   database.ErrBucketNotFound,
			Description: fmt.Sprintf("bucket not found for %s", signatureSetBucketName),
		}
	}

	return signatureBucket.Delete(hash[:])
}

//pair for sorting to keep all peers the same order.
type EntryPair struct {
	Key   protos.OutPoint
	Value *txo.UtxoEntry
}

type EntryPairList []EntryPair

func (p EntryPairList) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func (p EntryPairList) Len() int {
	return len(p)
}

func (p EntryPairList) Less(i, j int) bool {
	if p[i].Value.Amount() < p[j].Value.Amount() {
		return true

	} else if p[i].Value.Amount() == p[j].Value.Amount() {
		if p[i].Value.BlockHeight() < p[j].Value.BlockHeight() {
			return true

		} else if p[i].Value.BlockHeight() == p[j].Value.BlockHeight() {
			if p[i].Key.Index < p[j].Key.Index {
				return true

			} else if p[i].Key.Index == p[j].Key.Index {
				return bytes.Compare(p[i].Key.Hash[:], p[j].Key.Hash[:]) < 0
			}
		}
	}
	return false
}

// dbFetchBalance fetchs all utxo entries of specified address from balance bucket
func dbFetchBalance(dbTx database.Tx, address []byte) (*EntryPairList, error) {
	var mp EntryPairList
	balanceBucket := dbTx.Metadata().Bucket(balanceBucketName)
	if balanceBucket == nil {
		return &mp, nil
	}

	utxosBucket := balanceBucket.Bucket(address)
	if utxosBucket == nil {
		return &mp, nil
	}

	// deserialize utxo entry, then push to a map
	err := utxosBucket.ForEach(func(k, serializedUtxo []byte) error {
		recycleOutpointKey(&k)
		if serializedUtxo == nil {
			return nil
		}
		if len(serializedUtxo) == 0 {
			return common.AssertError(fmt.Sprintf("database contains entry "+
				"for spent tx output %v", k))
		}
		// Deserialize the utxo entry and return it.
		entry, err := DeserializeUtxoEntry(serializedUtxo)
		if err != nil {
			// Ensure any deserialization errors are returned as database
			// corruption errors.
			if common.IsDeserializeErr(err) {
				return database.Error{
					ErrorCode: database.ErrCorruption,
					Description: fmt.Sprintf("corrupt utxo entry "+
						"for %v: %v", k, err),
				}
			}
			return err
		}
		lockitem, err := dbFetchLockItemWithKey(dbTx, k)
		if err != nil {
			return err
		}
		entry.SetLockItem(lockitem)

		hash := common.Hash{}
		hash.SetBytes(k[:common.HashLength])
		idx, _ := deserializeVLQ(k[common.HashLength:])

		outpoint := protos.OutPoint{Hash: hash, Index: uint32(idx)}
		mp = append(mp, EntryPair{
			Key:   outpoint,
			Value: entry,
		})

		if len(entry.PkScript()) <= 1 {
			log.Debugf("dbFetchBalance entry pkScript is invalid, %v", entry.Asset())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &mp, nil
}

// -----------------------------------------------------------------------------
// The block index consists of two buckets with an entry for every block in the
// main chain.  One bucket is for the hash to height mapping and the other is
// for the height to hash mapping.
//
// The serialized format for values in the hash to height bucket is:
//   <height>
//
//   Field      Type     Size
//   height     uint32   4 bytes
//
// The serialized format for values in the height to hash bucket is:
//   <hash>
//
//   Field      Type             Size
//   hash       common.Hash   common.HashLength
// -----------------------------------------------------------------------------

// dbPutBlockIndex uses an existing database transaction to update or add the
// block index entries for the hash to height and height to hash mappings for
// the provided values.
func dbPutBlockIndex(dbTx database.Tx, hash *common.Hash, height int32) error {
	// Serialize the height for use in the index entries.
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))

	// Add the block hash to height mapping to the index.
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	if err := hashIndex.Put(hash[:], serializedHeight[:]); err != nil {
		return err
	}
	return nil
}

// dbRemoveBlockIndex uses an existing database transaction remove block index
// entries from the hash to height and height to hash mappings for the provided
// values.
func dbRemoveBlockIndex(dbTx database.Tx, hash *common.Hash, height int32) error {
	// Remove the block hash to height mapping.
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	if err := hashIndex.Delete(hash[:]); err != nil {
		return err
	}
	return nil
}

// dbFetchHeightByHash uses an existing database transaction to retrieve the
// height for the provided hash from the index.
func dbFetchHeightByHash(dbTx database.Tx, hash *common.Hash) (int32, error) {
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	serializedHeight := hashIndex.Get(hash[:])
	if serializedHeight == nil {
		str := fmt.Sprintf("block %s is not in the main chain", hash)
		return 0, errNotInMainChain(str)
	}

	return int32(byteOrder.Uint32(serializedHeight)), nil
}

// -----------------------------------------------------------------------------
// The best chain state consists of the best block hash and height, the total
// number of transactions up to and including those in the best block, and the
// accumulated work sum up to and including the best block.
//
// The serialized format is:
//
//   <block hash><block height><total txns><work sum length><work sum>
//
//   Field             Type             Size
//   block hash        common.Hash   common.HashLength
//   block height      uint32           4 bytes
//   total txns        uint64           8 bytes
// -----------------------------------------------------------------------------

// bestChainState represents the data to be stored the database for the current
// best chain state.
type bestChainState struct {
	hash      common.Hash
	height    uint32
	totalTxns uint64
}

// serializeBestChainState returns the serialization of the passed block best
// chain state.  This is data to be stored in the chain state bucket.
func serializeBestChainState(state bestChainState) []byte {
	// Calculate the full size needed to serialize the chain state.
	serializedLen := common.HashLength + 4 + 8

	// Serialize the chain state.
	serializedData := make([]byte, serializedLen)
	copy(serializedData[0:common.HashLength], state.hash[:])
	offset := uint32(common.HashLength)
	byteOrder.PutUint32(serializedData[offset:], state.height)
	offset += 4
	byteOrder.PutUint64(serializedData[offset:], state.totalTxns)
	return serializedData[:]
}

// deserializeBestChainState deserializes the passed serialized best chain
// state.  This is data stored in the chain state bucket and is updated after
// every block is connected or disconnected form the main chain.
// block.
func deserializeBestChainState(serializedData []byte) (bestChainState, error) {
	// Ensure the serialized data has enough bytes to properly deserialize
	// the hash, height, total transactions, and work sum length.
	if len(serializedData) < common.HashLength+12 {
		return bestChainState{}, database.Error{
			ErrorCode:   database.ErrCorruption,
			Description: "corrupt best chain state",
		}
	}

	state := bestChainState{}
	copy(state.hash[:], serializedData[0:common.HashLength])
	offset := uint32(common.HashLength)
	state.height = byteOrder.Uint32(serializedData[offset : offset+4])
	offset += 4
	state.totalTxns = byteOrder.Uint64(serializedData[offset : offset+8])
	return state, nil
}

// dbPutBestState uses an existing database transaction to update the best chain
// state with the given parameters.
func dbPutBestState(dbTx database.Tx, snapshot *BestState) error {
	// Serialize the current best chain state.
	serializedData := serializeBestChainState(bestChainState{
		hash:      snapshot.Hash,
		height:    uint32(snapshot.Height),
		totalTxns: snapshot.TotalTxns,
	})

	// Store the current best chain state into the database.
	return dbTx.Metadata().Put(chainStateKeyName, serializedData)
}

// createChainState initializes both the database and the chain state to the
// genesis block.  This includes creating the necessary buckets and inserting
// the genesis block, so it must only be called on an uninitialized database.
func (b *BlockChain) createChainState(chainStartTime int64) error {
	// Create a new node from the genesis block and set it as the best node.
	genesisBlock := asiutil.NewBlock(b.chainParams.GenesisBlock)
	header := &genesisBlock.MsgBlock().Header
	genesisRound := &ainterface.Round{
		Round:          0,
		Duration:       common.DefaultBlockInterval * 1,
		RoundStartUnix: chainStartTime,
	}
	node := newBlockNode(genesisRound, header, nil)
	node.status = statusDataStored | statusValid | statusFirstInRound
	b.bestChain.SetTip(node)

	// Add the new node to the index which is used for faster lookups.
	b.index.addNode(node)

	// Init State
	statedb, err := state.New(common.Hash{}, b.stateCache)
	if err != nil {
		return err
	}
	coinbaseTx, err := genesisBlock.Tx(0)
	if err != nil {
		return err
	}

	gennesisHash := header.BlockHash()
	b.chainParams.GenesisHash = &gennesisHash

	beneficiary := coinbaseTx.MsgTx().TxOut[0]

	//init system contract.
	context := fvm.NewFVMContext(chaincfg.OfficialAddress, new(big.Int).SetInt64(1), genesisBlock, b, nil, nil)
	vmenv := vm.NewFVM(context, statedb, chaincfg.ActiveNetParams.FvmParam, *b.GetVmConfig())

	sender := vm.AccountRef(chaincfg.OfficialAddress)
	cMap := chaincfg.TransferGenesisData(beneficiary.Data)
	for k, v := range cMap {
		byteCode := common.Hex2Bytes(v[0].Code)
		_, addr, _, _, err := vmenv.Create(sender, byteCode, uint64(4604216000), common.Big0, &beneficiary.Asset, byteCode, nil, true)
		if err != nil {
			log.Errorf("Deploy genesis contract failed when create %s", k)
			panic(err)
		}
		log.Info("init genesis system contract ", k, "contract address = ", addr.Hex())

		if v[0].InitCode != "" {
			proxyAddr := common.HexToAddress(k)
			_, _, _, err = vmenv.Call(sender, proxyAddr, common.Hex2Bytes(v[0].InitCode), uint64(4604216000), common.Big0, &beneficiary.Asset, true)
			if err != nil {
				log.Error("Deploy genesis contract failed when init code")
				panic(err)
			}
		}
	}

	stateRoot, err := statedb.Commit(false)
	if err != nil {
		log.Info("Commit error", err)
	}
	err = statedb.Database().TrieDB().Commit(stateRoot, true)
	if err != nil {
		log.Info("Commit error", err)
	}

	// Initialize the state related to the best block.  Since it is the
	// genesis block, use its timestamp for the median time.
	numTxns := uint64(len(genesisBlock.MsgBlock().Transactions))
	blockSize := uint64(genesisBlock.MsgBlock().SerializeSize())
	b.stateSnapshot = newBestState(node, blockSize, numTxns, numTxns, node.GetTime())

	view := txo.NewUtxoViewpoint()
	for _, tx := range genesisBlock.Transactions()[:] {
		view.AddTxOuts(tx.Hash(), tx.MsgTx(), false, genesisBlock.Height())
	}

	// Create the initial the database chain state including creating the
	// necessary index buckets and inserting the genesis block.
	err = b.db.Update(func(dbTx database.Tx) error {
		meta := dbTx.Metadata()
		// Create the bucket that houses the block index data.
		_, err := meta.CreateBucket(blockIndexBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the round index data.
		_, err = meta.CreateBucket(roundIndexBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the chain block hash to height
		// index.
		_, err = meta.CreateBucket(hashIndexBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the spend journal data and
		// store its version.
		_, err = meta.CreateBucket(spendJournalBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the utxo set and store its
		// version.  Note that the genesis block coinbase transaction is
		// intentionally not inserted here since it is not spendable by
		// consensus rules.
		_, err = meta.CreateBucket(utxoSetBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that house the created asset
		assetsBucket, err := meta.CreateBucket(assetsSetBucketName)
		if err != nil {
			return err
		}
		mainAsset := asiutil.AsimovAsset.FixedBytes()
		if err = assetsBucket.Put(mainAsset[:], []byte{1}); err != nil {
			return err
		}

		// Create the bucket that house the lock set
		_, err = meta.CreateBucket(lockSetBucketName)
		if err != nil {
			return err
		}

		// Save the genesis block to the block index database.
		err = dbStoreBlockNode(dbTx, node)
		if err != nil {
			return err
		}

		// Add the genesis block hash to height and height to hash
		// mappings to the index.
		err = dbPutBlockIndex(dbTx, &node.hash, node.height)
		if err != nil {
			return err
		}

		// Store the current best chain state into the database.
		err = dbPutBestState(dbTx, b.stateSnapshot)
		if err != nil {
			return err
		}

		err = dbPutBalance(dbTx, view)
		if err != nil {
			return err
		}

		err = dbPutUtxoView(dbTx, view)
		if err != nil {
			return err
		}

		// Store the genesis block into the database.
		return dbStoreBlock(dbTx, genesisBlock)
	})
	return err
}

// initChainState attempts to load and initialize the chain state from the
// database.  When the db does not yet contain any chain state, both it and the
// chain state are initialized to the genesis block.
func (b *BlockChain) initChainState(chainStartTime int64) error {
	// Determine the state of the chain database. We may need to initialize
	// everything from scratch or upgrade certain buckets.
	var initialized bool
	err := b.db.View(func(dbTx database.Tx) error {
		initialized = dbTx.Metadata().Get(chainStateKeyName) != nil
		return nil
	})
	if err != nil {
		return err
	}

	if !initialized {
		// At this point the database has not already been initialized, so
		// initialize both it and the chain state to the genesis block.
		return b.createChainState(chainStartTime)
	}

	// Attempt to load the chain state from the database.
	err = b.db.View(func(dbTx database.Tx) error {
		// Fetch the stored chain state from the database metadata.
		// When it doesn't exist, it means the database hasn't been
		// initialized for use with chain yet, so break out now to allow
		// that to happen under a writable database transaction.
		serializedData := dbTx.Metadata().Get(chainStateKeyName)
		log.Tracef("Serialized chain state: %x", serializedData)
		state, err := deserializeBestChainState(serializedData)
		if err != nil {
			return err
		}

		// Load all of the headers from the data for the known best
		// chain and construct the block index accordingly.  Since the
		// number of nodes are already known, perform a single alloc
		// for them versus a whole bunch of little ones to reduce
		// pressure on the GC.
		log.Infof("Loading block index...")

		blockIndexBucket := dbTx.Metadata().Bucket(blockIndexBucketName)

		// Determine how many blocks will be loaded into the index so we can
		// allocate the right amount.
		var blockCount int32
		cursor := blockIndexBucket.Cursor()
		for ok := cursor.First(); ok; ok = cursor.Next() {
			blockCount++
		}
		blockNodes := make([]blockNode, blockCount)

		var i int32
		var lastNode *blockNode
		var lastRound *ainterface.Round
		cursor = blockIndexBucket.Cursor()
		for ok := cursor.First(); ok; ok = cursor.Next() {
			header, round, status, err := deserializeBlockRow(cursor.Value())
			if err != nil {
				return err
			}

			// Determine the parent block node. Since we iterate block headers
			// in order of height, if the blocks are mostly linear there is a
			// very good chance the previous header processed is the parent.
			var parent *blockNode
			if lastNode == nil {
				blockHash := header.BlockHash()
				if !blockHash.IsEqual(b.chainParams.GenesisHash) {
					return common.AssertError(fmt.Sprintf("initChainState: Expected "+
						"first entry in block index to be genesis block, "+
						"found %s", blockHash))
				}
			} else if header.PrevBlock == lastNode.hash {
				// Since we iterate block headers in order of height, if the
				// blocks are mostly linear there is a very good chance the
				// previous header processed is the parent.
				parent = lastNode
			} else {
				parent = b.index.LookupNode(&header.PrevBlock)
				if parent == nil {
					return common.AssertError(fmt.Sprintf("initChainState: Could "+
						"not find parent for block %s", header.BlockHash()))
				}
			}

			// Initialize the block node for the block, connect it,
			// and add it to the block index.
			node := &blockNodes[i]
			if round != nil {
				initBlockNode(node, round, header, parent)
				lastRound = round
			} else {
				initBlockNode(node, lastRound, header, parent)
			}
			node.status = status
			b.index.addNode(node)

			lastNode = node
			i++
		}

		// Set the best chain view to the stored best state.
		tip := b.index.LookupNode(&state.hash)
		if tip == nil {
			return common.AssertError(fmt.Sprintf("initChainState: cannot find "+
				"chain tip %s in block index", state.hash))
		}
		b.bestChain.SetTip(tip)

		blockKey := database.NewBlockKey(&state.hash, database.BlockNormal)
		// Load the raw block bytes for the best block.
		blockBytes, err := dbTx.FetchBlock(blockKey)
		if err != nil {
			return err
		}
		var block protos.MsgBlock
		err = block.Deserialize(bytes.NewReader(blockBytes))
		if err != nil {
			return err
		}

		// As a final consistency check, we'll run through all the
		// nodes which are ancestors of the current chain tip, and mark
		// them as valid if they aren't already marked as such.  This
		// is a safe assumption as all the block before the current tip
		// are valid by definition.
		for iterNode := tip; iterNode != nil; iterNode = iterNode.parent {
			// If this isn't already marked as valid in the index, then
			// we'll mark it as valid now to ensure consistency once
			// we're up and running.
			if !iterNode.status.KnownValid() {
				log.Infof("Block %v (height=%v) ancestor of "+
					"chain tip not marked as valid, "+
					"upgrading to valid for consistency",
					iterNode.hash, iterNode.height)
				b.index.SetStatusFlags(iterNode, statusValid)
			}
		}

		// Initialize the state related to the best block.
		blockSize := uint64(len(blockBytes))
		numTxns := uint64(len(block.Transactions))
		b.stateSnapshot = newBestState(tip, blockSize,
			numTxns, state.totalTxns, tip.GetTime())

		return nil
	})

	if err != nil {
		return err
	}
	// As we might have updated the index after it was loaded, we'll
	// attempt to flush the index to the DB. This will only result in a
	// write if the elements are dirty, so it'll usually be a noop.
	return b.index.flushToDB()
}

// deserializeBlockRow parses a value in the block index bucket into a block
// header and block status bitfield.
func deserializeBlockRow(blockRow []byte) (*protos.BlockHeader, *ainterface.Round, blockStatus, error) {
	buffer := bytes.NewReader(blockRow)

	var header protos.BlockHeader
	err := header.Deserialize(buffer)
	if err != nil {
		return nil, nil, statusNone, err
	}

	statusByte, err := buffer.ReadByte()
	if err != nil {
		return nil, nil, statusNone, err
	}

	if blockStatus(statusByte)&statusFirstInRound == statusFirstInRound {
		var round ainterface.Round
		err = round.Deserialize(buffer)
		if err != nil {
			return nil, nil, statusNone, err
		}
		round.Round = header.Round

		return &header, &round, blockStatus(statusByte), nil
	}
	return &header, nil, blockStatus(statusByte), nil
}

// dbFetchHeaderByHash uses an existing database transaction to retrieve the
// block header for the provided hash.
func dbFetchHeaderByHash(dbTx database.Tx, hash *common.Hash) (*protos.BlockHeader, error) {
	headerBytes, err := dbTx.FetchBlockHeader(database.NewNormalBlockKey(hash))
	if err != nil {
		return nil, err
	}

	var header protos.BlockHeader
	err = header.Deserialize(bytes.NewReader(headerBytes))
	if err != nil {
		return nil, err
	}

	return &header, nil
}

// dbFetchBlockByNode uses an existing database transaction to retrieve the
// raw block for the provided node, deserialize it, and return a vvsutil.Block
// with the height set.
func dbFetchBlockByNode(dbTx database.Tx, node *blockNode) (*asiutil.Block, error) {
	// Load the raw block bytes from the database.
	blockBytes, err := dbTx.FetchBlock(database.NewNormalBlockKey(&node.hash))
	if err != nil {
		return nil, err
	}

	// Create the encapsulated block and set the height appropriately.
	block, err := asiutil.NewBlockFromBytes(blockBytes)
	if err != nil {
		return nil, err
	}

	return block, nil
}

// dbStoreBlockNode stores the block header and validation status to the block
// index bucket. This overwrites the current entry if there exists one.
func dbStoreBlockNode(dbTx database.Tx, node *blockNode) error {
	// Serialize block data to be stored.
	size := blockHdrSize + 1
	if node.status&statusFirstInRound == statusFirstInRound {
		size += roundHdrDeltaSize
	}
	w := bytes.NewBuffer(make([]byte, 0, size))
	header := node.Header()
	err := header.Serialize(w)
	if err != nil {
		return err
	}

	err = w.WriteByte(byte(node.status))
	if err != nil {
		return err
	}
	if node.status&statusFirstInRound == statusFirstInRound {
		err = node.round.Serialize(w)
		if err != nil {
			return err
		}
	}
	value := w.Bytes()

	// Write block header data to block index bucket.
	blockIndexBucket := dbTx.Metadata().Bucket(blockIndexBucketName)
	key := blockIndexKey(&node.hash, uint32(node.height))

	return blockIndexBucket.Put(key, value)
}

// dbStoreBlock stores the provided block in the database if it is not already
// there. The full block data is written to ffldb.
func dbStoreBlock(dbTx database.Tx, block *asiutil.Block) error {
	blockKey := database.NewNormalBlockKey(block.Hash())
	hasBlock, err := dbTx.HasBlock(blockKey)
	if err != nil {
		return err
	}
	if hasBlock {
		return nil
	}

	blockBytes, err := block.Bytes()
	if err != nil {
		str := fmt.Sprintf("failed to get serialized bytes for block %s",
			block.Hash())
		return ruleError(ErrFailedSerializedBlock, str)
	}
	log.Infof("save block, height=%d, hash=%s", block.Height(), block.Hash())
	return dbTx.StoreBlock(blockKey, blockBytes)
}

// dbStoreVBlock stores the provided block in the database if it is not already
// there. The full block data is written to ffldb.
func dbStoreVBlock(dbTx database.Tx, block *asiutil.VBlock) error {
	blockKey := database.NewVirtualBlockKey(block.Hash())
	hasBlock, err := dbTx.HasBlock(blockKey)
	if err != nil {
		return err
	}
	if hasBlock {
		return nil
	}

	blockBytes, err := block.Bytes()
	if err != nil {
		str := fmt.Sprintf("failed to get serialized bytes for vblock %s",
			block.Hash())
		return ruleError(ErrFailedSerializedBlock, str)
	}
	log.Debug("save vblock", block.Hash())
	return dbTx.StoreBlock(blockKey, blockBytes)
}

// blockIndexKey generates the binary key for an entry in the block index
// bucket. The key is composed of the block height encoded as a big-endian
// 32-bit unsigned int followed by the 32 byte block hash.
func blockIndexKey(blockHash *common.Hash, blockHeight uint32) []byte {
	indexKey := make([]byte, common.HashLength+4)
	binary.BigEndian.PutUint32(indexKey[0:4], blockHeight)
	copy(indexKey[4:common.HashLength+4], blockHash[:])
	return indexKey
}

// BlockByHeight returns the block at the given height in the main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockByHeight(blockHeight int32) (*asiutil.Block, error) {
	// Lookup the block height in the best chain.
	node := b.bestChain.NodeByHeight(blockHeight)
	if node == nil {
		str := fmt.Sprintf("no block node at height %d exists in the main chain", blockHeight)
		return nil, errNotInMainChain(str)
	}

	// Load the block from the database and return it.
	var block *asiutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		block, err = dbFetchBlockByNode(dbTx, node)
		return err
	})
	return block, err
}

// BlockByHash returns the block from the main chain with the given hash with
// the appropriate chain height set.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockByHash(hash *common.Hash) (*asiutil.Block, error) {
	// Lookup the block hash in block index and ensure it is in the best
	// chain.
	node := b.index.LookupNode(hash)
	if node == nil || !b.bestChain.Contains(node) {
		str := fmt.Sprintf("block %s is not in the main chain", hash)
		return nil, errNotInMainChain(str)
	}

	// Load the block from the database and return it.
	var block *asiutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		block, err = dbFetchBlockByNode(dbTx, node)
		return err
	})
	return block, err
}
