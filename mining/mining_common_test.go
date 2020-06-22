// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package mining

import (
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/blockchain/syscontract"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/common/address"
	"github.com/AsimovNetwork/asimov/common/hexutil"
	"github.com/AsimovNetwork/asimov/consensus/satoshiplus/minersync"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/database/dbdriver"
	"github.com/AsimovNetwork/asimov/database/dbimpl/ethdb"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/rpcs/rpc"
	"github.com/AsimovNetwork/asimov/txscript"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	testDbType          = "ffldb"
)

var (
	defaultHomeDir = asiutil.AppDataDir("asimovd", false)
)

/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
type HistoryFlag byte

var (
	roundBucketName = []byte("roundIdx")
)

type RoundValidators struct {
	blockHash  common.Hash
	validators []*common.Address
	weightmap  map[common.Address]uint16
}

type RoundMinerInfo struct {
	round      uint32
	middleTime uint32
	miners     map[string]float64
	mapping    map[string]*ainterface.ValidatorInfo
}

type RoundManager struct {
	minerCollector *minersync.BtcPowerCollector
	db             database.Transactor

	// round cache
	vLock                sync.RWMutex
	roundValidatorsCache []*RoundValidators
	historyValidators map[common.Address]HistoryFlag

	rmLock  sync.RWMutex
	rmCache []*RoundMinerInfo
}

func (m *RoundManager) HasValidator(validator common.Address) bool {
	return false
}

func (m *RoundManager) GetValidators(blockHash common.Hash, round uint32, fn ainterface.GetValidatorsCallBack) (
	[]*common.Address, map[common.Address]uint16, error) {
	privateKey := "0xd0f0461b7b4d26cf370e6c73b58ef7fa26e8e30853a8cee901ed42cf0879cb6e"
	privKeyBytes, _ := hexutil.Decode(privateKey)
	_, publicKey := crypto.PrivKeyFromBytes(crypto.S256(), privKeyBytes)
	pkaddr, _ := address.NewAddressPubKey(publicKey.SerializeCompressed())
	addr := pkaddr.AddressPubKeyHash()
	validators := make([]*common.Address, 0, chaincfg.ActiveNetParams.RoundSize)
	for i := uint16(0); i < chaincfg.ActiveNetParams.RoundSize; i++ {
		validators = append(validators, addr)
	}
	weightmap := make(map[common.Address]uint16)
	weightmap[*addr] = chaincfg.ActiveNetParams.RoundSize
	return validators, weightmap, nil
}

func (m *RoundManager) setValidators(blockHash common.Hash, validators []*common.Address) map[common.Address]uint16 {
	return nil
}

func (m *RoundManager) getRoundMinerFromCache(round uint32) *RoundMinerInfo {
	return nil
}

func (m *RoundManager) setRoundMiner(roundMiner *RoundMinerInfo) {
}

func (m *RoundManager) getMinersInfo(round uint32) (*RoundMinerInfo, error) {
	return nil, nil
}

func (m *RoundManager) GetMinersByRound(round uint32) (map[string]float64, error) {
	return nil, nil
}

func (m *RoundManager) GetHsMappingByRound(round uint32) (map[string]*ainterface.ValidatorInfo, error) {
	return nil, nil
}

func (m *RoundManager) Init(round uint32, db database.Transactor, c ainterface.IBtcClient) error {
	return nil
}

func (m *RoundManager) Start() {
}

func (m *RoundManager) Halt() {
}

func (m *RoundManager) GetContract() common.Address {
	return common.ConsensusSatoshiPlus
}

func NewRoundManager() *RoundManager {
	return &RoundManager{}
}

func (m *RoundManager) GetRoundInterval(round int64) int64 {
	return 5 * 120
}

func (m *RoundManager) GetNextRound(round *ainterface.Round) (*ainterface.Round, error) {
	return nil, nil
}

var _ ainterface.IRoundManager = (*RoundManager)(nil)
/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
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
			Time:    600 * uint32(i),
		})
	}

	c.chainInfo = &minersync.GetBlockChainInfoResult{
		Blocks: height + count - 1,
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

/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

type fakeIn struct {
	account     *crypto.Account
	amount      int64
	asset       *protos.Asset
	index       uint32
	coinbase    bool
	blockHeight int32
	blockHash   common.Hash
}

type fakeOut struct {
	addr   *common.Address
	amount int64
	asset  *protos.Asset
}

func createFakeTx(vin []*fakeIn, vout []*fakeOut, global_view *txo.UtxoViewpoint) *asiutil.Tx {
	txMsg := protos.NewMsgTx(protos.TxVersion)

	if global_view == nil {
		global_view = txo.NewUtxoViewpoint()
	}
	for _, v := range vin {
		inaddr := v.account.Address
		prePkScript, _ := txscript.PayToAddrScript(inaddr)
		outpoint := protos.OutPoint{v.blockHash, v.index}
		txin := protos.NewTxIn(&outpoint, nil)
		txMsg.AddTxIn(txin)

		entry := txo.NewUtxoEntry(v.amount, prePkScript, v.blockHeight, v.coinbase, v.asset, nil)
		global_view.AddEntry(outpoint, entry)
	}

	for _, v := range vout {
		pkScript, _ := txscript.PayToAddrScript(v.addr)
		txout := protos.NewTxOut(v.amount, pkScript, *v.asset)
		txMsg.AddTxOut(txout)
	}

	for i, v := range vin {
		entry := global_view.LookupEntry(protos.OutPoint{v.blockHash, v.index})
		// Sign the txMsg transaction.
		lookupKey := func(a common.IAddress) (*crypto.PrivateKey, bool, error) {
			return &v.account.PrivateKey, true, nil
		}
		sigScript, _ := txscript.SignTxOutput(txMsg, i, entry.PkScript(), txscript.SigHashAll, txscript.KeyClosure(lookupKey), nil, nil)
		txMsg.TxIn[i].SignatureScript = sigScript
	}

	tx := asiutil.NewTx(txMsg)
	return tx
}

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

func newFakeChain(paramstmp *chaincfg.Params) (*blockchain.BlockChain, func(), error) {
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
		return nil, nil, nil
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
		return nil, nil, err
	}

	// Load StateDB
	stateDB, stateDbErr := ethdb.NewLDBDatabase(cfg.StateDir, 768, 1024)
	if stateDbErr != nil {
		teardown()
		log.Errorf("%v", stateDbErr)
		return nil, nil, stateDbErr
	}

	// Create a btc rpc client
	btcClient := NewFakeBtcClient(chaincfg.ActiveNetParams.CollectHeight,
		chaincfg.ActiveNetParams.CollectInterval+int32(chaincfg.ActiveNetParams.BtcBlocksPerRound)*24)

	// Create a round manager.
	roundManger := NewRoundManager()

	// Create a contract manager.
	contractManager := syscontract.NewContractManager()

	dir, err := os.Getwd()
	var loadBlkErr error
	var genesisBlock *protos.MsgBlock
	if chaincfg.ActiveNetParams.Params.Net == common.TestNet {
		genesisBlock, loadBlkErr = asiutil.LoadBlockFromFile("../genesisbin/testnet.block")
	} else if chaincfg.ActiveNetParams.Params.Net == common.DevelopNet {
		genesisBlock, loadBlkErr = asiutil.LoadBlockFromFile("../genesisbin/devnet.block")
	} else {
		genesisBlock, loadBlkErr = asiutil.LoadBlockFromFile("../genesisbin/mainnet.block")
	}
	if loadBlkErr != nil {
		strErr := "Load genesis block error, " + err.Error() + dir
		return nil, nil, errors.New(strErr)
	}
	genesisHash := asiutil.NewBlock(genesisBlock).Hash()
	if *chaincfg.ActiveNetParams.GenesisHash != *genesisHash {
		strErr := fmt.Sprintf("Load genesis block genesis hash mismatch expected %s, but %s",
			chaincfg.ActiveNetParams.GenesisHash.String(), genesisHash.String())
		return nil, nil, errors.New(strErr)
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
		//Account:       &acc,
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
		return nil, nil, err
	}

	teardown = func() {
		db.Close()
		stateDB.Close()
		os.RemoveAll(cfg.DataDir)
		os.RemoveAll(cfg.LogDir)
		os.RemoveAll(cfg.StateDir)
	}

	chaincfg.Cfg = cfg
	return chain, teardown, nil
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

	// The database name is based on the database type.
	dbPath := blockDbPath(cfg.DataDir, testDbType)

	//在测试代码中要连接测试网络
	//db, err := database.Open(cfg.DbType, dbPath, configuration.ActiveNetParams.Net)
	db, err := dbdriver.Open(testDbType, dbPath, chaincfg.ActiveNetParams.Params.Net)
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
		db, err = dbdriver.Create(testDbType, dbPath, chaincfg.ActiveNetParams.Params.Net)
		if err != nil {
			return nil, err
		}
	}

	return db, nil
}

type fakeTxSource struct {
	pool map[common.Hash]*TxDesc
}

func (fts *fakeTxSource) clear() {
	fts.pool = make(map[common.Hash]*TxDesc)
}

func (fts *fakeTxSource) push(tx *TxDesc) {
	fts.pool[*tx.Tx.Hash()] = tx
}

// MiningDescs returns a slice of mining descriptors for all the
// transactions in the source pool.
func (fts *fakeTxSource) TxDescs() TxDescList {
	descs := make(TxDescList, len(fts.pool))
	i := 0
	for _, desc := range fts.pool {
		descs[i] = desc
		i++
	}
	return descs
}

func (fts *fakeTxSource) UpdateForbiddenTxs(txHashes []*common.Hash, height int64) {
}

type fakeSigSource struct {
	pool []*asiutil.BlockSign
}

func (fss *fakeSigSource) push(sign *asiutil.BlockSign) {
	fss.pool = append(fss.pool, sign)
}

func (fss *fakeSigSource) MiningDescs(height int32) []*asiutil.BlockSign {
	descs := make([]*asiutil.BlockSign, 0, common.BlockSignDepth)
	for _, v := range fss.pool {
		if v.MsgSign.BlockHeight >= height-common.BlockSignDepth {
			descs = append(descs, v)
		}
	}
	return descs
}
