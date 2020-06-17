// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package mining

const (
	// UnminedHeight is the height used for the "block" height field of the
	// contextual transaction information provided in a transaction store
	// when it has not yet been mined into a block.
	UnminedHeight = 0x7fffffff
)

// Policy houses the policy (configuration parameters) which is used to control
// the generation of block templates.  See the documentation for
// NewBlockTemplate for more details on each of these parameters are used.
type Policy struct {
	// TxMinPrice is the minimum price in Xing per byte that is
	// required for a transaction to be treated as free for mining purposes
	// (block template generation).
	TxMinPrice float64

	// BlockProductedTimeOut limits a block producing time.
	// It is the maximum percent (default 0.5) of producing block interval.
	BlockProductedTimeOut float64

	// TxConnectTimeOut limits a tx connecting time, include executing vm.
	// It is the maximum percent (default 0.7) of producing block producing
	// interval.
	TxConnectTimeOut float64

	// UtxoValidateTimeOut limits source txs' utxo validating time.
	// It is the maximum percent (default 0.35) of producing block interval.
	UtxoValidateTimeOut float64

	// BlockSyncTime is the time for synchronizing a block.
	// The default value is 1000 ms.
	BlockSyncTime float64
}
