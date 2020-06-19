// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satoshiplus

import (
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/blockchain/syscontract"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/common/hexutil"
	"github.com/AsimovNetwork/asimov/consensus/params"
	"github.com/AsimovNetwork/asimov/consensus/satoshiplus/minersync"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/database/dbdriver"
	"github.com/AsimovNetwork/asimov/database/dbimpl/ethdb"
	"github.com/AsimovNetwork/asimov/mempool"
	"github.com/AsimovNetwork/asimov/mining"
	"github.com/AsimovNetwork/asimov/rpcs/rpc"
	"github.com/AsimovNetwork/asimov/protos"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	testDbType                   = "ffldb"
	DefaultConfigFilename        = "asimovd.conf"
	DefaultDataDirname           = "data"
	DefaultLogDirname            = "logs"
	DefaultStateDirname          = "state"
	DefaultMaxOrphanTransactions = 100
	DefaultMaxOrphanTxSize       = 100000
)

var (
	HomeDir            = asiutil.HomeDir()
	defaultHomeDir     = asiutil.AppDataDir("asimovd", false)
	DefaultAppDataDir  = cleanAndExpandPath(asiutil.AppDataDir("asimovd", false))
	DefaultConfigFile  = filepath.Join(DefaultAppDataDir, DefaultConfigFilename)
	DefaultDataDir     = filepath.Join(DefaultAppDataDir, DefaultDataDirname)
	DefaultRPCKeyFile  = filepath.Join(DefaultAppDataDir, "rpc.key")
	DefaultRPCCertFile = filepath.Join(DefaultAppDataDir, "rpc.cert")
	DefaultLogDir      = filepath.Join(DefaultAppDataDir, DefaultLogDirname)
	DefaultStateDir    = filepath.Join(DefaultAppDataDir, DefaultStateDirname)
	Cfg                *chaincfg.FConfig

	DefaultHttpModules      = []string{"net", "web3"}
	DefaultHTTPVirtualHosts = []string{"localhost"}
	DefaultWSModules        = []string{"net", "web3"}
)

func cleanAndExpandPath(path string) string {
	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		homeDir := filepath.Dir(defaultHomeDir)
		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but they variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

func newFakeChain(paramstmp *chaincfg.Params) (*blockchain.BlockChain, func(), *RoundManager, error) {

	cfg := &chaincfg.FConfig{
		ConfigFile:           chaincfg.DefaultConfigFile,
		DebugLevel:           chaincfg.DefaultLogLevel,
		MaxPeers:             chaincfg.DefaultMaxPeers,
		BanDuration:          chaincfg.DefaultBanDuration,
		BanThreshold:         chaincfg.DefaultBanThreshold,
		RPCMaxClients:        chaincfg.DefaultMaxRPCClients,
		RPCMaxWebsockets:     chaincfg.DefaultMaxRPCWebsockets,
		RPCMaxConcurrentReqs: chaincfg.DefaultMaxRPCConcurrentReqs,
		DataDir:              chaincfg.DefaultDataDir,
		LogDir:               chaincfg.DefaultLogDir,
		StateDir:             chaincfg.DefaultStateDir,
		RPCKey:               chaincfg.DefaultRPCKeyFile,
		RPCCert:              chaincfg.DefaultRPCCertFile,
		MinTxPrice:           chaincfg.DefaultMinTxPrice,
		MaxOrphanTxs:         chaincfg.DefaultMaxOrphanTransactions,
		MaxOrphanTxSize:      chaincfg.DefaultMaxOrphanTxSize,
		HTTPEndpoint:         chaincfg.DefaultHTTPEndPoint,
		HTTPModules:          chaincfg.DefaultHttpModules,
		HTTPVirtualHosts:     chaincfg.DefaultHTTPVirtualHosts,
		HTTPTimeouts:         rpc.DefaultHTTPTimeouts,
		WSEndpoint:           chaincfg.DefaultWSEndPoint,
		WSModules:            chaincfg.DefaultWSModules,
		DevelopNet:           true,
		Consensustype:        "satoshiplus",
	}

	consensus := common.GetConsensus(cfg.Consensustype)
	if consensus < 0 {
		log.Errorf("The consensus type is %v, which is not support", consensus)
		return nil, nil, nil, nil
	}
	chaincfg.ActiveNetParams.Params = paramstmp

	var teardown func()
	cfg.DataDir = cleanAndExpandPath(cfg.DataDir)
	cfg.DataDir = filepath.Join(cfg.DataDir, "UnitTest")

	// Append the network type to the logger directory so it is "namespaced"
	// per network in the same fashion as the data directory.
	cfg.LogDir = cleanAndExpandPath(cfg.LogDir)
	cfg.LogDir = filepath.Join(cfg.LogDir, "UnitTest")

	cfg.StateDir = cleanAndExpandPath(cfg.StateDir)
	cfg.StateDir = filepath.Join(cfg.StateDir, "UnitTest")

	// Load the block database.
	db, err := loadBlockDB(cfg)
	if err != nil {
		return nil, nil, nil, err
	}

	// Load StateDB
	stateDB, stateDbErr := ethdb.NewLDBDatabase(cfg.StateDir, 768, 1024)
	if stateDbErr != nil {
		teardown()
		log.Errorf("%v", stateDbErr)
		return nil, nil, nil, stateDbErr
	}

	// Create a btc rpc client
	btcClient := NewFakeBtcClient(chaincfg.ActiveNetParams.CollectHeight,
		chaincfg.ActiveNetParams.CollectInterval+int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)*64+6)
	btcClient.UpInterval()

	// Create a round manager.
	roundManger := NewRoundManager()

	// Create a contract manager.
	contractManager := syscontract.NewContractManager()

	dir, err := os.Getwd()
	var loadBlkErr error
	var genesisBlock *protos.MsgBlock
	if chaincfg.ActiveNetParams.Params.Net == common.TestNet {
		genesisBlock, loadBlkErr = asiutil.LoadBlockFromFile("../../genesisbin/testnet.block")
	} else if chaincfg.ActiveNetParams.Params.Net == common.DevelopNet {
		genesisBlock, loadBlkErr = asiutil.LoadBlockFromFile("../../genesisbin/devnet.block")
	} else {
		genesisBlock, loadBlkErr = asiutil.LoadBlockFromFile("../../genesisbin/mainnet.block")
	}
	if loadBlkErr != nil {
		strErr := "Load genesis block error, " + err.Error() + dir
		return nil, nil, nil, errors.New(strErr)
	}
	genesisHash := asiutil.NewBlock(genesisBlock).Hash()
	if *chaincfg.ActiveNetParams.GenesisHash != *genesisHash {
		strErr := fmt.Sprintf("Load genesis block genesis hash mismatch expected %s, but %s",
			chaincfg.ActiveNetParams.GenesisHash.String(), genesisHash.String())
		return nil, nil, nil, errors.New(strErr)
	}
	chaincfg.ActiveNetParams.GenesisBlock = genesisBlock

	chain, err := blockchain.New(&blockchain.Config{
		DB: db,
		//Interrupt:     interrupt,
		ChainParams: paramstmp,
		//Checkpoints:   checkpoints,
		TimeSource: blockchain.NewMedianTime(),
		//IndexManager:  indexManager,
		StateDB: stateDB,
		//TemplateIndex: s.templateIndex,
		BtcClient:       btcClient,
		RoundManager:    roundManger,
		ContractManager: contractManager,
	}, nil)
	if err != nil {
		teardown = func() {
			db.Close()
			stateDB.Close()
			os.RemoveAll(cfg.DataDir)
			os.RemoveAll(cfg.LogDir)
			os.RemoveAll(cfg.StateDir)
		}
		return nil, nil, nil, err
	}

	roundManger.minerCollector.UpdateMinerInfo(256)

	teardown = func() {
		db.Close()
		stateDB.Close()
		os.RemoveAll(cfg.DataDir)
		os.RemoveAll(cfg.LogDir)
		os.RemoveAll(cfg.StateDir)
	}

	chaincfg.Cfg = cfg
	return chain, teardown, roundManger, nil
}

const (
	// blockDbNamePrefix is the prefix for the block database name.  The
	// database type is appended to this value to form the full block
	// database name.
	blockDbNamePrefix = "blocks"
)

func blockDbPath(dataDir, dbType string) string {
	// The database name is based on the database type.
	dbName := blockDbNamePrefix + "_" + dbType
	if dbType == "sqlite" {
		dbName = dbName + ".db"
	}
	dbPath := filepath.Join(dataDir, dbName)
	return dbPath
}

func loadBlockDB(cfg *chaincfg.FConfig) (database.Database, error) {
	// The memdb backend does not have a file path associated with it, so
	// handle it uniquely.  We also don't want to worry about the multiple
	// database type warnings when running with the memory database.
	//if cfg.DbType == "memdb" {
	//	db, err := dbdriver.Create(cfg.DbType)
	//	if err != nil {
	//		return nil, err
	//	}
	//	return db, nil
	//}

	// The database name is based on the database type.
	dbPath := blockDbPath(cfg.DataDir, testDbType)

	//在测试代码中要连接测试网络
	//db, err := database.Open(cfg.DbType, dbPath, configuration.ActiveNetParams.Net)
	db, err := dbdriver.Open(testDbType, dbPath, chaincfg.MainNetParams.Net)
	if err != nil {
		// Return the error if it's not because the database doesn't
		// exist.
		if dbErr, ok := err.(database.Error); !ok || dbErr.ErrorCode !=
			database.ErrDbDoesNotExist {

			return nil, err
		}

		// Create the db if it does not exist.
		err = os.MkdirAll(cfg.DataDir, 0700)
		if err != nil {
			return nil, err
		}
		//在测试代码中要连接测试网络
		//db, err = database.Create(cfg.DbType, dbPath, configuration.ActiveNetParams.Net)
		db, err = dbdriver.Create(testDbType, dbPath, chaincfg.MainNetParams.Net)
		if err != nil {
			return nil, err
		}
	}

	return db, nil
}


func ProcessBlock(template *mining.BlockTemplate, flags common.BehaviorFlags) (bool, error) {
	return false, nil
}

func createPoaConfig(privateKey string, paramstmp *chaincfg.Params) (*params.Config, func(), error) {
	chain, teardownFunc, roundManager, err := newFakeChain(paramstmp)
	if err != nil || chain == nil {
		return nil, nil, fmt.Errorf("newFakeChain error %v", err)
	}

	privKeyBytes, _ := hexutil.Decode(privateKey)
	privKey, publicKey := crypto.PrivKeyFromBytes(crypto.S256(), privKeyBytes)
	var acc = crypto.Account{
		PrivateKey: *privKey,
		PublicKey:  *publicKey,
	}
	acc.Address, err = common.NewAddressWithId(common.PubKeyHashAddrID,
		common.Hash160(acc.PublicKey.SerializeCompressed()))

	txC := mempool.Config{
		Policy: mempool.Policy{
			MaxOrphanTxs:      DefaultMaxOrphanTransactions,
			MaxOrphanTxSize:   DefaultMaxOrphanTxSize,
			MinRelayTxPrice:   chaincfg.Cfg.MinTxPrice,
			MaxTxVersion:      2,
		},
		FetchUtxoView:          chain.FetchUtxoView,
		Chain:                  chain,
		BestHeight:             func() int32 { return chain.BestSnapshot().Height },
		MedianTimePast:         func() int64 { return chain.BestSnapshot().TimeStamp },
		CheckTransactionInputs: blockchain.CheckTransactionInputs,
	}
	txMemPool := mempool.New(&txC)
	sigMemPool := mempool.NewSigPool()

	//syncManager, err := netsync.New(&netsync.Config{
	//	PeerNotifier:       nil,
	//	Chain:              chain,
	//	TxMemPool:          txMemPool,
	//	SigMemPool:         sigMemPool,
	//	ChainParams:        &chaincfg.MainNetParams,
	//	DisableCheckpoints: chaincfg.Cfg.DisableCheckpoints,
	//	MaxPeers:           chaincfg.Cfg.MaxPeers,
	//	//FeeEstimator:       s.feeEstimator,
	//	Account: &acc,
	//	BroadcastMessage: func(msg protos.Message, exclPeers ...interface{}) {
	//		return
	//	},
	//})
	//if err != nil {
	//	return nil, teardownFunc, err
	//}

	policy := mining.Policy{
		TxMinPrice: 0,
	}
	blockTemplateGenerator := mining.NewBlkTmplGenerator(&policy, txMemPool, sigMemPool, chain)

	consensusConfig := params.Config{
		BlockTemplateGenerator: blockTemplateGenerator,
		ProcessBlock:           ProcessBlock,
		IsCurrent:              chain.IsCurrent,
		ProcessSig:             sigMemPool.ProcessSig,
		Chain:                  chain,
		GasFloor:               160000000,
		GasCeil:                160000000,
		RoundManager:           roundManager,
		Account:                &acc,
	}

	return &consensusConfig, teardownFunc, nil
}

type FakeBtcClient struct {
	minerInfo []minersync.GetBitcoinBlockMinerInfoResult
	chainInfo *minersync.GetBlockChainInfoResult
}

func NewFakeBtcClient(height, count int32) *FakeBtcClient {
	minerInfo := make([]minersync.GetBitcoinBlockMinerInfoResult, 0, count)
	c := &FakeBtcClient{
		minerInfo: minerInfo,
	}
	c.push(height, count)
	return c
}

func (c *FakeBtcClient) Push(count int32) {
	height := c.minerInfo[len(c.minerInfo)-1].Height + 1
	c.push(height, count)
}

func (c *FakeBtcClient) push(height, count int32) {

	for i := int32(0); i < count; i++ {
		c.minerInfo = append(c.minerInfo, minersync.GetBitcoinBlockMinerInfoResult{
			Height:  height + i,
			Pool:    "",
			Hash:    strconv.Itoa(int(height + i)),
			Address: "11",
			Time:    600 * uint32(height+i),
		})
	}

	c.chainInfo = &minersync.GetBlockChainInfoResult{
		Blocks: height + count - 1,
	}
}

func (c *FakeBtcClient) UpInterval() {
	interval := uint32(300)
	add := uint32(30)
	c.minerInfo[0].Time = 0
	for i := int(chaincfg.ActiveNetParams.CollectInterval); i < len(c.minerInfo); i = i + int(chaincfg.ActiveNetParams.BtcBlocksPerRound) {
		for j := 0;j < int(chaincfg.ActiveNetParams.BtcBlocksPerRound);j++ {
			c.minerInfo[i+j].Time = c.minerInfo[i-1].Time + interval
		}
		interval += add
	}
}

func (c *FakeBtcClient) GetBitcoinMinerInfo(result interface{}, height, count int32) error {
	begin := c.minerInfo[0].Height
	last := c.minerInfo[len(c.minerInfo)-1].Height
	if height < begin || height+count-1 > last {
		return errors.New("miners not find")
	}
	miners := result.(*[]minersync.GetBitcoinBlockMinerInfoResult)
	*miners = c.minerInfo[height-begin : height-begin+count]
	return nil
}

func (c *FakeBtcClient) GetBitcoinBlockChainInfo(result interface{}) error {
	chaininfo := result.(*minersync.GetBlockChainInfoResult)
	*chaininfo = *c.chainInfo
	return nil
}
