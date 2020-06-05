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
	"github.com/AsimovNetwork/asimov/blockchain/syscontract"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/vm/fvm"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/state"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/vm"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
	"math"
	"math/big"
	"sort"
	"time"
)

const (
	// MinCoinbaseScriptLen is the minimum length a coinbase script can be.
	MinCoinbaseScriptLen = 2

	// MaxCoinbaseScriptLen is the maximum length a coinbase script can be.
	MaxCoinbaseScriptLen = 100

	// medianTimeBlocks is the number of previous blocks which should be
	// used to calculate the median time used to validate block timestamps.
	medianTimeBlocks = 11

	// baseSubsidy is the starting subsidy amount for mined blocks.  This
	// value is halved every SubsidyHalvingInterval blocks.
	baseSubsidy = 200 * common.XingPerAsimov

	// MaxBlockSigOpsCost is the maximum number of signature operations
	// allowed for a block.
	MaxBlockSigOpsCost = 200000

	// NormalGas is the number of gas used in normal utxo per byte.
	// Estimate a normal tx takes 1k bytes, and refer an ethereum's notmal tx costs 21000 gas.
	NormalGas = 21
)

var (
	// zeroHash is the zero value for a common.Hash and is defined as
	// a package level variable to avoid the need to create a new instance
	// every time a check is needed.
	zeroHash common.Hash
)

var CoinBasePreOut = protos.NewOutPoint(&zeroHash, math.MaxUint32)

// define OutPoint for transfer creation and mint
var TransferCreationOut = protos.NewOutPoint(&zeroHash, asiutil.TransferCreationIdx)
var TransferMintOut = protos.NewOutPoint(&zeroHash, asiutil.TransferMintIdx)

// isNullOutpoint determines whether or not a previous transaction output point
// is set.
func isNullOutpoint(outpoint *protos.OutPoint) bool {
	if outpoint.Index == math.MaxUint32 && outpoint.Hash == zeroHash {
		return true
	}
	return false
}

// IsCoinBaseTx determines whether or not a transaction is a coinbase.  A coinbase
// is a special transaction created by miners that has no inputs.  This is
// represented in the block chain by a transaction with a single input that has
// a previous output transaction index set to the maximum value along with a
// zero hash.
//
// This function only differs from IsCoinBase in that it works with a raw protos
// transaction as opposed to a higher level util transaction.
func IsCoinBaseTx(msgTx *protos.MsgTx) bool {
	// A coin base must only have one transaction input.
	if len(msgTx.TxIn) != 1 {
		return false
	}

	// The previous output of a coin base must have a max value index and
	// a zero hash.
	prevOut := &msgTx.TxIn[0].PreviousOutPoint
	if prevOut.Index != math.MaxUint32 || prevOut.Hash != zeroHash {
		return false
	}

	return true
}

func IsTransferCreateOrMintTx(msgTx *protos.MsgTx) bool {
	if len(msgTx.TxIn) != 1 {
		return false
	}

	prevOut := &msgTx.TxIn[0].PreviousOutPoint

	if prevOut == nil {
		return false
	}

	if (prevOut.Index != asiutil.TransferCreationIdx && prevOut.Index != asiutil.TransferMintIdx) || prevOut.Hash != zeroHash {
		return false
	}

	return true
}

// IsCoinBase determines whether or not a transaction is a coinbase.  A coinbase
// is a special transaction created by miners that has no inputs.  This is
// represented in the block chain by a transaction with a single input that has
// a previous output transaction index set to the maximum value along with a
// zero hash.
//
// This function only differs from IsCoinBaseTx in that it works with a higher
// level util transaction as opposed to a raw protos transaction.
func IsCoinBase(tx *asiutil.Tx) bool {
	return IsCoinBaseTx(tx.MsgTx())
}

// SequenceLockActive determines if a transaction's sequence locks have been
// met, meaning that all the inputs of a given transaction have reached a
// height or time sufficient for their relative lock-time maturity.
func SequenceLockActive(sequenceLock *SequenceLock, blockHeight int32,
	timePast int64) bool {

	// If either the seconds, or height relative-lock time has not yet
	// reached, then the transaction is not yet mature according to its
	// sequence locks.
	if sequenceLock.Seconds >= timePast ||
		sequenceLock.BlockHeight >= blockHeight {
		return false
	}

	return true
}

// IsFinalizedTransaction determines whether or not a transaction is finalized.
func IsFinalizedTransaction(tx *asiutil.Tx, blockHeight int32, blockTime int64) bool {
	msgTx := tx.MsgTx()

	// Lock time of zero means the transaction is finalized.
	lockTime := msgTx.LockTime
	if lockTime == 0 {
		return true
	}

	// The lock time field of a transaction is either a block height at
	// which the transaction is finalized or a timestamp depending on if the
	// value is before the common.LockTimeThreshold.  When it is under the
	// threshold it is a block height.
	blockTimeOrHeight := int64(0)
	if lockTime < common.LockTimeThreshold {
		blockTimeOrHeight = int64(blockHeight)
	} else {
		blockTimeOrHeight = blockTime
	}
	if int64(lockTime) < blockTimeOrHeight {
		return true
	}

	// At this point, the transaction's lock time hasn't occurred yet, but
	// the transaction might still be finalized if the sequence number
	// for all transaction inputs is maxed out.
	for _, txIn := range msgTx.TxIn {
		if txIn.Sequence != math.MaxUint32 {
			return false
		}
	}
	return true
}

// CalcBlockSubsidy returns the subsidy amount a block at the provided height
// should have. This is mainly used for determining how much the coinbase for
// newly generated blocks awards as well as validating the coinbase for blocks
// has the expected value.
//
// The subsidy is halved every SubsidyReductionInterval blocks.  Mathematically
// this is: baseSubsidy / 2^(height/SubsidyReductionInterval)
//
// At the target block generation rate for the main network, this is
// approximately every 4 years.
func CalcBlockSubsidy(height int32, chainParams *chaincfg.Params) int64 {
	if chainParams.SubsidyReductionInterval == 0 {
		return baseSubsidy
	}

	// Equivalent to: baseSubsidy / 2^(height/subsidyHalvingInterval)
	return baseSubsidy >> uint(height/chainParams.SubsidyReductionInterval)
}

// CheckTransactionSanity performs some preliminary checks on a transaction to
// ensure it is sane.  These checks are context free.
func CheckTransactionSanity(tx *asiutil.Tx) error {
	// A transaction must have at least one input.
	msgTx := tx.MsgTx()
	if len(msgTx.TxIn) == 0 {
		return ruleError(ErrNoTxInputs, "CheckTransactionSanity: transaction has no inputs")
	}

	// A transaction must not exceed the maximum allowed block payload when
	// serialized.
	serializedTxSize := tx.MsgTx().SerializeSize()
	if serializedTxSize > common.MaxBlockSize {
		str := fmt.Sprintf("CheckTransactionSanity: serialized transaction is too big - got "+
			"%d, max %d", serializedTxSize, common.MaxBlockSize)
		return ruleError(ErrTxTooBig, str)
	}

	// Ensure the transaction amounts are in range.  Each transaction
	// output must not be negative or more than the max allowed per
	// transaction.  Also, the total of all outputs must abide by the same
	// restrictions.  All amounts in a transaction are in a unit value known
	// as a xing.  One bitcoin is a quantity of xing as defined by the
	// XingPerAsimov common.
	totalInCoin := make(map[protos.Asset]int64)
	for _, txOut := range msgTx.TxOut {
		// Only contract invoke accept value 0.
		if txOut.Value == 0 && !txOut.Asset.IsIndivisible() && len(txOut.Data) > 0 {
			continue
		}
		xing := txOut.Value
		if xing <= 0 {
			str := fmt.Sprintf("CheckTransactionSanity: transaction output has non-positive "+
				"value of %v", xing)
			return ruleError(ErrBadTxOutValue, str)
		}
		// Two's complement int64 overflow guarantees that any overflow
		// is detected and reported.  This is impossible for Bitcoin, but
		// perhaps possible if an alt increases the total money supply.
		if !txOut.Asset.IsIndivisible() {
			if xing > common.MaxXing {
				str := fmt.Sprintf("CheckTransactionSanity: transaction output value of %v is "+
					"higher than max allowed value of %v", xing,
					common.MaxXing)
				return ruleError(ErrBadTxOutValue, str)
			}
			if _, ok := totalInCoin[txOut.Asset]; ok {
				totalInCoin[txOut.Asset] += xing
			} else {
				totalInCoin[txOut.Asset] = xing
			}

			if totalInCoin[txOut.Asset] <= 0 {
				str := fmt.Sprintf("CheckTransactionSanity: total value of all transaction "+
					"outputs exceeds max allowed value of %v",
					common.MaxXing)
				return ruleError(ErrBadTxOutValue, str)
			}
			if totalInCoin[txOut.Asset] > common.MaxXing {
				str := fmt.Sprintf("CheckTransactionSanity: total value of all transaction "+
					"outputs is %v which is higher than max "+
					"allowed value of %v", totalInCoin[txOut.Asset],
					common.MaxXing)
				return ruleError(ErrBadTxOutValue, str)
			}
		}
	}

	// Check for duplicate transaction inputs.
	existingTxOut := make(map[protos.OutPoint]struct{})
	for _, txIn := range msgTx.TxIn {
		if _, exists := existingTxOut[txIn.PreviousOutPoint]; exists {
			return ruleError(ErrDuplicateTxInputs, "CheckTransactionSanity: transaction "+
				"contains duplicate inputs")
		}
		existingTxOut[txIn.PreviousOutPoint] = struct{}{}
	}

	// Coinbase script length must be between min and max length.
	if IsCoinBase(tx) {
		slen := len(msgTx.TxIn[0].SignatureScript)
		if slen < MinCoinbaseScriptLen || slen > MaxCoinbaseScriptLen {
			str := fmt.Sprintf("CheckTransactionSanity: coinbase transaction script length "+
				"of %d is out of range (min: %d, max: %d)",
				slen, MinCoinbaseScriptLen, MaxCoinbaseScriptLen)
			return ruleError(ErrBadCoinbaseScriptLen, str)
		}
	} else {
		if msgTx.TxContract.GasLimit < uint32(tx.MsgTx().SerializeSize())*NormalGas {
			errstr := fmt.Sprintf("CheckTransactionSanity: tx with gaslimit less than txsize * NormalGas = %d",
				uint32(tx.MsgTx().SerializeSize())*NormalGas)
			return ruleError(ErrBadGasLimit, errstr)
		}
		// Previous transaction outputs referenced by the inputs to this
		// transaction must not be null.
		for _, txIn := range msgTx.TxIn {
			if isNullOutpoint(&txIn.PreviousOutPoint) {
				return ruleError(ErrBadTxInput, "CheckTransactionSanity: transaction "+
					"input refers to previous output that "+
					"is null")
			}
		}
	}

	return nil
}

// CountSigOps returns the number of signature operations for all transaction
// input and output scripts in the provided transaction.  This uses the
// quicker, but imprecise, signature operation counting mechanism from
// txscript.
func CountSigOps(tx *asiutil.Tx) int {
	msgTx := tx.MsgTx()

	// Accumulate the number of signature operations in all transaction
	// inputs.
	totalSigOps := 0
	for _, txIn := range msgTx.TxIn {
		numSigOps := txscript.GetSigOpCount(txIn.SignatureScript)
		totalSigOps += numSigOps
	}

	// Accumulate the number of signature operations in all transaction
	// outputs.
	for _, txOut := range msgTx.TxOut {
		numSigOps := txscript.GetSigOpCount(txOut.PkScript)
		totalSigOps += numSigOps
	}

	return totalSigOps
}

// CountP2SHSigOps returns the number of signature operations for all input
// transactions which are of the pay-to-script-hash type.  This uses the
// precise, signature operation counting mechanism from the script engine which
// requires access to the input transaction scripts.
func CountP2SHSigOps(tx *asiutil.Tx, isCoinBaseTx bool, utxoView *txo.UtxoViewpoint) (int, error) {
	// Coinbase transactions have no interesting inputs.
	if isCoinBaseTx {
		return 0, nil
	}

	// Accumulate the number of signature operations in all transaction
	// inputs.
	msgTx := tx.MsgTx()
	totalSigOps := 0
	for txInIndex, txIn := range msgTx.TxIn {
		// Ensure the referenced input transaction is available.
		utxo := utxoView.LookupEntry(txIn.PreviousOutPoint)
		if utxo == nil || utxo.IsSpent() {
			str := fmt.Sprintf("CountP2SHSigOps: output %v referenced from "+
				"transaction %s:%d either does not exist or "+
				"has already been spent", txIn.PreviousOutPoint,
				tx.Hash(), txInIndex)
			return 0, ruleError(ErrMissingTxOut, str)
		}

		// We're only interested in pay-to-script-hash types, so skip
		// this input if it's not one.
		pkScript := utxo.PkScript()
		if !txscript.IsPayToScriptHash(pkScript) {
			continue
		}

		// Count the precise number of signature operations in the
		// referenced public key script.
		sigScript := txIn.SignatureScript
		numSigOps := txscript.GetPreciseSigOpCount(sigScript, pkScript)

		// We could potentially overflow the accumulator so check for
		// overflow.
		lastSigOps := totalSigOps
		totalSigOps += numSigOps
		if totalSigOps < lastSigOps {
			str := fmt.Sprintf("the public key script from output "+
				"%v contains too many signature operations - "+
				"overflow", txIn.PreviousOutPoint)
			return 0, ruleError(ErrTooManySigOps, str)
		}
	}

	return totalSigOps, nil
}

// checkBlockHeaderSanity performs some preliminary checks on a block header to
// ensure it is sane before continuing with processing.  These checks are
// context free.
//
// The flags do not modify the behavior of this function directly, however they
// are needed to pass along to checkProofOfWork.
func checkBlockHeaderSanity(header *protos.BlockHeader, parent *blockNode) error {
	// Ensure the block time is not too far in the future.
	if header.Timestamp-time.Now().Unix() > int64(chaincfg.Cfg.MaxTimeOffset) {
		str := fmt.Sprintf("block timestamp of %v is too far in the "+
			"future, max validtime offset is %d", header.Timestamp, chaincfg.Cfg.MaxTimeOffset)
		return ruleError(ErrTimeTooNew, str)
	}
	if parent == nil {
		return nil
	}
	if parent.round.Round > header.Round ||
		parent.round.Round == header.Round && parent.slot >= header.SlotIndex {
		str := fmt.Sprintf("block has old slot/round than parent: slot:%d/%d, round:%d/%d",
			parent.slot, header.SlotIndex, parent.round.Round, header.Round)
		return ruleError(ErrBadSlotOrRound, str)
	}
	// compare two neighbor nodes time stamp.
	delta := time.Now().Unix() - parent.timestamp
	if parent.round.Round == 0 {
		delta = time.Now().Unix() - chaincfg.ActiveNetParams.ChainStartTime
	}
	maxslot := (delta + int64(chaincfg.Cfg.MaxTimeOffset)*2) / common.MinBlockInterval
	slotcount := int64(header.Round-parent.round.Round)*int64(chaincfg.ActiveNetParams.RoundSize) +
		int64(header.SlotIndex)
	if parent.round.Round > 0 {
		slotcount -= int64(parent.slot)
	} else {
		slotcount -= int64(chaincfg.ActiveNetParams.RoundSize - 1)
	}
	if maxslot < slotcount {
		str := fmt.Sprintf("block has too new slot/round: slot:%d, round:%d, delta %d",
			header.SlotIndex, header.Round, delta)
		return ruleError(ErrBadSlotOrRound, str)
	}

	return nil
}

// checkBlockSanity performs some preliminary checks on a block to ensure it is
// sane before continuing with block processing.  These checks are context free.
func checkBlockSanity(block *asiutil.Block, parent *blockNode, flags common.BehaviorFlags) error {
	msgBlock := block.MsgBlock()
	header := &msgBlock.Header
	err := checkBlockHeaderSanity(header, parent)
	if err != nil {
		return err
	}

	// A block must have at least one transaction.
	numTx := len(msgBlock.Transactions)
	if numTx == 0 {
		return ruleError(ErrNoTransactions, "block does not contain "+
			"any transactions")
	}

	// A block must not have more transactions than the max block payload or
	// else it is certainly over the size limit.
	if numTx > common.MaxBlockSize {
		str := fmt.Sprintf("block contains too many transactions - "+
			"got %d, max %d", numTx, common.MaxBlockSize)
		return ruleError(ErrBlockTooBig, str)
	}

	// A block must not exceed the maximum allowed block payload when
	// serialized.
	serializedSize := msgBlock.SerializeSize()
	if serializedSize > common.MaxBlockSize {
		str := fmt.Sprintf("serialized block is too big - got %d, "+
			"max %d", serializedSize, common.MaxBlockSize)
		return ruleError(ErrBlockTooBig, str)
	}

	// The first transaction in a block must be a coinbase.
	transactions := block.Transactions()
	coinbaseIdx := len(transactions) - 1
	if !IsCoinBase(transactions[coinbaseIdx]) {
		return ruleError(ErrLastTxNotCoinbase, "last transaction in "+
			"block is not a coinbase")
	}

	calculatedPoaHash := msgBlock.CalculatePoaHash()
	if !header.PoaHash.IsEqual(&calculatedPoaHash) {
		str := fmt.Sprintf("block poa hash is invalid - block "+
			"header indicates %v, but calculated value is %v",
			header.PoaHash, calculatedPoaHash)
		return ruleError(ErrBadPoaHash, str)
	}

	// A block must not have more than one coinbase.
	for i, tx := range transactions[:coinbaseIdx] {
		if IsCoinBase(tx) {
			str := fmt.Sprintf("block contains second coinbase at "+
				"index %d", i+1)
			return ruleError(ErrMultipleCoinbases, str)
		}
	}

	// Do some preliminary checks on each transaction to ensure they are
	// sane before continuing.
	for _, tx := range transactions {
		err := CheckTransactionSanity(tx)
		if err != nil {
			return err
		}
	}

	// Build merkle tree and ensure the calculated merkle root matches the
	// entry in the block header.  This also has the effect of caching all
	// of the transaction hashes in the block to speed up future hash
	// checks.  Bitcoind builds the tree here and checks the merkle root
	// after the following checks, but there is no reason not to check the
	// merkle root matches here.
	merkles := BuildMerkleTreeStore(block.Transactions())
	calculatedMerkleRoot := merkles[len(merkles)-1]
	if !header.MerkleRoot.IsEqual(calculatedMerkleRoot) {
		str := fmt.Sprintf("block merkle root is invalid - block "+
			"header indicates %v, but calculated value is %v",
			header.MerkleRoot, calculatedMerkleRoot)
		return ruleError(ErrBadMerkleRoot, str)
	}

	// Check for duplicate transactions.  This check will be fairly quick
	// since the transaction hashes are already cached due to building the
	// merkle tree above.
	existingTxHashes := make(map[common.Hash]struct{})
	for _, tx := range transactions {
		hash := tx.Hash()
		if _, exists := existingTxHashes[*hash]; exists {
			str := fmt.Sprintf("block contains duplicate "+
				"transaction %v", hash)
			return ruleError(ErrDuplicateTx, str)
		}
		existingTxHashes[*hash] = struct{}{}
	}

	// The number of signature operations must be less than the maximum
	// allowed per block.
	totalSigOps := 0
	for _, tx := range transactions {
		// We could potentially overflow the accumulator so check for
		// overflow.
		lastSigOps := totalSigOps
		totalSigOps += CountSigOps(tx)
		if totalSigOps < lastSigOps || totalSigOps > MaxBlockSigOpsCost {
			str := fmt.Sprintf("block contains too many signature "+
				"operations - got %v, max %v", totalSigOps,
				MaxBlockSigOpsCost)
			return ruleError(ErrTooManySigOps, str)
		}
	}

	return nil
}

// CheckBlockSanity performs some preliminary checks on a block to ensure it is
// sane before continuing with block processing.  These checks are context free.
func CheckBlockSanity(block *asiutil.Block, parent *blockNode) error {
	return checkBlockSanity(block, parent, common.BFNone)
}

// ExtractCoinbaseHeight attempts to extract the height of the block from the
// scriptSig of a coinbase transaction.  Coinbase heights are only present in
// blocks of version 2 or later.  This was added as part of BIP0034.
func ExtractCoinbaseHeight(coinbaseTx *asiutil.Tx) (int32, error) {
	sigScript := coinbaseTx.MsgTx().TxIn[0].SignatureScript
	if len(sigScript) < 1 {
		str := "the coinbase signature script for blocks " +
			"must start with the length of the serialized block height"
		return 0, ruleError(ErrMissingCoinbaseHeight, str)
	}

	// Detect the case when the block height is a small integer encoded with
	// as single byte.
	opcode := int(sigScript[0])
	if opcode == txscript.OP_0 {
		return 0, nil
	}
	if opcode >= txscript.OP_1 && opcode <= txscript.OP_16 {
		return int32(opcode - (txscript.OP_1 - 1)), nil
	}

	// Otherwise, the opcode is the length of the following bytes which
	// encode in the block height.
	serializedLen := int(sigScript[0])
	if len(sigScript[1:]) < serializedLen {
		str := "the coinbase signature script for blocks " +
			"must start with the serialized block height"
		return 0, ruleError(ErrMissingCoinbaseHeight, str)
	}

	serializedHeightBytes := make([]byte, 8)
	copy(serializedHeightBytes, sigScript[1:serializedLen+1])
	serializedHeight := binary.LittleEndian.Uint64(serializedHeightBytes)

	return int32(serializedHeight), nil
}

// checkSerializedHeight checks if the signature script in the passed
// transaction starts with the serialized block height of wantHeight.
func checkSerializedHeight(coinbaseTx *asiutil.Tx, wantHeight int32) error {
	serializedHeight, err := ExtractCoinbaseHeight(coinbaseTx)
	if err != nil {
		return err
	}

	if serializedHeight != wantHeight {
		str := fmt.Sprintf("the coinbase signature script serialized "+
			"block height is %d when %d was expected",
			serializedHeight, wantHeight)
		return ruleError(ErrBadCoinbaseHeight, str)
	}
	return nil
}

// checkBlockHeaderContext performs several validation checks on the block header
// which depend on its position within the block chain.
//
// The flags modify the behavior of this function as follows:
//  - types.BFFastAdd: All checks except those involving comparing the header against
//    the checkpoints are not performed.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) checkBlockHeaderContext(header *protos.BlockHeader, prevNode *blockNode, round *ainterface.Round) error {
	// The height of this block is one more than the referenced previous
	// block.
	blockHeight := prevNode.height + 1
	if header.Height != blockHeight {
		str := "block height mismatch of %v is not %v"
		str = fmt.Sprintf(str, header.Height, blockHeight)
		return ruleError(ErrHeightMismatch, str)
	}

	// Ensure chain matches up to predetermined checkpoints.
	blockHash := header.BlockHash()
	if !b.verifyCheckpoint(blockHeight, &blockHash) {
		str := fmt.Sprintf("block at height %d does not match "+
			"checkpoint hash", blockHeight)
		return ruleError(ErrBadCheckpoint, str)
	}

	// Ensure the timestamp for the block header is in the
	// range of allowed timestamp of the last several blocks.
	interval := header.Timestamp - round.RoundStartUnix
	expected := round.Duration * int64(header.SlotIndex)
	expected = expected / int64(chaincfg.ActiveNetParams.RoundSize)
	if interval < expected-int64(chaincfg.Cfg.MaxTimeOffset) ||
		interval > expected+int64(chaincfg.Cfg.MaxTimeOffset) {
		str := "block timestamp %d - round start %d = %d is out of range [%d, %d]"
		str = fmt.Sprintf(str, header.Timestamp, round.RoundStartUnix, interval,
			expected-int64(chaincfg.Cfg.MaxTimeOffset),
			expected+int64(chaincfg.Cfg.MaxTimeOffset))
		return ruleError(ErrTimeStampOutOfRange, str)
	}

	// The gas limit of this block
	gasLimit := CalcGasLimit(prevNode.GasUsed(), prevNode.GasLimit(), common.GasFloor, common.GasCeil)
	if gasLimit != header.GasLimit {
		str := fmt.Sprintf("block at height %d does not match "+
			"gas limit", blockHeight)
		return ruleError(ErrBadGasLimit, str)
	}

	// Find the previous checkpoint and prevent blocks which fork the main
	// chain before it.  This prevents storage of new, otherwise valid,
	// blocks which build off of old blocks that are likely at a much easier
	// difficulty and therefore could be used to waste cache and disk space.
	checkpointNode, err := b.findPreviousCheckpoint()
	if err != nil {
		return err
	}
	if checkpointNode != nil && blockHeight < checkpointNode.height {
		str := fmt.Sprintf("block at height %d forks the main chain "+
			"before the previous checkpoint at height %d",
			blockHeight, checkpointNode.height)
		return ruleError(ErrForkTooOld, str)
	}

	if header.SlotIndex >= chaincfg.ActiveNetParams.RoundSize {
		str := fmt.Sprintf("slot is out of range: height=%d, round=%d, slot=%d",
			header.Height, header.Round, header.SlotIndex)
		return ruleError(ErrInvalidSlotIndex, str)
	}

	return nil
}

// checkBlockContext performs several validation checks on the block which depend
// on its position within the block chain.
//
// The flags modify the behavior of this function as follows:
//  - types.BFFastAdd: The transaction are not checked to see if they are finalized
//    and the somewhat expensive BIP0034 validation is not performed.
//
// The flags are also passed to checkBlockHeaderContext.  See its documentation
// for how the flags modify its behavior.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) checkBlockContext(block *asiutil.Block, prevNode *blockNode,
	round *ainterface.Round, flags common.BehaviorFlags) error {
	// Perform all block header related validation checks.
	header := &block.MsgBlock().Header
	err := b.checkBlockHeaderContext(header, prevNode, round)
	if err != nil {
		return err
	}

	fastAdd := flags&common.BFFastAdd == common.BFFastAdd
	if !fastAdd {
		// Once the CSV soft-fork is fully active, we'll switch to
		// using the current time past of the past block's
		// timestamps for all lock-time based checks.
		blockTime := prevNode.GetTime()

		// The height of this block is one more than the referenced
		// previous block.
		blockHeight := prevNode.height + 1

		// The block gas limit of this block
		blockgaslimit := block.MsgBlock().Header.GasLimit

		coinbaseIdx := len(block.Transactions()) - 1

		// Ensure all transactions in the block are finalized.
		for i, tx := range block.Transactions() {
			if !IsFinalizedTransaction(tx, blockHeight,
				blockTime) {

				str := fmt.Sprintf("block contains unfinalized "+
					"transaction %v", tx.Hash())
				return ruleError(ErrUnfinalizedTx, str)
			}
			txgaslimit := uint64(tx.MsgTx().TxContract.GasLimit)
			if i == coinbaseIdx {
				if txgaslimit != common.CoinbaseTxGas {
					errStr := fmt.Sprintf("coinbase tx gas should be %d, but %d",
						common.CoinbaseTxGas, tx.MsgTx().TxContract.GasLimit)
					return ruleError(ErrGasLimitOverFlow, errStr)
				}
			} else {
				if txgaslimit < uint64(tx.MsgTx().SerializeSize()*common.GasPerByte) {
					errStr := fmt.Sprintf("tx gaslimit %d is less than its size cost %d",
						tx.MsgTx().TxContract.GasLimit, tx.MsgTx().SerializeSize()*common.GasPerByte)
					return ruleError(ErrGasLimitOverFlow, errStr)
				}
			}
			if blockgaslimit < txgaslimit {
				errStr := fmt.Sprintf("txs total gaslimit %d is overflow by block gaslimit",
					txgaslimit)
				return ruleError(ErrGasLimitOverFlow, errStr)
			}
			blockgaslimit -= txgaslimit
		}

		// Ensure coinbase starts with serialized block heights for
		// blocks whose version is the serializedHeightVersion or newer
		// once a majority of the network has upgraded.  This is part of
		// BIP0034.
		coinbaseTx := block.Transactions()[coinbaseIdx]
		err = checkSerializedHeight(coinbaseTx, blockHeight)
		if err != nil {
			return err
		}

		// check if the coinbase in global table.
		if !b.roundManager.HasValidator(header.CoinBase) {
			errStr := fmt.Sprintf("the miner is unknown %d", header.CoinBase)
			return ruleError(ErrValidatorMismatch, errStr)
		}

		err = b.checkSignatures(block)
		if err != nil {
			return err
		}
	}

	return nil
}

// Check signature only.
func (b *BlockChain) checkSignatures(block *asiutil.Block) error {
	header := &block.MsgBlock().Header
	bestHeight := header.Height - 1
	for i, preSig := range block.MsgBlock().PreBlockSigs {
		// The max height depth is 10
		if preSig.BlockHeight+common.BlockSignDepth < bestHeight || preSig.BlockHeight > bestHeight {
			errStr := fmt.Sprintf("Block contains sig too deep, bestHeight %d, deep %d", bestHeight, preSig.BlockHeight)
			return ruleError(ErrBadPreSigHeight, errStr)
		}

		if !b.roundManager.HasValidator(preSig.Signer) {
			errStr := fmt.Sprintf("the Signer of preSig is unknown %d", preSig.Signer)
			return ruleError(ErrValidatorMismatch, errStr)
		}

		// signature verify
		err := AddressVerifySignature(preSig.BlockHash[:], &preSig.Signer, preSig.Signature[:])
		if err != nil {
			log.Warnf("Verify pre signature failed: height = %d, PreIndex = %d", header.Height, i)
			return err
		}
	}

	err := AddressVerifySignature(block.Hash()[:], &header.CoinBase, header.SigData[:])
	if err != nil {
		log.Errorf("Verify signature failed: height=%d, round=%d, slot=%d, hash=%v",
			header.Height, header.Round, header.SlotIndex, block.Hash())
		return err
	}
	return nil
}

func (b *BlockChain) checkSignaturesWeight(node *blockNode, block *asiutil.Block, view *txo.UtxoViewpoint) error {
	header := block.MsgBlock().Header

	//get the validators of current block:
	validators, weightMap, err := b.GetValidators(node.round.Round)
	if err != nil {
		return err
	}
	if *validators[header.SlotIndex] != header.CoinBase {
		str := fmt.Sprintf("checkSignaturesWeight miner invalid: height=%d, round=%d, slot=%d, miner=%v",
			header.Height, header.Round, header.SlotIndex, header.CoinBase.String())
		return ruleError(ErrValidatorMismatch, str)
	}

	selfWeight := weightMap[header.CoinBase]

	// check PreblkSignature in this block
	weight := uint16(0)
	var prenode *blockNode
	preSigs := block.MsgBlock().PreBlockSigs
	bestHeight := header.Height - 1
	for i, preSig := range preSigs {
		if i > 0 {
			if !preSigs.Less(i-1, i) {
				errStr := fmt.Sprintf("Block signs are not in order [%d, %d]", i-1, i)
				return ruleError(ErrPreSigOrder, errStr)
			}
		}
		// The max height depth is 10
		if preSig.BlockHeight+common.BlockSignDepth < bestHeight {
			errStr := fmt.Sprintf("Block contains sig too deep, bestHeight %d, deep %d", bestHeight, preSig.BlockHeight)
			return ruleError(ErrBadPreSigHeight, errStr)
		}
		for prenode = node.parent; prenode != nil; prenode = prenode.parent {
			if prenode.height == preSig.BlockHeight {
				if prenode.hash != preSig.BlockHash {
					errStr := fmt.Sprintf("presig mismatch block hash %v vs %v", preSig.BlockHash, prenode.hash)
					return ruleError(ErrHashMismatch, errStr)
				}
				if prenode.coinbase == preSig.Signer {
					errStr := fmt.Sprintf("presig signer error: %v is the same with signed block coinbaseAddr", prenode.coinbase)
					return ruleError(ErrValidatorMismatch, errStr)
				}
				break
			}
			if prenode.height < preSig.BlockHeight {
				break
			}
		}
		if prenode == nil || prenode.height < preSig.BlockHeight {
			continue
		}

		_, weightMap, err := b.GetValidators(prenode.round.Round)
		if err != nil {
			return err
		}

		if _, ok := weightMap[preSig.Signer]; !ok {
			errStr := fmt.Sprintf("presig mismatch signer %v ", preSig.Signer)
			return ruleError(ErrValidatorMismatch, errStr)
		}

		weight += weightMap[preSig.Signer]
	}
	// verify weight
	if header.Weight != selfWeight+weight {
		errStr := fmt.Sprintf("Block weight verify failed, weight %d, expected %d ", header.Weight, selfWeight+weight)
		return ruleError(ErrBadBlockWeight, errStr)
	}

	// check pre block sign.
	if len(block.Signs()) > 0 {
		chkSigErr := b.db.View(func(dbTx database.Tx) error {
			for _, sign := range block.Signs() {
				if view != nil {
					action := view.TryGetSignAction(sign.MsgSign);
					if action == txo.ViewRm {
						continue
					}
					if action == txo.ViewAdd {
						return ruleError(ErrSignDuplicate, "sign duplicated")
					}
				}
				has := dbHasSignature(dbTx, sign)
				if has {
					return ruleError(ErrSignDuplicate, "sign duplicated")
				}
			}
			return nil
		})
		if chkSigErr != nil {
			return chkSigErr
		}
		if view != nil {
			for _, sign := range block.Signs() {
				view.AddViewSign(sign.MsgSign)
			}
		}
	}

	return nil
}

// CheckTransactionInputs performs a series of checks on the inputs to a
// transaction to ensure they are valid.  An example of some of the checks
// include verifying all inputs exist, ensuring the coinbase seasoning
// requirements are met, detecting double spends, validating all values and fees
// are in the legal range and the total output amount doesn't exceed the input
// amount, and verifying the signatures to prove the spender was the owner of
// the bitcoins and therefore allowed to spend them.  As it checks the inputs,
// it also calculates the total fees for the transaction and returns that value.
//
// NOTE: The transaction MUST have already been sanity checked with the
// CheckTransactionSanity function prior to calling this function.
func CheckTransactionInputs(tx *asiutil.Tx, txHeight int32, utxoView *txo.UtxoViewpoint,
	b *BlockChain) (int64, *map[protos.Asset]int64, error) {

	// Coinbase transactions have no inputs.
	if IsCoinBase(tx) {
		return 0, nil, nil
	}

	// VTX needn't be checked.
	if tx.Type() == asiutil.TxTypeVM {
		return 0, nil, nil
	}

	txHash := tx.Hash()

	// fee can be flowAsset only.
	// no more than one asset in one tx except flowAsset .
	//var totalCoinXingIn int64
	//var totalFlowAssetIn int64
	totalInCoin := make(map[protos.Asset]int64)

	//undivisible.
	totalInAsset := make(map[protos.Asset]map[int64]struct{})

	for txInIndex, txIn := range tx.MsgTx().TxIn {
		// Ensure the referenced input transaction is available.
		utxo := utxoView.LookupEntry(txIn.PreviousOutPoint)
		if utxo == nil || utxo.IsSpent() {
			str := fmt.Sprintf("CheckTransactionInputs: output %v referenced from "+
				"transaction %s:%d either does not exist or "+
				"has already been spent", txIn.PreviousOutPoint,
				tx.Hash(), txInIndex)
			return 0, nil, ruleError(ErrMissingTxOut, str)
		}

		// Ensure the transaction is not spending coins which have not
		// yet reached the required coinbase maturity.
		if utxo.IsCoinBase() {
			originHeight := utxo.BlockHeight()
			blocksSincePrev := txHeight - originHeight
			coinbaseMaturity := int32(b.chainParams.CoinbaseMaturity)
			if blocksSincePrev < coinbaseMaturity {
				str := fmt.Sprintf("CheckTransactionInputs: tried to spend coinbase "+
					"transaction output %v from height %v "+
					"at height %v before required maturity "+
					"of %v blocks", txIn.PreviousOutPoint,
					originHeight, txHeight,
					coinbaseMaturity)
				return 0, nil, ruleError(ErrImmatureSpend, str)
			}
		}

		// Ensure the transaction amounts are in range.  Each of the
		// output values of the input transactions must not be negative
		// or more than the max allowed per transaction.  All amounts in
		// a transaction are in a unit value known as a xing.  One
		// asimov coin is a quantity of xing as defined by the
		// XingPerAsimov common.
		originTxXing := utxo.Amount()
		if originTxXing <= 0 {
			str := fmt.Sprintf("CheckTransactionInputs: txHash = %s, txIn.PreviousOutPoint = %v,"+
				"txInIndex = %d, transaction output has negative "+
				"value of %v", tx.Hash(), txIn.PreviousOutPoint, txInIndex, common.Amount(originTxXing))
			return 0, nil, ruleError(ErrBadTxOutValue, str)
		}
		if originTxXing > common.MaxXing {
			str := fmt.Sprintf("CheckTransactionInputs: transaction output value of %v is "+
				"higher than max allowed value of %v",
				common.Amount(originTxXing),
				common.MaxXing)
			return 0, nil, ruleError(ErrBadTxOutValue, str)
		}

		if !utxo.Asset().IsIndivisible() {
			if _, ok := totalInCoin[*utxo.Asset()]; ok {
				totalInCoin[*utxo.Asset()] += utxo.Amount()
			} else {
				totalInCoin[*utxo.Asset()] = utxo.Amount()
			}

		} else {
			if _, ok := totalInAsset[*utxo.Asset()]; !ok {
				totalInAsset[*utxo.Asset()] = make(map[int64]struct{})
			}

			assetInfo := totalInAsset[*utxo.Asset()]
			if _, ok := assetInfo[utxo.Amount()]; ok {
				str := fmt.Sprintf("CheckTransactionInputs: duplicated input asset "+
					"no %v asset type of %v", utxo.Amount(), utxo.Asset())
				return 0, nil, ruleError(ErrDuplicateTxInputs, str)

			} else {
				assetInfo[utxo.Amount()] = struct{}{}
			}
		}

	}

	// Calculate the total output amount for this transaction.  It is safe
	// to ignore overflow and out of range errors here because those error
	// conditions would have already been caught by checkTransactionSanity.
	totalOutCoin := make(map[protos.Asset]int64)
	totalOutAsset := make(map[protos.Asset]map[int64]struct{})

	for outIdx, txOut := range tx.MsgTx().TxOut {

		if outIdx != 0 && txscript.HasContractOp(txOut.PkScript) {
			str := fmt.Sprintf("CheckTransactionInputs: invalid outputs with contract call in not first output.")
			return 0, nil, ruleError(ErrInvalidOutput, str)
		}

		if !txOut.Asset.IsIndivisible() {
			if _, ok := totalOutCoin[txOut.Asset]; ok {
				totalOutCoin[txOut.Asset] += txOut.Value
			} else {
				totalOutCoin[txOut.Asset] = txOut.Value
			}

		} else {
			if _, ok := totalOutAsset[txOut.Asset]; !ok {
				totalOutAsset[txOut.Asset] = make(map[int64]struct{})
			}

			assetInfo := totalOutAsset[txOut.Asset]
			if _, ok := assetInfo[txOut.Value]; ok {
				str := fmt.Sprintf("CheckTransactionInputs: duplicated out asset no %v asset type of %v",
					txOut.Value, txOut.Asset)
				return 0, nil, ruleError(ErrDuplicateTxInputs, str)

			} else {
				assetInfo[txOut.Value] = struct{}{}
			}
		}
	}

	//check undivisible.
	if len(totalOutAsset) != len(totalInAsset) {
		str := fmt.Sprintf("CheckTransactionInputs: number of input asset of %v are "+
			"not equal number output of %v ",
			len(totalInAsset), len(totalOutAsset))
		return 0, nil, ruleError(ErrAssetsNotEqual, str)
	}
	for k, outAsset := range totalOutAsset {
		inAsset, ok := totalInAsset[k]
		if !ok {
			str := fmt.Sprintf("CheckTransactionInputs: miss asset inputs for "+
				"transaction %v, asset %v", txHash, k)
			return 0, nil, ruleError(ErrNoTxInputs, str)
		}

		if len(outAsset) != len(inAsset) {
			str := fmt.Sprintf("CheckTransactionInputs: number of input asset of %v are "+
				"not equal number output of %v for asset type %v ",
				len(inAsset), len(outAsset), k)
			return 0, nil, ruleError(ErrAssetsNotEqual, str)
		}

		for assetNum := range outAsset {
			if _, has := inAsset[assetNum]; !has {
				str := fmt.Sprintf("CheckTransactionInputs: miss asset inputs for "+
					"transaction %v ,asset %v, asset num %v", txHash, k, assetNum)
				return 0, nil, ruleError(ErrNoTxInputs, str)
			}
		}
	}

	fees := make(map[protos.Asset]int64)
	for k, out := range totalOutCoin {
		in, ok := totalInCoin[k]
		if !ok {
			str := fmt.Sprintf("CheckTransactionInputs: miss asset inputs for "+
				"transaction %v ,asset %v", txHash, k)
			return 0, nil, ruleError(ErrAssetsNotEqual, str)
		}

		if in < out {
			str := fmt.Sprintf("CheckTransactionInputs: total value of asset inputs for "+
				"transaction %v, asset %v, value %v is less than output"+
				"spent of %v", txHash, k, in, out)
			return 0, nil, ruleError(ErrSpendTooHigh, str)
		}

		if in > out {
			fees[k] = in - out
		}

		delete(totalInCoin, k)
	}

	//pure fee
	for k, v := range totalInCoin {
		fees[k] = v
	}

	totalFee := int64(0)
	for k, v := range fees {
		fee := GetUsd(&k, v)
		if fee > 0 {
			totalFee += fee
		}
	}

	return totalFee, &fees, nil
	// NOTE: it is possible to have more than one Asset input
	// which will be spent to allow other asset as fee.
}

func CheckVTransactionInputs(tx *asiutil.Tx, utxoView *txo.UtxoViewpoint) error {
	for txInIndex, txIn := range tx.MsgTx().TxIn {
		isVtxMint := asiutil.IsMintOrCreateInput(txIn)
		if isVtxMint {
			continue
		}

		utxo := utxoView.LookupEntry(txIn.PreviousOutPoint)
		if utxo == nil || utxo.IsSpent() {
			str := fmt.Sprintf("CheckVTransactionInputs: vtx preoutput %v referenced from "+
				"transaction %s:%d either does not exist or "+
				"has already been spent", txIn.PreviousOutPoint,
				tx.Hash(), txInIndex)
			return ruleError(ErrBadTxInput, str)
		}

		originTxXing := utxo.Amount()
		if originTxXing <= 0 {
			str := fmt.Sprintf("CheckVTransactionInputs: transaction preoutput has non-positve "+
				"value of %v", common.Amount(originTxXing))
			return ruleError(ErrBadTxInput, str)
		}
		if !utxo.Asset().IsIndivisible() && originTxXing > common.MaxXing {
			str := fmt.Sprintf("CheckVTransactionInputs: transaction preoutput value of %v is "+
				"higher than max allowed value of %v",
				common.Amount(originTxXing),
				common.MaxXing)
			return ruleError(ErrBadTxInput, str)
		}
	}

	for _, txOut := range tx.MsgTx().TxOut {
		if txOut.Value <= 0 {
			str := fmt.Sprintf("CheckVTransactionInputs: transaction output has non-positve "+
				"value of %v", txOut.Value)
			return ruleError(ErrBadTxOutValue, str)
		}

		if !txOut.Asset.IsIndivisible() && txOut.Value > common.MaxXing {
			str := fmt.Sprintf("CheckVTransactionInputs: transaction output value of %v is "+
				"higher than max allowed value of %v",
				txOut.Value,
				common.MaxXing)
			return ruleError(ErrBadTxOutValue, str)
		}
	}

	return nil
}

// we should get the value from the market maker contract.
func GetUsd(asset *protos.Asset, value int64) int64 {
	return 1e8 / 1e8 * value
}

func IsActive(addr *common.Address, curNode *blockNode) bool {
	targetRound := curNode.round.Round - chaincfg.ActiveNetParams.KeepAliveInterval

	for node := curNode; node != nil; node = node.parent {
		if node.round.Round <= uint32(targetRound) {
			break
		}
		if node.Coinbase() == *addr {
			return true
		}
	}
	return false
}

// GetValidators depends on current round miners and pre-round stateRoot.
func (b *BlockChain) GetValidators(round uint32) ([]*common.Address, map[common.Address]uint16, error) {
	// found preround's last node
	node := b.bestChain.Tip()
	if round > node.round.Round+1 {
		log.Warnf("target round is too bigger than latest round: %d, %d", round, node.round.Round)
	}
	for ; node != nil; node = node.parent {
		if node.round.Round < round {
			break
		}
	}
	return b.GetValidatorsByNode(round, node)
}

// GetValidatorsByNode depends on current round miners and pre-round last node.
func (b *BlockChain) GetValidatorsByNode(round uint32, preroundLastNode *blockNode) ([]*common.Address, map[common.Address]uint16, error) {
	if preroundLastNode == nil {
		return b.roundManager.GetValidators(common.Hash{}, round, nil)
	}
	fn := func(mineraddrs []string) ([]common.Address, []int32, error) {
		// get validators via node
		header := protos.BlockHeader{
			Timestamp: preroundLastNode.timestamp,
			Height:    preroundLastNode.height,
			StateRoot: preroundLastNode.stateRoot,
		}
		block := asiutil.NewBlock(&protos.MsgBlock{
			Header: header,
		})

		var stateDB *state.StateDB
		stateDB, _ = state.New(header.StateRoot, b.stateCache)
		if stateDB == nil {
			return nil, nil, common.AssertError("stateDB is nil")
		}

		signupValidators, rounds, err := b.contractManager.GetSignedUpValidators(b.roundManager.GetContract(), block,
			stateDB, chaincfg.ActiveNetParams.FvmParam, mineraddrs)
		if err != nil {
			return nil, nil, err
		}

		filters := make([]int32, len(signupValidators))
		for i, sv := range signupValidators {
			if rounds[i] == 0xFFFFFFFF || rounds[i] > round {
				filters[i] = -1
				continue
			}
			if round-rounds[i] <= chaincfg.ActiveNetParams.KeepAliveInterval || IsActive(&sv, preroundLastNode) {
				filters[i] = 1
			}
		}
		return signupValidators, filters, err
	}
	return b.roundManager.GetValidators(preroundLastNode.hash, round, fn)
}

// checkConnectBlock performs several checks to confirm connecting the passed
// block to the chain represented by the passed view does not violate any rules.
// In addition, the passed view is updated to spend all of the referenced
// outputs and add all of the new utxos created by block.  Thus, the view will
// represent the state of the chain as if the block were actually connected and
// consequently the best hash for the view is also updated to passed block.
//
// An example of some of the checks performed are ensuring connecting the block
// would not cause any duplicate transaction hashes for old transactions that
// aren't already fully spent, double spends, exceeding the maximum allowed
// signature operations per block, invalid values in relation to the expected
// block subsidy, or fail transaction script validation.
//
// The checkConnectBlock function makes use of this function to perform
// the bulk of its work.  The only difference is this function accepts a node
// which may or may not require reorganization to connect it to the main chain
// whereas checkConnectBlock creates a new node which specifically
// connects to the end of the current main chain and then calls this function
// with that node.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) checkConnectBlock(node *blockNode, block *asiutil.Block, view *txo.UtxoViewpoint,
	stxos *[]txo.SpentTxOut, msgvblock *protos.MsgVBlock) (
	types.Receipts, []*types.Log, error) {

	// Check Sig & Weight
	err := b.checkSignaturesWeight(node, block, view)
	if err != nil {
		return nil, nil, err
	}

	//contract related statedb info.
	statedb, err := state.New(node.parent.stateRoot, b.stateCache)
	if err != nil {
		return nil, nil, err
	}
	feepool, err := b.GetAcceptFees(block,
		statedb, chaincfg.ActiveNetParams.FvmParam, block.Height())
	if err != nil {
		return nil, nil, err
	}
	// If the side chain blocks end up in the database, a call to
	// CheckBlockSanity should be done here in case a previous version
	// allowed a block that is no longer valid.  However, since the
	// implementation only currently uses memory for the side chain blocks,
	// it isn't currently necessary.

	// The coinbase for the Genesis block is not spendable, so just return
	// an error now.
	if node.hash.IsEqual(b.chainParams.GenesisHash) {
		str := "invalid hash when checking block connection: the block to be checked can not be genesis block"
		return nil, nil, ruleError(ErrInvalidBlockHash, str)
	}

	// Ensure the view is for the node being checked.
	parentHash := &block.MsgBlock().Header.PrevBlock
	if !view.BestHash().IsEqual(parentHash) {
		errStr := fmt.Sprintf("inconsistent view when "+
			"checking block connection: best hash is %v instead "+
			"of expected %v", view.BestHash(), parentHash)
		return nil, nil, ruleError(ErrHashMismatch, errStr)
	}

	// Load all of the utxos referenced by the inputs for all transactions
	// in the block don't already exist in the utxo view from the database.
	//
	// These utxo entries are needed for verification of things such as
	// transaction inputs, counting pay-to-script-hashes, and scripts.
	err = fetchInputUtxos(view, b.db, block)
	if err != nil {
		return nil, nil, err
	}

	// The number of signature operations must be less than the maximum
	// allowed per block.  Note that the preliminary sanity checks on a
	// block also include a check similar to this one, but this check
	// expands the count to include a precise count of pay-to-script-hash
	// signature operations in each of the input transaction public key
	// scripts.
	transactions := block.Transactions()
	coinbaseIdx := len(transactions) - 1
	totalSigOpCost := 0
	for i, tx := range transactions {
		// Since the first (and only the first) transaction has
		// already been verified to be a coinbase transaction,
		// use i == 0 as an optimization for the flag to
		// countP2SHSigOps for whether or not the transaction is
		// a coinbase transaction rather than having to do a
		// full coinbase check again.
		sigOpCost, err := GetSigOpCost(tx, i == coinbaseIdx, view)
		if err != nil {
			return nil, nil, err
		}

		// Check for overflow or going over the limits.  We have to do
		// this on every loop iteration to avoid overflow.
		lastSigOpCost := totalSigOpCost
		totalSigOpCost += sigOpCost
		if totalSigOpCost < lastSigOpCost || totalSigOpCost > MaxBlockSigOpsCost {
			str := fmt.Sprintf("block contains too many "+
				"signature operations - got %v, max %v",
				totalSigOpCost, MaxBlockSigOpsCost)
			return nil, nil, ruleError(ErrTooManySigOps, str)
		}
	}

	// Perform several checks on the inputs for each transaction.  Also
	// accumulate the total fees.  This could technically be combined with
	// the loop above instead of running another loop over the transactions,
	// but by separating it we can avoid running the more expensive (though
	// still relatively cheap as compared to running the scripts) checks
	// against all the inputs when the signature operations are out of
	// bounds.
	var totalGasUsed uint64
	allFees := make(map[protos.Asset]int64)
	var (
		receipts          types.Receipts
		allLogs           []*types.Log
		totalFeeLockItems map[protos.Asset]*txo.LockItem
	)
	for i, tx := range transactions {
		fee, feeList, err := CheckTransactionInputs(tx, node.height, view, b)
		if err != nil {
			return nil, nil, err
		}

		// Sum the total fees and ensure we don't overflow the
		// accumulator.
		if feeList != nil {
			for asset, _ := range *feeList {
				if _, ok := feepool[asset]; !ok {
					errstr := fmt.Sprintf("Skipping tx %s because its "+
						"fee %v is unsupported", tx.Hash(), asset)
					return nil, nil, ruleError(ErrForbiddenAsset, errstr)
				}
			}
			err = MergeFees(&allFees, feeList)
			if err != nil {
				return nil, nil, err
			}
		}

		// Add all of the outputs for this transaction which are not
		// provably unspendable as available utxos.  Also, the passed
		// spent txos slice is updated to contain an entry for each
		// spent txout in the order each transaction spends them.
		statedb.Prepare(*tx.Hash(), *block.Hash(), i)
		receipt, err, gasUsed, vtx, feeLockItems := b.ConnectTransaction(block, i, view, tx, stxos, statedb, fee)
		if receipt != nil {
			receipts = append(receipts, receipt)
			allLogs = append(allLogs, receipt.Logs...)
		}
		if err != nil {
			return nil, nil, err
		}
		totalGasUsed += gasUsed
		if vtx != nil && msgvblock != nil {
			msgvblock.AddTransaction(vtx)
		}
		if totalFeeLockItems == nil {
			totalFeeLockItems = feeLockItems
		} else if feeLockItems != nil {
			for k, item := range feeLockItems {
				if titem, ok := totalFeeLockItems[k]; ok {
					titem.Merge(item)
				} else {
					totalFeeLockItems[k] = item
				}
			}
		}
	}

	if block.MsgBlock().Header.GasUsed != totalGasUsed {
		errStr := fmt.Sprintf("total gas used mismatch, header %d vs calc %d.",
			block.MsgBlock().Header.GasUsed, totalGasUsed)
		return nil, nil, ruleError(ErrGasMismatch, errStr)
	}

	if err := b.checkCoinbaseTx(node.parent, block, allFees); err != nil {
		return nil, nil, err
	}

	// Don't run scripts if this node is before the latest known good
	// checkpoint since the validity is verified via the checkpoints (all
	// transactions are included in the merkle root hash and any changes
	// will therefore be detected by the next checkpoint).  This is a huge
	// optimization because running the scripts is the most time consuming
	// portion of block handling.
	checkpoint := b.LatestCheckpoint()
	runScripts := true
	if checkpoint != nil && node.height <= checkpoint.Height {
		runScripts = false
	}

	// Blocks created after the BIP0016 activation time need to have the
	// pay-to-script-hash checks enabled.
	var scriptFlags txscript.ScriptFlags
	scriptFlags |= txscript.ScriptBip16

	// Enforce CHECKLOCKTIMEVERIFY for block versions 4+ once the historical
	// activation threshold has been reached.  This is part of BIP0065.
	scriptFlags |= txscript.ScriptVerifyCheckLockTimeVerify

	// If the CSV soft-fork is now active, then modify the
	// scriptFlags to ensure that the CSV op code is properly
	// validated during the script checks bleow.
	scriptFlags |= txscript.ScriptVerifyCheckSequenceVerify

	// We obtain the MTP of the *previous* block in order to
	// determine if transactions in the current block are final.
	medianTime := node.parent.GetTime()

	// Additionally, if the CSV soft-fork package is now active,
	// then we also enforce the relative sequence number based
	// lock-times within the inputs of all transactions in this
	// candidate block.
	for _, tx := range block.Transactions() {
		// A transaction can only be included within a block
		// once the sequence locks of *all* its inputs are
		// active.
		sequenceLock, err := b.calcSequenceLock(node, tx, view,
			false)
		if err != nil {
			return nil, nil, err
		}
		if !SequenceLockActive(sequenceLock, node.height,
			medianTime) {
			str := fmt.Sprintf("block contains " +
				"transaction whose input sequence " +
				"locks are not met")
			return nil, nil, ruleError(ErrUnfinalizedTx, str)
		}
	}

	// Now that the inexpensive checks are done and have passed, verify the
	// transactions are actually allowed to spend the coins by running the
	// expensive ECDSA signature check scripts.  Doing this last helps
	// prevent CPU exhaustion attacks.
	if runScripts {
		err := checkBlockScripts(block, view, scriptFlags)
		if err != nil {
			return nil, nil, err
		}
	}

	// Update the best hash for view to include this block since all of its
	// transactions have been connected.
	view.SetBestHash(&node.hash)

	//save dbstate.
	stateRoot, err := statedb.Commit(true)
	if err != nil {
		return nil, nil, err
	}
	if err := statedb.Database().TrieDB().Commit(stateRoot, false); err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(node.stateRoot[:], stateRoot[:]) {
		return nil, nil, ruleError(ErrStateRootNotMatch, "state root of the block is not matched.")
	}

	updateFeeLockItems(block, view, totalFeeLockItems)

	return receipts, allLogs, nil
}

func (b *BlockChain) createCoinbaseContractOut(preround uint32, preroundLastNode *blockNode) (*protos.TxOut, error) {
	proxy, _, abi := b.GetSystemContractInfo(b.roundManager.GetContract())
	updateValidatorBlockInfoFunc := common.ContractConsensusSatoshiPlus_UpdateValidatorsBlockInfoFunction()
	validators, expectedBlocks, actualBlocks, mapping, err := b.CountRoundMinerInfo(preround, preroundLastNode)
	if err != nil {
		return nil, err
	}
	bigIntExpected := make([]*big.Int, len(expectedBlocks))
	for i, v := range expectedBlocks {
		bigIntExpected[i] = big.NewInt(int64(v))
	}
	bigIntActual := make([]*big.Int, len(actualBlocks))
	for i, v := range actualBlocks {
		bigIntActual[i] = big.NewInt(int64(v))
	}
	miners := make([]string, 0, len(mapping))
	mineraddresses := make([]common.Address, 0, len(mapping))
	minerNames := make([]string, 0, len(mapping))
	for miner, _ := range mapping {
		miners = append(miners, miner)
	}
	sort.Strings(miners)
	for _, miner := range miners {
		v := mapping[miner]
		mineraddresses = append(mineraddresses, v.Address)
		minerNames = append(minerNames, v.MinerName)
	}

	newround := preround + 1
	activeRound := newround + chaincfg.ActiveNetParams.MappingDelayInterval
	bigIntRound := big.NewInt(int64(activeRound))
	input, err := fvm.PackFunctionArgs(abi, updateValidatorBlockInfoFunc, validators,
		bigIntExpected, bigIntActual, miners, mineraddresses, bigIntRound, minerNames)
	if err != nil {
		return nil, err
	}
	pkscript, err := txscript.PayToAddrScript(&proxy)
	return protos.NewContractTxOut(0, pkscript, asiutil.AsimovAsset, input), nil
}

// The total output values of the coinbase transaction must not exceed
// the expected subsidy value plus total transaction fees gained from
// mining the block.  It is safe to ignore overflow and out of range
// errors here because those error conditions would have already been
// caught by checkTransactionSanity.
func (b *BlockChain) checkCoinbaseTx(prenode *blockNode, block *asiutil.Block, allFees map[protos.Asset]int64) error {
	coinbaseIdx := len(block.Transactions()) - 1
	coinbaseTx := block.Transactions()[coinbaseIdx].MsgTx()
	mineFeelist := make(map[protos.Asset]int64)
	for _, txOut := range coinbaseTx.TxOut {
		mineFeelist[txOut.Asset] += txOut.Value
	}

	reward := CalcBlockSubsidy(block.Height(), b.chainParams)
	allFees[asiutil.AsimovAsset] += reward

	if len(allFees) != len(mineFeelist) {
		str := fmt.Sprintf("checkCoinbaseTx: the asset numbers of allFees: %v "+
			"are not equal to numbers of mineFeelist: %v", len(allFees), len(mineFeelist))
		return ruleError(ErrBadCoinbaseValue, str)
	}
	// match fees
	for k, v := range mineFeelist {
		vv, ok := allFees[k]
		if !ok || v != vv {
			str := fmt.Sprintf("checkCoinbaseTx: coinbase transaction for block pays " +
				"which is more than expected value of")
			return ruleError(ErrBadCoinbaseValue, str)
		}
	}
	// reward for core team
	if block.Height() <= chaincfg.ActiveNetParams.Params.SubsidyReductionInterval ||
		block.MsgBlock().Header.Timestamp-b.chainParams.GenesisBlock.Header.Timestamp < 86400*365*4 {
		if err := checkCoreTeamReward(coinbaseTx, mineFeelist); err != nil {
			return err
		}
	}
	from := 0
	// check consensus call data
	round := block.MsgBlock().Header.Round
	if prenode.round.Round < round {
		actualOut := coinbaseTx.TxOut[0]
		expectedOut, err := b.createCoinbaseContractOut(prenode.round.Round, prenode)
		if err != nil {
			errStr := fmt.Sprintf("failed to create coinbase, %v", err)
			return ruleError(ErrBadCoinbaseData, errStr)
		}
		if bytes.Compare(expectedOut.Data, actualOut.Data) != 0 ||
			bytes.Compare(expectedOut.PkScript, actualOut.PkScript) != 0 ||
			expectedOut.Value != actualOut.Value || !expectedOut.Asset.Equal(&actualOut.Asset) {
			errStr := fmt.Sprintf("coinbase tx contains unexpected contract txout %s, expect: %v actual: %v",
				block.MsgBlock().Header.CoinBase.String(), expectedOut, actualOut)
			return ruleError(ErrBadCoinbaseData, errStr)
		}
		from++
	}
	// other out should not contain data.
	for _, txOut := range coinbaseTx.TxOut[from:] {
		if len(txOut.Data) > 0 {
			str := fmt.Sprintf("coinbase tx contains unexpected out data %s",
				block.MsgBlock().Header.CoinBase.String())
			return ruleError(ErrBadCoinbaseData, str)
		}
	}

	return nil
}

// validate the reward of the core team in the coinbase tx.
func checkCoreTeamReward(coinbaseTx *protos.MsgTx, mineFeelist map[protos.Asset]int64) error {
	fundationAddr := common.HexToAddress(string(common.GenesisOrganization))
	pkScript, _ := txscript.PayToAddrScript(&fundationAddr)
	coreTeamReward := make(map[protos.Asset]int64)
	for _, txout := range coinbaseTx.TxOut {
		if bytes.Equal(pkScript, txout.PkScript) {
			coreTeamReward[txout.Asset] += txout.Value
		}
	}

	for k, v := range mineFeelist {
		coreTeamValue := int64(float64(v) * common.CoreTeamPercent)
		if coreTeamValue > 0 {
			vv, ok := coreTeamReward[k]
			if !ok || vv < coreTeamValue {
				str := fmt.Sprintf("coinbase transaction for block misses core team reward")
				return ruleError(ErrBadCoinbaseValue, str)
			}
		}
	}
	return nil
}

// GetSigOpCost returns the unified sig op cost for the passed transaction
// respecting current active soft-forks which modified sig op cost counting.
func GetSigOpCost(tx *asiutil.Tx, isCoinBaseTx bool, utxoView *txo.UtxoViewpoint) (int, error) {
	numSigOps := CountSigOps(tx)
	numP2SHSigOps, err := CountP2SHSigOps(tx, isCoinBaseTx, utxoView)
	if err != nil {
		return 0, nil
	}
	numSigOps += numP2SHSigOps

	return numSigOps, nil
}

func pkScriptToAddr(pkScript []byte) ([]byte, error) {
	scriptClass, addrs, requiredSigs, err := txscript.ExtractPkScriptAddrs(pkScript)
	if err != nil || len(addrs) <= 0 {
		log.Info("extra caller addr error,", scriptClass, addrs, requiredSigs, err)
		return nil, err
	}
	callerAddr := addrs[0].StandardAddress()
	return callerAddr[:], nil
}

// suppose both parameters are valid (Divisible)
func MergeFees(totalp *map[protos.Asset]int64, feep *map[protos.Asset]int64) error {
	if totalp == nil || feep == nil {
		return nil
	}

	total := *totalp
	for asset, v := range *feep {

		if _, has := total[asset]; has {
			lastTotalFee := total[asset]
			total[asset] += v
			if total[asset] < lastTotalFee {
				return ruleError(ErrBadFees, "total fee for block overflows accumulator")
			}
		} else {
			total[asset] = v
		}
	}
	return nil
}

//TODO
func (b *BlockChain) GetAcceptFees(
	block *asiutil.Block,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	height int32) (map[protos.Asset]int32, error) {
	fees, err := b.contractManager.GetFees(block, stateDB, chainConfig)
	if err != nil {
		return nil, err
	}

	res := make(map[protos.Asset]int32)
	for asset, value := range fees {
		if value <= height {
			res[asset] = value
		}
	}
	res[asiutil.AsimovAsset] = 0
	return res, nil
}

//TODO
func (b *BlockChain) GetByteCode(
	view *txo.UtxoViewpoint,
	block *asiutil.Block,
	gas uint64,
	stateDB vm.StateDB,
	chainConfig *params.ChainConfig,
	category uint16,
	name string) ([]byte, bool, uint64) {

	contractTemplate, ok, leftOverGas := b.contractManager.GetTemplate(block, gas, stateDB, chainConfig, category, name)
	if !ok {
		return nil, false, leftOverGas
	}

	if contractTemplate.Status != syscontract.TEMPLATE_STATUS_APPROVE {
		log.Info("Template's status is not equal approved.")
		return nil, false, leftOverGas
	}

	// Get byte code by key
	keyHash := common.HexToHash(contractTemplate.Key)
	_, _, byteCode, _, _, err := b.FetchTemplate(view, &keyHash)
	if err != nil {
		return nil, false, leftOverGas
	}

	return byteCode, true, leftOverGas
}

// CalcGasLimit computes the gas limit of the next block after parent. It aims
// to keep the baseline gas above the provided floor, and increase it towards the
// ceil if the blocks are full. If the ceil is exceeded, it will always decrease
// the gas allowance.
func CalcGasLimit(gasUsed, gasLimit uint64, gasFloor, gasCeil uint64) uint64 {
	// contrib = (parentGasUsed * 3 / 2) / 1024
	contrib := (gasUsed + (gasUsed >> 1)) / params.GasLimitBoundDivisor

	// decay = parentGasLimit / 1024 -1
	decay := gasLimit/params.GasLimitBoundDivisor - 1

	/*
		strategy: gasLimit of block-to-mine is set based on parent's
		gasUsed value.  if parentGasUsed > parentGasLimit * (2/3) then we
		increase it, otherwise lower it (or leave it unchanged if it's right
		at that usage) the amount increased/decreased depends on how far away
		from parentGasLimit * (2/3) parentGasUsed is.
	*/
	limit := gasLimit - decay + contrib
	if limit < params.MinGasLimit {
		limit = params.MinGasLimit
	}
	// If we're outside our allowed gas range, we try to hone towards them
	if limit < gasFloor {
		limit = gasLimit + decay
		if limit > gasFloor {
			limit = gasFloor
		}
	} else if limit > gasCeil {
		limit = gasLimit - decay
		if limit < gasCeil {
			limit = gasCeil
		}
	}
	return limit
}
