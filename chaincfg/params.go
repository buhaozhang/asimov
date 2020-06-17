// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package chaincfg

import (
	"errors"
	"math"

	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/vm/fvm/params"
)

// Checkpoint identifies a known good point in the block chain.  Using
// checkpoints allows a few optimizations for old blocks during initial download
// and also prevents forks from old blocks.
//
// Each checkpoint is selected based upon several factors.  See the
// documentation for blockchain.IsCheckpointCandidate for details on the
// selection criteria.
type Checkpoint struct {
	Height int32
	Hash   *common.Hash
}

// DNSSeed identifies a DNS seed.
type DNSSeed struct {
	// Host defines the hostname of the seed.
	Host string

	// HasFiltering defines whether the seed supports filtering
	// by service flags (wire.ServiceFlag).
	HasFiltering bool
}

// ConsensusDeployment defines details related to a specific consensus rule
// change that is voted in.  This is part of BIP0009.
type ConsensusDeployment struct {
	// BitNumber defines the specific bit number within the block version
	// this particular soft-fork deployment refers to.
	BitNumber uint8

	// StartTime is the median block time after which voting on the
	// deployment starts.
	StartTime int64

	// ExpireTime is the median block time after which the attempted
	// deployment expires.
	ExpireTime int64
}

// Constants that define the deployment offset in the deployments field of the
// parameters for each deployment.  This is useful to be able to get the details
// of a specific deployment by name.
const (
	// DeploymentTestDummy defines the rule change deployment ID for testing
	// purposes.
	DeploymentTestDummy = iota

	// NOTE: DefinedDeployments must always come last since it is used to
	// determine how many defined deployments there currently are.

	// DefinedDeployments is the number of currently defined deployments.
	DefinedDeployments
)

type BitcoinParams struct {
	Host        string
	RpcUser     string
	RpcPassword string
}

// Params defines an asimov network by its parameters.  These parameters may be
// used by asimov applications to differentiate networks as well as addresses
// and keys for one network from those intended for use on another network.
type Params struct {
	// Net defines the magic bytes used to identify the network.
	Net common.AsimovNet

	// DefaultPort defines the default peer-to-peer port for the network.
	DefaultPort string

	// DNSSeeds defines a list of DNS seeds for the network that are used
	// as one method to discover peers.
	DNSSeeds []DNSSeed

	// GenesisBlock defines the first block of the chain.
	GenesisBlock *protos.MsgBlock

	// GenesisHash is the starting block hash.
	GenesisHash *common.Hash

	// GenesisHash is the default candidates for selecting validators
	GenesisCandidates []common.Address

	// CoinbaseMaturity is the number of blocks required before newly mined
	// coins (coinbase transactions) can be spent.
	CoinbaseMaturity int32

	// SubsidyReductionInterval is the interval of blocks before the subsidy
	// is reduced.
	SubsidyReductionInterval int32

	// ChainStartTime is the time of last slot in round 0. It also represents
	// the first expected block time - 5 seconds.
	ChainStartTime int64

	// RoundSize is the interval of blocks before the next round is started.
	RoundSize uint16

	// BtcBlocksPerRound is the expected number of blocks in bitcoin generated
	// in asimov round.
	BtcBlocksPerRound uint16

	// CollectHeight is the height of bitcoin which is the initial collect
	// height.
	CollectHeight int32

	// CollectInterval is the interval of blocks in bitcoin which used as a
	// poa round source data.
	CollectInterval int32

	// MappingDelayInterval is the interval of block rounds when the
	// validator can work after a bitcoin mapping tx include in block.
	MappingDelayInterval uint32

	// KeepAliveInterval is the interval of block rounds which is the
	// max number for a validator keep alive.
	KeepAliveInterval uint32

	// Checkpoints ordered from oldest to newest.
	Checkpoints []Checkpoint

	// These fields are related to voting on consensus rule changes as
	// defined by BIP0009.
	//
	// RuleChangeActivationThreshold is the number of blocks in a threshold
	// state retarget window for which a positive vote for a rule change
	// must be cast in order to lock in a rule change. It should typically
	// be 95% for the main network and 75% for test networks.
	//
	// MinerConfirmationWindow is the number of blocks in each threshold
	// state retarget window.
	//
	// Deployments define the specific consensus rule changes to be voted
	// on.
	RuleChangeActivationThreshold uint32
	MinerConfirmationWindow       uint32
	Deployments                   [DefinedDeployments]ConsensusDeployment

	FvmParam *params.ChainConfig

	Bitcoin []*BitcoinParams
}

// Name defines a human-readable identifier for the network.
func (p *Params) Name() string {
	return p.Net.String()
}

// MainNetParams defines the network parameters for the main Bitcoin network.
var MainNetParams = Params{
	Net:         common.MainNet,
	DefaultPort: "8777",
	DNSSeeds:    []DNSSeed{
		{"seed1.asimov.testnet", false},
		{"seed2.asimov.testnet", false},
	},

	// Chain parameters
	GenesisHash:       &mainnetGenesisHash,
	GenesisCandidates: []common.Address{
		common.HexToAddress("0x66f29f0fd5008c9b676a32744839d9953d07e2bfc6"),
		common.HexToAddress("0x66347c825835a984374d16bddbf674cfb6645e7bc1"),
		common.HexToAddress("0x663cb5d3ec78b7e37a218f7ee50c84118e59e46a76"),
	},
	CoinbaseMaturity:         4320,
	SubsidyReductionInterval: 25000000,
	RoundSize:                720,
	BtcBlocksPerRound:        6,

	CollectHeight:   632500,
	CollectInterval: 2016,

	MappingDelayInterval: 24 * 3,
	KeepAliveInterval:    24,

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1916, // 95% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016, //
	Deployments: [DefinedDeployments]ConsensusDeployment{},

	FvmParam: params.MainnetChainConfig,

	Bitcoin: []*BitcoinParams{
		{
			Host:        "btc.asimov.network:8300",
			RpcUser:     "asimov",
			RpcPassword: "asimov",
		},
	},
}

// DevelopNetParams defines the network parameters for the develop
// Asimov network.
var DevelopNetParams = Params{
	Net:         common.DevelopNet,
	DefaultPort: "18700",
	DNSSeeds: []DNSSeed{
		{"seed1.asimov.tech", true},
		{"seed2.asimov.tech", false},
	},

	// Chain parameters
	GenesisHash:  &devnetGenesisHash,
	GenesisCandidates: []common.Address{
		common.HexToAddress("0x6632032786c61472128d1b3185c92626f8ff0ee4d3"),
		common.HexToAddress("0x668bd8118cc510f8ccd1089bd9d5e44bdc20d6e373"),
		common.HexToAddress("0x66e8e93bb62ade708ccb5715736a75df4a981b20c5"),
	},
	CoinbaseMaturity:         1, //modify from 120 to 1 for test
	SubsidyReductionInterval: 25000000,
	RoundSize:                120,
	BtcBlocksPerRound:        1,
	CollectHeight:            10100,
	CollectInterval:          144,

	MappingDelayInterval: 2,
	KeepAliveInterval:    2,

	// Checkpoints ordered from oldest to newest.
	Checkpoints: nil,

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 108, // 75%  of MinerConfirmationWindow
	MinerConfirmationWindow:       20,
	Deployments: [DefinedDeployments]ConsensusDeployment{
		DeploymentTestDummy: {
			BitNumber:  28,
			StartTime:  0,             // Always available for vote
			ExpireTime: math.MaxInt64, // Never expires
		},
	},

	FvmParam: params.TestnetChainConfig,

	Bitcoin: []*BitcoinParams{
		{
			Host:        "devnet-btc.asimov.tech:18554",
			RpcUser:     "asimov",
			RpcPassword: "asimov",
		},
	},
}

// TestNetParams defines the network parameters for the test flow network.
// Not to be confused with the regression test network, this
// network is sometimes simply called "testnet".
var TestNetParams = Params{
	Net:         common.TestNet,
	DefaultPort: "18721",
	DNSSeeds: []DNSSeed{
		{"seed1.asimov.pro", true},
		{"seed2.asimov.pro", false},
	},

	// Chain parameters
	GenesisHash:  &testnetGenesisHash,

	GenesisCandidates: []common.Address{
		common.HexToAddress("0x6632032786c61472128d1b3185c92626f8ff0ee4d3"),
		common.HexToAddress("0x668bd8118cc510f8ccd1089bd9d5e44bdc20d6e373"),
		common.HexToAddress("0x66e8e93bb62ade708ccb5715736a75df4a981b20c5"),
	},
	CoinbaseMaturity:         240,
	SubsidyReductionInterval: 25000000,
	// 20 minutes
	RoundSize:         240,
	BtcBlocksPerRound: 2,
	CollectHeight:     60,
	CollectInterval:   144,

	MappingDelayInterval: 2,
	KeepAliveInterval:    24,

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1512, // 75% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016,
	Deployments: [DefinedDeployments]ConsensusDeployment{
		DeploymentTestDummy: {
			BitNumber:  28,
			StartTime:  1199145601, // January 1, 2008 UTC
			ExpireTime: 1230767999, // December 31, 2008 UTC
		},
	},

	FvmParam: params.TestnetChainConfig,

	Bitcoin: []*BitcoinParams{
		{
			Host:        "testnet-btc.asimov.tech:18332",
			RpcUser:     "asimov",
			RpcPassword: "asimov",
		},
	},
}

var (
	// ErrDuplicateNet describes an error where the parameters for a Bitcoin
	// network could not be set due to the network already being a standard
	// network or previously-registered into this package.
	ErrDuplicateNet = errors.New("duplicate Bitcoin network")

	// ErrUnknownHDKeyID describes an error where the provided id which
	// is intended to identify the network for a hierarchical deterministic
	// private extended key is not registered.
	ErrUnknownHDKeyID = errors.New("unknown hd private extended key bytes")
)

var (
	registeredNets = make(map[common.AsimovNet]struct{})
)

// String returns the hostname of the DNS seed in human-readable form.
func (d DNSSeed) String() string {
	return d.Host
}

// Register registers the network parameters for a Bitcoin network.  This may
// error with ErrDuplicateNet if the network is already registered (either
// due to a previous Register call, or the network being one of the default
// networks).
//
// Network parameters should be registered into this package by a main package
// as early as possible.  Then, library packages may lookup networks or network
// parameters based on inputs and work regardless of the network being standard
// or not.
func Register(params *Params) error {
	if _, ok := registeredNets[params.Net]; ok {
		return ErrDuplicateNet
	}
	registeredNets[params.Net] = struct{}{}
	return nil
}

// mustRegister performs the same function as Register except it panics if there
// is an error.  This should only be called from package init functions.
func mustRegister(params *Params) {
	if err := Register(params); err != nil {
		panic("failed to register network: " + err.Error())
	}
}

func init() {
	// Register all default networks when the package is initialized.
	mustRegister(&MainNetParams)
	mustRegister(&DevelopNetParams)
	mustRegister(&TestNetParams)
}
