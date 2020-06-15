// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.


package rpcjson

import (
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm/core/types"
)

type CreateRawTransactionResult struct {
	Hex          string            `json:"hex"`
	ContractAddr map[uint64]string `json:"contractaddr"`
}

// GetRawMempoolVerboseResult models the data returned from the getrawmempool
// command when the verbose flag is set.  When the verbose flag is not set,
// getrawmempool returns an array of transaction hashes.
type GetRawMempoolVerboseResult struct {
	Size    int32    `json:"size"`
	Fee     int64    `json:"fee"`
	Time    int64    `json:"time"`
	Height  int64    `json:"height"`
	Depends []string `json:"depends"`
}

// GetBlockTemplateResultAux models the coinbaseaux field of the
// getblocktemplate command.
type GetBlockTemplateResultAux struct {
	Flags string `json:"flags"`
}

// ScriptSig models a signature script.  It is defined separately since it only
// applies to non-coinbase.  Therefore the field in the Vin structure needs
// to be a pointer.
type ScriptSig struct {
	Asm string `json:"asm"`
	Hex string `json:"hex"`
}

// Vin models parts of the tx data.  It is defined separately since
// getrawtransaction, decoderawtransaction, and searchrawtransaction use the
// same structure.
type Vin struct {
	Coinbase  string     `json:"coinbase"`
	Txid      string     `json:"txid"`
	Vout      uint32     `json:"vout"`
	ScriptSig *ScriptSig `json:"scriptSig"`
	Sequence  uint32     `json:"sequence"`
}

// ScriptPubKeyResult models the scriptPubKey data of a tx script.  It is
// defined separately since it is used by multiple commands.
type ScriptPubKeyResult struct {
	Asm       string   `json:"asm"`
	Hex       string   `json:"hex,omitempty"`
	ReqSigs   int32    `json:"reqSigs,omitempty"`
	Type      string   `json:"type"`
	Addresses []string `json:"addresses,omitempty"`
}

// Vout models parts of the tx data.  It is defined separately since both
// getrawtransaction and decoderawtransaction use the same structure.
type Vout struct {
	Value        int64              `json:"value"`
	N            uint32             `json:"n"`
	ScriptPubKey ScriptPubKeyResult `json:"scriptPubKey"`
	Data         string             `json:"data"`
	Asset        string             `json:"asset"`
}

type FeeResult struct {
	Value int64  `json:"value"`
	Asset string `json:"asset"`
}


// GetBlockHeaderVerboseResult models the data from the getblockheader command when
// the verbose flag is set.  When the verbose flag is not set, getblockheader
// returns a hex-encoded string.
type GetBlockHeaderVerboseResult struct {
	Hash          string `json:"hash"`
	Confirmations int64  `json:"confirmations"`
	Height        int32  `json:"height"`
	Version       int32  `json:"version"`
	// VersionHex    string `json:"versionHex"`
	MerkleRoot   string `json:"merkleroot"`
	StateRoot    string `json:"stateroot"`
	Time         int64  `json:"time"`
	PreviousHash string `json:"previousblockhash,omitempty"`
	NextHash     string `json:"nextblockhash,omitempty"`
	GasLimit     int64  `json:"gaslimist,omitempty"`
	GasUsed      int64  `json:"gasused,omitempty"`
}

type LogResult struct {
	// Consensus fields:
	// address of the contract that generated the event
	Address string `json:"address" gencodec:"required"`
	// list of topics provided by the contract.
	Topics []string `json:"topics" gencodec:"required"`
	// supplied by the contract, usually ABI-encoded
	Data string `json:"data" gencodec:"required"`

	// Derived fields. These fields are filled in by the node
	// but not secured by consensus.
	// block in which the transaction was included
	BlockNumber uint64 `json:"blockNumber"`
	// hash of the transaction
	TxHash string `json:"transactionHash" gencodec:"required"`
	// index of the transaction in the block
	TxIndex uint `json:"transactionIndex" gencodec:"required"`
	// hash of the block in which the transaction was included
	BlockHash string `json:"blockHash"`
	// index of the log in the receipt
	Index uint `json:"logIndex" gencodec:"required"`

	// The Removed field is true if this log was reverted due to a chain reorganisation.
	// You must pay attention to this field if you receive logs through a filter query.
	Removed bool `json:"removed"`
}

type ReceiptResult struct {
	// Consensus fields
	PostState         string       `json:"root"`
	Status            uint64       `json:"status"`
	CumulativeGasUsed uint64       `json:"cumulativeGasUsed" gencodec:"required"`
	Bloom             string       `json:"logsBloom"         gencodec:"required"`
	Logs              []*LogResult `json:"logs"              gencodec:"required"`

	// Implementation fields (don't reorder!)
	TxHash          string `json:"transactionHash" gencodec:"required"`
	ContractAddress string `json:"contractAddress"`
	GasUsed         uint64 `json:"gasUsed" gencodec:"required"`
}

// GetBlockVerboseResult models the data from the getblock command when the
// verbose flag is set.  When the verbose flag is not set, getblock returns a
// hex-encoded string.
type GetBlockVerboseResult struct {
	Hash          string           `json:"hash"`
	Confirmations int64            `json:"confirmations"`
	Size          int32            `json:"size"`
	Height        int64            `json:"height"`
	Version       int32            `json:"version"`
	MerkleRoot    string           `json:"merkleroot"`
	Tx            []string         `json:"tx,omitempty"`
	RawTx         []TxResult       `json:"rawtx,omitempty"`
	Time          int64            `json:"time"`
	TxCount       uint64           `json:"txCount"`
	PreviousHash  string           `json:"previousblockhash"`
	StateRoot     string           `json:"stateroot"`
	NextHash      string           `json:"nextblockhash,omitempty"`
	Vtxs          []TxResult       `json:"vtxs,omitempty"`
	Receipts      []*ReceiptResult `json:"receipts,omitempty"`
	Round         uint32           `json:"round"`
	Slot          uint16           `json:"slot"`
	GasLimit      uint64           `json:"gaslimit,omitempty"`
	GasUsed       uint64           `json:"gasused,omitempty"`
	Reward        int64            `json:"reward,omitempty"`
	FeeList       []FeeResult      `json:"FeeList,omitempty"`
	PreSigList    []MsgSignResult  `json:"presiglist,omitempty"`
	Weight        uint16           `json:"weight"`
}

// DecodeScriptResult models the data returned from the decodescript command.
type DecodeScriptResult struct {
	Asm       string   `json:"asm"`
	ReqSigs   int32    `json:"reqSigs,omitempty"`
	Type      string   `json:"type"`
	Addresses []string `json:"addresses,omitempty"`
	P2sh      string   `json:"p2sh,omitempty"`
}

// GetBlockChainInfoResult models the data returned from the getblockchaininfo
// command.
type GetBlockChainInfoResult struct {
	Chain         string `json:"chain"`
	Blocks        int32  `json:"blocks"`
	BestBlockHash string `json:"bestblockhash"`
	MedianTime    int64  `json:"mediantime"`
	Round         int32  `json:"round"`
	Slot          int16  `json:"slot"`
}

type GetConsensusMiningInfoResult struct {
	ContractAddr    string `json:"contractAddr"`
	MiningMemberAbi string `json:"miningMemberAbi"`
	SignUpFunc      string `json:"signUpFunc"`
}

// MsgSignResult models the data from the GetBlock command.
type MsgSignResult struct {
	BlockHeight int32  `json:"blockheight"`
	BlockHash   string `json:"blockhash"`
	Signer      string `json:"signer"`
	Signature   string `json:"signature"`
}

type GetSignUpStatusResult struct {
	AutoSignUp  bool              `json:"autoSignUp"`
	PreOutsList []protos.OutPoint `json:"preOutsList"`
	Balance     int64             `json:"balance"`
}

type GetMergeUtxoResult struct {
	Asset string					`json:"asset"`
	Value int64						`json:"value"`
	PreOutsList []protos.OutPoint	`json:"preOutsList"`
}

// GetNetTotalsResult models the data returned from the getnettotals command.
type GetNetTotalsResult struct {
	TotalBytesRecv uint64 `json:"totalbytesrecv"`
	TotalBytesSent uint64 `json:"totalbytessent"`
	TimeMillis     int64  `json:"timemillis"`
}

// PrevOut represents previous output for an input Vin.
type PrevOut struct {
	Addresses []string `json:"addresses,omitempty"`
	Value     int64    `json:"value"`
	Asset     string   `json:"asset"`
	Data      string   `json:"data"`
}

// VinPrevOut is like Vin except it includes PrevOut.  It is used by searchrawtransaction
type VinPrevOut struct {
	Coinbase  string     `json:"coinbase"`
	Txid      string     `json:"txid"`
	Vout      uint32     `json:"vout"`
	ScriptSig *ScriptSig `json:"scriptSig"`
	PrevOut   *PrevOut   `json:"prevOut,omitempty"`
	Sequence  uint32     `json:"sequence"`
}

// TxRawResult models the data from the getrawtransaction command.
type TxRawResult struct {
	Hex           string      `json:"hex"`
	Txid          string      `json:"txid"`
	Hash          string      `json:"hash,omitempty"`
	Size          int32       `json:"size,omitempty"`
	Version       uint32      `json:"version"`
	LockTime      uint32      `json:"locktime"`
	Vin           []Vin       `json:"vin"`
	Vout          []Vout      `json:"vout"`
	BlockHash     string      `json:"blockhash,omitempty"`
	Confirmations int64       `json:"confirmations,omitempty"`
	Time          int64       `json:"time,omitempty"`
	Blocktime     int64       `json:"blocktime,omitempty"`
	Fee           []FeeResult `json:"fee,omitempty"`
	GasLimit      int64       `json:"gaslimit"`
}

// TxRawResult models the data from the getrawtransaction command.
type TxResult struct {
	Hex           string       `json:"hex"`
	Txid          string       `json:"txid"`
	Hash          string       `json:"hash,omitempty"`
	Size          int32        `json:"size,omitempty"`
	Version       uint32       `json:"version"`
	LockTime      uint32       `json:"locktime"`
	Vin           []VinPrevOut `json:"vin"`
	Vout          []Vout       `json:"vout"`
	BlockHash     string       `json:"blockhash,omitempty"`
	Confirmations int64        `json:"confirmations,omitempty"`
	Time          int64        `json:"time,omitempty"`
	Blocktime     int64        `json:"blocktime,omitempty"`
	Fee           []FeeResult  `json:"fee,omitempty"`
	GasLimit      int64        `json:"gaslimit"`
	GasUsed       int64        `json:"gasused"`
}

// SearchRawTransactionsResult models the data from the searchrawtransaction
// command.
type SearchRawTransactionsResult struct {
	Hex           string       `json:"hex,omitempty"`
	Txid          string       `json:"txid"`
	Hash          string       `json:"hash"`
	Size          string       `json:"size"`
	Vsize         string       `json:"vsize"`
	Version       uint32       `json:"version"`
	LockTime      uint32       `json:"locktime"`
	Vin           []VinPrevOut `json:"vin"`
	Vout          []Vout       `json:"vout"`
	BlockHash     string       `json:"blockhash,omitempty"`
	Confirmations int64        `json:"confirmations,omitempty"`
	Time          int64        `json:"time,omitempty"`
	Blocktime     int64        `json:"blocktime,omitempty"`
	GasLimit      int64        `json:"gaslimit,omitempty"`
	GasUsed       int64        `json:"gasused,omitempty"`
	Fee           []FeeResult  `json:"fee,omitempty"`
}

// TxRawDecodeResult models the data from the decoderawtransaction command.
type TxRawDecodeResult struct {
	Txid     string `json:"txid"`
	Version  uint32 `json:"version"`
	Locktime uint32 `json:"locktime"`
	Vin      []Vin  `json:"vin"`
	Vout     []Vout `json:"vout"`
}

type CallLogResult struct {
	EventName string      `json:"eventname"`
	Log       interface{} `json:"log"`
}

type CallResult struct {
	Result  interface{}     `json:"result"`
	Logs    []CallLogResult `json:"logs"`
	Name    string          `json:"name,omitempty"`
	GasUsed uint64          `json:"gasused,omitempty"`
}
type TestResult struct {
	List []CallResult `json:"list"`
}

type FeeItemResult struct {
	Assets string `json:"assets"`
	Height int32  `json:"height"`
}

type RunTxResult struct {
	Receipt *types.Receipt     `json:"receipt"`
	GasUsed uint64             `json:"gasUsed"`
	VTX     *TxRawDecodeResult `json:"vtx"`
}

type ContractTemplate struct {
	TemplateTName string `json:"template_name"`
	TemplateType  uint16 `json:"template_type"`
}

type ContractTemplateDetail struct {
	Category     uint16 `json:"category"`
	TemplateName string `json:"template_name"`
	ByteCode     string `json:"byte_code"`
	Abi          string `json:"abi"`
	Source       string `json:"source"`
}

type LockEntryResult struct {
	Id     string	`json:"id"`
	Amount int64	`json:"amount"`
}

// ListUnspentResult models a successful response from the listunspent request.
type ListUnspentResult struct {
	TxID          string  `json:"txid"`
	Vout          uint32  `json:"vout"`
	Address       string  `json:"address"`
	Height        int32   `json:"height"`
	ScriptPubKey  string  `json:"scriptPubKey"`
	//RedeemScript  string  `json:"redeemScript,omitempty"`
	Amount        int64   `json:"amount"`
	Confirmations int64   `json:"confirmations"`
	Spendable     bool    `json:"spendable"`
	Assets        string  `json:"assets"`
	ListLockEntry []*LockEntryResult `json:"locks"`
}

type UnspentPageResult struct {
	ListUnspent []*ListUnspentResult  `json:"utxos"`
	Count       int32                 `json:"count"`
}

// GetBestBlockResult models the data from the getbestblock command.
type GetBestBlockResult struct {
	Hash   string `json:"hash"`
	Height int32  `json:"height"`
}

type GetBalanceResult struct {
	Asset string `json:"asset"`
	Value string `json:"value"`
}
type GetBalancesResult struct {
	Address string             `json:"address"`
	Assets  []GetBalanceResult `json:"assets"`
}
