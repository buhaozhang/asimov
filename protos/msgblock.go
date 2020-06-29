// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package protos

import (
	"bytes"
	"fmt"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/common/serialization"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
	"io"
)

// defaultTransactionAlloc is the default size used for the backing array
// for transactions.  The transaction array will dynamically grow as needed, but
// this figure is intended to provide enough space for the number of
// transactions in the vast majority of blocks without needing to grow the
// backing array multiple times.
const defaultTransactionAlloc = 2048

// MaxBlocksPerMsg is the maximum number of blocks allowed per message.
const MaxBlocksPerMsg = 16

// MaxBlockPayload is the maximum bytes a block message can be in bytes.
const MaxBlockPayload = common.MaxBlockSize

// maxTxPerBlock is the maximum number of transactions that could
// possibly fit into a block.
const maxTxPerBlock = (MaxBlockPayload / minTxPayload) + 1

// TxLoc holds locator data for the offset and length of where a transaction is
// located within a MsgBlock data buffer.
type TxLoc struct {
	TxStart int
	TxLen   int
}

// MsgBlock implements the Message interface and represents a bitcoin
// block message.  It is used to deliver block and transaction information in
// response to a getdata message (MsgGetData) for a given block hash.
type MsgBlock struct {
	Header       BlockHeader
	ReceiptHash  common.Hash
	Bloom        types.Bloom
	Transactions []*MsgTx
	PreBlockSigs BlockSignList // collect signatures for ancestors
}

// AddTransaction adds a transaction to the message.
func (msg *MsgBlock) AddTransaction(tx *MsgTx) {
	msg.Transactions = append(msg.Transactions, tx)
}

// ClearTransactions removes all transactions from the message.
func (msg *MsgBlock) ClearTransactions() {
	msg.Transactions = make([]*MsgTx, 0, defaultTransactionAlloc)
}

// VVSDecode decodes r using the bitcoin protocol encoding into the receiver.
// This is part of the Message interface implementation.
// See Deserialize for decoding blocks stored to disk, such as in a database, as
// opposed to decoding blocks from the protos.
func (msg *MsgBlock) VVSDecode(r io.Reader, pver uint32, enc MessageEncoding) error {
	err := readBlockHeader(r, &msg.Header)
	if err != nil {
		return err
	}
	if err := serialization.ReadNBytes(r, msg.ReceiptHash[:], common.HashLength); err != nil {
		return err
	}
	if err := serialization.ReadNBytes(r, msg.Bloom[:], types.BloomByteLength); err != nil {
		return err
	}

	txCount, err := serialization.ReadVarInt(r, pver)
	if err != nil {
		return err
	}

	// Prevent more transactions than could possibly fit into a block.
	// It would be possible to cause memory exhaustion and panics without
	// a sane upper bound on this count.
	if txCount > maxTxPerBlock {
		str := fmt.Sprintf("too many transactions to fit into a block "+
			"[count %d, max %d]", txCount, maxTxPerBlock)
		return messageError("MsgBlock.VVSDecode", str)
	}

	msg.Transactions = make([]*MsgTx, 0, txCount)
	for i := uint64(0); i < txCount; i++ {
		tx := MsgTx{}
		err := tx.VVSDecode(r, pver, enc)
		if err != nil {
			return err
		}
		msg.Transactions = append(msg.Transactions, &tx)
	}

	n, err := serialization.ReadVarUint(r)
	if err != nil {
		return err
	}

	msgSigns := make([]MsgBlockSign, n)
	msg.PreBlockSigs = make([]*MsgBlockSign, n)
	for i := 0; i < int(n); i++ {
		err = msgSigns[i].Deserialize(r)
		if err != nil {
			return err
		}
		msg.PreBlockSigs[i] = &msgSigns[i]
	}

	return nil
}

// Deserialize decodes a block from r into the receiver using a format that is
// suitable for long-term storage such as a database while respecting the
// Version field in the block.  This function differs from VVSDecode in that
// VVSDecode decodes from the bitcoin protos protocol as it was sent across the
// network.  The protos encoding can technically differ depending on the protocol
// version and doesn't even really need to match the format of a stored block at
// all.  As of the time this comment was written, the encoded block is the same
// in both instances, but there is a distinct difference and separating the two
// allows the API to be flexible enough to deal with changes.
func (msg *MsgBlock) Deserialize(r io.Reader) error {
	// At the current time, there is no difference between the protos encoding
	// at protocol version 0 and the stable long-term storage format.  As
	// a result, make use of VVSDecode.
	//
	// Passing an encoding type of BaseEncoding to VVSEncode for the
	// MessageEncoding parameter indicates that the transactions within the
	// block are expected to be serialized according to the new
	// serialization structure defined in BIP0141.
	return msg.VVSDecode(r, 0, BaseEncoding)
}

// DeserializeTxLoc decodes r in the same manner Deserialize does, but it takes
// a byte buffer instead of a generic reader and returns a slice containing the
// start and length of each transaction within the raw data that is being
// deserialized.
func (msg *MsgBlock) DeserializeTxLoc(r *bytes.Buffer) ([]TxLoc, error) {
	fullLen := r.Len()

	// At the current time, there is no difference between the protos encoding
	// at protocol version 0 and the stable long-term storage format.  As
	// a result, make use of existing protos protocol functions.
	err := readBlockHeader(r, &msg.Header)
	if err != nil {
		return nil, err
	}
	if err := serialization.ReadNBytes(r, msg.ReceiptHash[:], common.HashLength); err != nil {
		return nil, err
	}
	if err := serialization.ReadNBytes(r, msg.Bloom[:], types.BloomByteLength); err != nil {
		return nil, err
	}

	txCount, err := serialization.ReadVarInt(r, 0)
	if err != nil {
		return nil, err
	}

	// Prevent more transactions than could possibly fit into a block.
	// It would be possible to cause memory exhaustion and panics without
	// a sane upper bound on this count.
	if txCount > maxTxPerBlock {
		str := fmt.Sprintf("too many transactions to fit into a block "+
			"[count %d, max %d]", txCount, maxTxPerBlock)
		return nil, messageError("MsgBlock.DeserializeTxLoc", str)
	}

	// Deserialize each transaction while keeping track of its location
	// within the byte stream.
	msg.Transactions = make([]*MsgTx, 0, txCount)
	txLocs := make([]TxLoc, txCount)
	for i := uint64(0); i < txCount; i++ {
		txLocs[i].TxStart = fullLen - r.Len()
		tx := MsgTx{}
		err := tx.Deserialize(r)
		if err != nil {
			return nil, err
		}
		msg.Transactions = append(msg.Transactions, &tx)
		txLocs[i].TxLen = (fullLen - r.Len()) - txLocs[i].TxStart
	}

	return txLocs, nil
}

// VVSEncode encodes the receiver to w using the bitcoin protocol encoding.
// This is part of the Message interface implementation.
// See Serialize for encoding blocks to be stored to disk, such as in a
// database, as opposed to encoding blocks for the protos.
func (msg *MsgBlock) VVSEncode(w io.Writer, pver uint32, enc MessageEncoding) error {
	err := writeBlockHeader(w, &msg.Header)
	if err != nil {
		return err
	}
	if err := serialization.WriteNBytes(w, msg.ReceiptHash[:]); err != nil {
		return err
	}
	if err := serialization.WriteNBytes(w, msg.Bloom[:]); err != nil {
		return err
	}

	err = serialization.WriteVarInt(w, pver, uint64(len(msg.Transactions)))
	if err != nil {
		return err
	}

	for _, tx := range msg.Transactions {
		err = tx.VVSEncode(w, pver, enc)
		if err != nil {
			return err
		}
	}

	err = serialization.WriteVarUint(w, uint64(len(msg.PreBlockSigs)))
	if err != nil {
		return err
	}

	for _, sig := range msg.PreBlockSigs {
		err := sig.Serialize(w)
		if err != nil {
			return err
		}
	}

	return nil
}

// Serialize encodes the block to w using a format that suitable for long-term
// storage such as a database while respecting the Version field in the block.
// This function differs from VVSEncode in that VVSEncode encodes the block to
// the bitcoin protos protocol in order to be sent across the network.  The protos
// encoding can technically differ depending on the protocol version and doesn't
// even really need to match the format of a stored block at all.  As of the
// time this comment was written, the encoded block is the same in both
// instances, but there is a distinct difference and separating the two allows
// the API to be flexible enough to deal with changes.
func (msg *MsgBlock) Serialize(w io.Writer) error {
	// At the current time, there is no difference between the protos encoding
	// at protocol version 0 and the stable long-term storage format.  As
	// a result, make use of VVSEncode.
	//
	// Passing BaseEncoding as the encoding type here indicates that
	// each of the transactions should be serialized using the witness
	// serialization structure defined in BIP0141.
	return msg.VVSEncode(w, 0, BaseEncoding)
}

// SerializeSize returns the number of bytes it would take to serialize the
// block, factoring in any witness data within transaction.
func (msg *MsgBlock) SerializeSize() int {
	// Block header bytes + ReceiptHash size + Bloom size
	// + varint size for the number of transactions.
	n := BlockHeaderPayload + common.HashLength + types.BloomByteLength +
		serialization.VarIntSerializeSize(uint64(len(msg.Transactions)))

	for _, tx := range msg.Transactions {
		n += tx.SerializeSize()
	}

	n += serialization.VarIntSerializeSize(uint64(len(msg.PreBlockSigs)))
	for _, sig := range msg.PreBlockSigs {
		n += sig.SerializeSize()
	}

	return n
}

// Command returns the protocol command string for the message.  This is part
// of the Message interface implementation.
func (msg *MsgBlock) Command() string {
	return CmdBlock
}

// MaxPayloadLength returns the maximum length the payload can be for the
// receiver.  This is part of the Message interface implementation.
func (msg *MsgBlock) MaxPayloadLength(pver uint32) uint32 {
	// Block header at 80 bytes + transaction count + max transactions
	// which can vary up to the MaxBlockPayload (including the block header
	// and transaction count).
	return MaxBlockPayload
}

// BlockHash computes the block identifier hash for this block.
func (msg *MsgBlock) BlockHash() common.Hash {
	return msg.Header.BlockHash()
}

// TxHashes returns a slice of hashes of all of transactions in this block.
func (msg *MsgBlock) TxHashes() ([]common.Hash, error) {
	hashList := make([]common.Hash, 0, len(msg.Transactions))
	for _, tx := range msg.Transactions {
		hashList = append(hashList, tx.TxHash())
	}
	return hashList, nil
}

// CalculatePoaHash returns hash value from MsgBlock when run with poa
func (msg *MsgBlock) CalculatePoaHash() common.Hash {
	buflen := common.HashLength + types.BloomByteLength
	for _, sig := range msg.PreBlockSigs {
		buflen += sig.SerializeSize()
	}

	buf := bytes.NewBuffer(make([]byte, 0, buflen))
	serialization.WriteNBytes(buf, msg.ReceiptHash[:])
	serialization.WriteNBytes(buf, msg.Bloom[:])
	for _, sig := range msg.PreBlockSigs {
		sig.Serialize(buf)
	}

	return common.DoubleHashH(buf.Bytes())
}

// NewMsgBlock returns a new bitcoin block message that conforms to the
// Message interface.  See MsgBlock for details.
func NewMsgBlock(blockHeader *BlockHeader) *MsgBlock {
	return &MsgBlock{
		Header:       *blockHeader,
		Transactions: make([]*MsgTx, 0, defaultTransactionAlloc),
	}
}
