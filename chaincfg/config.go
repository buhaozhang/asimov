// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package chaincfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/rpcs/rpc"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/common"
	fnet "github.com/AsimovNetwork/asimov/common/net"
	"github.com/AsimovNetwork/asimov/logger"
)

const (
	DefaultConfigFilename       = "asimovd.conf"
	DefaultGenesisFilename      = "genesis.json"
	DefaultDataDirname          = "data"
	DefaultLogLevel             = "info"
	DefaultLogDirname           = "logs"
	DefaultStateDirname         = "state"
	DefaultMaxPeers             = 125
	DefaultBanDuration          = time.Hour * 24
	DefaultBanThreshold         = 100
	DefaultMaxRPCClients        = 10
	DefaultMaxRPCWebsockets     = 25
	DefaultMaxRPCConcurrentReqs = 20
	// DefaultMinTxPrice is the minimum price in xing that is required
	// for a transaction to be treated as free for relay and mining
	// purposes.  It is also used to help determine if a transaction is
	// considered as a base for calculating minimum required fees
	// for larger transactions.
	DefaultMinTxPrice            = 0.01
	DefaultMaxOrphanTransactions = 100
	DefaultMaxOrphanTxSize       = 100000
	DefaultAutoSignUpGasLimit    = 300000
	DefaultMergeLimit            = 10

	// DefaultMaxTimeOffsetSeconds is the maximum number of seconds a block
	// time is allowed to be ahead of the current time.
	DefaultMaxTimeOffsetSeconds = 30

	DefaultHTTPEndPoint = "127.0.0.1:8545" // Default endpoint interface for the HTTP RPC server
	DefaultWSEndPoint   = "127.0.0.1:8546" // Default endpoint interface for the websocket RPC server

	// DefaultBlockProductedTimeOut is the default value for the policy
	// `BlockProductedTimeOut`. There are four steps which take the main
	// time of a block interval:
	// 1. producing a block (*)
	// 2. process block in the miner node
	// 3. broadcast the block
	// 4. process block in other nodes
	DefaultBlockProductedTimeOut = 0.5

	// DefaultTxConnectTimeOut is the default value for the policy
	// `TxConnectTimeOut`. The whole progress of producing a block contains:
	// 1. fetch txs from mempool & order them
	// 2. validating utxos (*UtxoValidateTimeOut)
	// 3. connect txs (*DefaultTxConnectTimeOut)
	// 4. create coinbase (maybe coinbase tx need execute vm)
	// 5. commit state db.
	DefaultTxConnectTimeOut = 0.75

	// refer to doc of DefaultTxConnectTimeOut
	DefaultUtxoValidateTimeOut = 0.25

	//DefaultSyncBlockTime is the default time cost for synchronizing a block
	DefaultSyncBlockTime = 1000
)

var (
	HomeDir            = asiutil.HomeDir()
	DefaultAppDataDir  = cleanAndExpandPath(asiutil.AppDataDir("asimovd", false))
	DefaultConfigFile  = filepath.Join(DefaultAppDataDir, DefaultConfigFilename)
	DefaultGenesisPath = DefaultAppDataDir
	DefaultDataDir     = filepath.Join(DefaultAppDataDir, DefaultDataDirname)
	DefaultRPCKeyFile  = filepath.Join(DefaultAppDataDir, "rpc.key")
	DefaultRPCCertFile = filepath.Join(DefaultAppDataDir, "rpc.cert")
	DefaultLogDir      = filepath.Join(DefaultAppDataDir, DefaultLogDirname)
	DefaultStateDir    = filepath.Join(DefaultAppDataDir, DefaultStateDirname)
	Cfg                *FConfig

	DefaultHttpModules      = []string{"net", "web3"}
	DefaultHTTPVirtualHosts = []string{"localhost"}
	DefaultWSOrigins        = []string{"*"}
	DefaultWSModules        = []string{"net", "web3"}
)

// runServiceCommand is only set to a real function on Windows.  It is used
// to parse and execute service commands specified via the -s flag.
var runServiceCommand func(string) error

// FConfig defines the configuration options for asimovd.
//
// See loadConfig for details on the configuration load process.
type FConfig struct {
	ShowVersion          bool          `short:"V" long:"Version" description:"Display Version information and exit"`
	ConfigFile           string        `short:"C" long:"configfile" description:"Path to configuration file"`
	DataDir              string        `short:"b" long:"datadir" description:"Directory to store data"`
	LogDir               string        `long:"logdir" description:"Directory to logger output."`
	StateDir             string        `long:"statedir" description:"Directory to state data."`
	AddPeers             []string      `short:"a" long:"addpeer" description:"Add a peer to connect with at startup"`
	ConnectPeers         []string      `long:"connect" description:"Connect only to the specified peers at startup"`
	DisableListen        bool          `long:"nolisten" description:"Disable listening for incoming connections -- NOTE: Listening is automatically disabled if the --connect or --proxy options are used without also specifying listen interfaces via --listen"`
	Listeners            []string      `long:"listen" description:"Add an interface/port to listen for connections (default all interfaces port: 8777, testnet: 18721)"`
	MaxPeers             int           `long:"maxpeers" description:"Max number of inbound and outbound peers"`
	DisableBanning       bool          `long:"nobanning" description:"Disable banning of misbehaving peers"`
	BanDuration          time.Duration `long:"banduration" description:"How long to ban misbehaving peers.  Valid time units are {s, m, h}.  Minimum 1 second"`
	BanThreshold         uint32        `long:"banthreshold" description:"Maximum allowed ban score before disconnecting and banning misbehaving peers."`
	WhitelistsArr        []string      `long:"whitelist" description:"Add an IP network or IP that will not be banned. (eg. 192.168.1.0/24 or ::1)"`
	AgentBlacklist       []string      `long:"agentblacklist" description:"A comma separated list of user-agent substrings which will cause btcd to reject any peers whose user-agent contains any of the blacklisted substrings."`
	AgentWhitelist       []string      `long:"agentwhitelist" description:"A comma separated list of user-agent substrings which will cause btcd to require all peers' user-agents to contain one of the whitelisted substrings. The blacklist is applied before the blacklist, and an empty whitelist will allow all agents that do not fail the blacklist."`
	RPCUser              string        `short:"u" long:"rpcuser" description:"Username for RPC connections"`
	RPCPass              string        `short:"P" long:"rpcpass" default-mask:"-" description:"Password for RPC connections"`
	RPCLimitUser         string        `long:"rpclimituser" description:"Username for limited RPC connections"`
	RPCLimitPass         string        `long:"rpclimitpass" default-mask:"-" description:"Password for limited RPC connections"`
	RPCCert              string        `long:"rpccert" description:"File containing the certificate file"`
	RPCKey               string        `long:"rpckey" description:"File containing the certificate key"`
	RPCMaxClients        int           `long:"rpcmaxclients" description:"Max number of RPC clients for standard connections"`
	RPCMaxWebsockets     int           `long:"rpcmaxwebsockets" description:"Max number of RPC websocket connections"`
	RPCMaxConcurrentReqs int           `long:"rpcmaxconcurrentreqs" description:"Max number of concurrent RPC requests that may be processed concurrently"`
	DisableRPC           bool          `long:"norpc" description:"Disable built-in RPC server -- NOTE: The RPC server is disabled by default if no rpcuser/rpcpass or rpclimituser/rpclimitpass is specified"`
	DisableTLS           bool          `long:"notls" description:"Disable TLS for the RPC server -- NOTE: This is only allowed if the RPC server is bound to localhost"`
	DisableDNSSeed       bool          `long:"nodnsseed" description:"Disable DNS seeding for peers"`
	ExternalIPs          []string      `long:"externalip" description:"Add an ip to the list of local addresses we claim to listen on to peers"`
	Proxy                string        `long:"proxy" description:"Connect via SOCKS5 proxy (eg. 127.0.0.1:9050)"`
	ProxyUser            string        `long:"proxyuser" description:"Username for proxy server"`
	ProxyPass            string        `long:"proxypass" default-mask:"-" description:"Password for proxy server"`
	OnionProxy           string        `long:"onion" description:"Connect to tor hidden services via SOCKS5 proxy (eg. 127.0.0.1:9050)"`
	OnionProxyUser       string        `long:"onionuser" description:"Username for onion proxy server"`
	OnionProxyPass       string        `long:"onionpass" default-mask:"-" description:"Password for onion proxy server"`
	NoOnion              bool          `long:"noonion" description:"Disable connecting to tor hidden services"`
	TorIsolation         bool          `long:"torisolation" description:"Enable Tor stream isolation by randomizing user credentials for each connection."`
	TestNet              bool          `long:"testnet" description:"Use the test network"`
	RejectReplacement    bool          `long:"rejectreplacement" description:"Reject transactions that attempt to replace existing transactions within the mempool through the Replace-By-Price (RBP) signaling policy."`
	DevelopNet           bool          `long:"devnet" description:"Use the develop network"`
	ChainId              uint64        `long:"chainid" description:"Use distinguish different chain, the main chain occupy zero, each subchain take a positive integer"`
	AddCheckpointsArr    []string      `long:"addcheckpoint" description:"Add a custom checkpoint.  Format: '<height>:<hash>'"`
	DisableCheckpoints   bool          `long:"nocheckpoints" description:"Disable built-in checkpoints.  Don't do this unless you know what you're doing."`
	Profile              string        `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65536"`
	CPUProfile           string        `long:"cpuprofile" description:"Write CPU profile to the specified file"`
	DebugLevel           string        `short:"d" long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the logger level for individual subsystems -- Use show to list available subsystems"`
	Upnp                 bool          `long:"upnp" description:"Use UPnP to map our listening port outside of NAT"`
	MinTxPrice           float64       `long:"mintxprice" description:"The minimum transaction price, it should not less than 0.01"`
	BlkProductedTimeOut  float64       `long:"blkproductedtimeout" description:"the value for the policy BlockProductedTimeOut,the value must be in range of (0, 1)"`
	TxConnectTimeOut     float64       `long:"txconnecttimeout" description:"the value for the policy TxConnectTimeOut,the value must be in range of (0, 1)"`
	UtxoValidateTimeOut  float64       `long:"utxovalidatetimeout" description:"the time for validating utxos,the value must be in range of (0, 1)"`
	BlockSyncTime        float64       `long:"blocksynctime" description:"the time for synchronizing a block,the time unit is millisecond"`
	DisableBlockEarly    bool          `long:"disableblockearly" description:"Generate blocks early when the last block is received and consensus is satoshi"`
	MaxOrphanTxs         int           `long:"maxorphantx" description:"Max number of orphan transactions to keep in memory"`
	MaxOrphanTxSize      int           `long:"maxorphantxsize" description:"Max size of an orphan transaction to allow in memory"`
	Consensustype        string        `long:"consensustype" description:"Consensus type which the server uses"`
	Privatekey           string        `long:"privatekey" description:"Add the private key which is used to assign block header for generated blocks"`
	UserAgentComments    []string      `long:"uacomment" description:"Comment to add to the user agent -- See BIP 14 for more information."`
	NoPeerBloomFilters   bool          `long:"nopeerbloomfilters" description:"Disable bloom filtering support"`
	NoCFilters           bool          `long:"nocfilters" description:"Disable committed filtering (CF) support"`
	DropCfIndex          bool          `long:"dropcfindex" description:"Deletes the index used for committed filtering (CF) support from the database on start up and then exits."`
	BlocksOnly           bool          `long:"blocksonly" description:"Do not accept transactions from remote peers."`
	EmptyRound           bool          `long:"emptyround" description:"Allow round contains no blocks."`
	DropTxIndex          bool          `long:"droptxindex" description:"Deletes the hash-based transaction index from the database on start up and then exits."`
	DropAddrIndex        bool          `long:"dropaddrindex" description:"Deletes the address-based transaction index from the database on start up and then exits."`
	MaxTimeOffset        int           `long:"maxtimeoffset" description:"The maximum number of seconds a block time is allowed to be ahead of the current time, it is allowd to take [5-30]."`
	MergeLimit           int           `long:"mergeLimit" description:"It is a miner strategy that miner can merge its utxo and push into block."`
	AddCheckpoints       []Checkpoint
	Whitelists           []*net.IPNet

	EwasmOptions string `long:"vm.ewasm" description:"Ewasm options"`
	EvmOptions   string `long:"vm.evm" description:"Evm options"`

	HTTPEndpoint     string   `long:"httpendpoint" description:"Http endpoint to listen for HTTP RPC connections (default port: 127.0.0.1:8545)"`
	HTTPModules      []string `long:"httpmodule" description:"HTTP RPC modules supported by current node (default [\"net\", \"web3\"])"`
	HTTPCors         []string `long:"httpcor" description:"HTTPCors is the Cross-Origin Resource Sharing header to send to requesting"`
	HTTPVirtualHosts []string `long:"httpvirtualhost" description:"HTTPVirtualHosts is the list of virtual host which are allowed on incoming requests (default [\"localhost\"])"`
	HTTPTimeouts     rpc.HTTPTimeouts
	WSEndpoint       string   `long:"wsendpoint" description:"Ws endpoint to listen for Websocket connections (default port: 127.0.0.1:8546)"`
	WSOrigins        []string `long:"wsorigins" description:"Ws origins is whitelist of ws (default *)"`
	WSModules        []string `long:"wsmodule" description:"WebSocket modules supported by current node (default [\"net\", \"web3\"])"`

	AddBtc    []string `long:"addbtc" description:"Add a param to call btc server.  Format: '<ip>:<port>:<rpcuser>:<rpcpassword>'"`
	BtcParams []*BitcoinParams

	GenesisPath    string `long:"genesispath" description:"Path of genesis files"`
	GenesisBlockFile string
	GenesisParamFile string
}

// serviceOptions defines the configuration options for the daemon as a service on
// Windows.
type serviceOptions struct {
	ServiceCommand string `short:"s" long:"service" description:"Service command {install, remove, start, stop}"`
}

// cleanAndExpandPath expands environment variables and leading ~ in the
// passed path, cleans the result, and returns it.
func cleanAndExpandPath(path string) string {
	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		homeDir := filepath.Clean(HomeDir)
		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but they variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

// ParseAndSetDebugLevels attempts to parse the specified debug level and set
// the levels accordingly.  An appropriate error is returned if anything is
// invalid.
func ParseAndSetDebugLevels(debugLevel string) error {
	// When the specified string doesn't have any delimters, treat it as
	// the logger level for all subsystems.
	if !strings.Contains(debugLevel, ",") && !strings.Contains(debugLevel, "=") {
		// Validate debug logger level.
		if !logger.ValidLogLevel(debugLevel) {
			str := "the specified debug level [%v] is invalid"
			return fmt.Errorf(str, debugLevel)
		}

		// Change the logging level for all subsystems.
		logger.SetLogLevels(debugLevel)
		return nil
	}

	// Split the specified string into subsystem/level pairs while detecting
	// issues and update the logger levels accordingly.
	for _, logLevelPair := range strings.Split(debugLevel, ",") {
		if !strings.Contains(logLevelPair, "=") {
			str := "the specified debug level contains an invalid " +
				"subsystem/level pair [%v]"
			return fmt.Errorf(str, logLevelPair)
		}

		// Extract the specified subsystem and logger level.
		fields := strings.Split(logLevelPair, "=")
		subsysID, logLevel := fields[0], fields[1]

		// Validate logger level.
		if !logger.ValidLogLevel(logLevel) {
			str := "the specified debug level [%v] is invalid"
			return fmt.Errorf(str, logLevel)
		}

		// Validate subsystem and set log level.
		if !logger.SetLogLevel(subsysID, logLevel) {
			str := "the specified subsystem [%v] is invalid -- " +
				"supported subsytems %v"
			return fmt.Errorf(str, subsysID, logger.SupportedSubsystems())
		}
	}

	return nil
}

// newCheckpointFromStr parses checkpoints in the '<height>:<hash>' format.
func newCheckpointFromStr(checkpoint string) (Checkpoint, error) {
	parts := strings.Split(checkpoint, ":")
	if len(parts) != 2 {
		return Checkpoint{}, fmt.Errorf("unable to parse "+
			"checkpoint %q -- use the syntax <height>:<hash>",
			checkpoint)
	}

	height, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("unable to parse "+
			"checkpoint %q due to malformed height", checkpoint)
	}

	nhlen := len(parts[1])
	if nhlen == 0 {
		return Checkpoint{}, fmt.Errorf("unable to parse "+
			"checkpoint %q due to missing hash", checkpoint)
	}

	bytes := common.FromHex(parts[1])
	if len(bytes) != common.HashLength {
		return Checkpoint{}, fmt.Errorf("invalid hash length of %v, want %v", nhlen,
			common.HashLength)
	}

	hash := common.BytesToHash(bytes)

	return Checkpoint{
		Height: int32(height),
		Hash:   &hash,
	}, nil
}

// parseCheckpoints checks the checkpoint strings for valid syntax
// ('<height>:<hash>') and parses them to chaincfg.Checkpoint instances.
func parseCheckpoints(checkpointStrings []string) ([]Checkpoint, error) {
	if len(checkpointStrings) == 0 {
		return nil, nil
	}
	checkpoints := make([]Checkpoint, len(checkpointStrings))
	for i, cpString := range checkpointStrings {
		checkpoint, err := newCheckpointFromStr(cpString)
		if err != nil {
			return nil, err
		}
		checkpoints[i] = checkpoint
	}
	return checkpoints, nil
}

// filesExists reports whether the named file or directory exists.
func FileExists(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// newConfigParser returns a new command line flags parser.
func newConfigParser(cfg *FConfig, so *serviceOptions, options flags.Options) *flags.Parser {
	parser := flags.NewParser(cfg, options)
	if runtime.GOOS == "windows" {
		parser.AddGroup("Service Options", "Service Options", so)
	}
	return parser
}

// loadConfig initializes and parses the FConfig using a FConfig file and command
// line options.
//
// The configuration proceeds as follows:
// 	1) Start with a default FConfig with sane settings
// 	2) Pre-parse the command line to check for an alternative FConfig file
// 	3) Load configuration file overwriting defaults with any specified options
// 	4) Parse CLI options and overwrite/add any specified options
//
// The above results in btcd functioning properly without any FConfig settings
// while still allowing the user to override settings with FConfig files and
// command line options.  Command line options always take precedence.
func LoadConfig() (*FConfig, []string, error) {
	// Default FConfig.
	cfg := FConfig{
		ConfigFile:           DefaultConfigFile,
		GenesisPath:          DefaultGenesisPath,
		DebugLevel:           DefaultLogLevel,
		MaxPeers:             DefaultMaxPeers,
		BanDuration:          DefaultBanDuration,
		BanThreshold:         DefaultBanThreshold,
		RPCMaxClients:        DefaultMaxRPCClients,
		RPCMaxWebsockets:     DefaultMaxRPCWebsockets,
		RPCMaxConcurrentReqs: DefaultMaxRPCConcurrentReqs,
		DataDir:              DefaultDataDir,
		LogDir:               DefaultLogDir,
		StateDir:             DefaultStateDir,
		RPCKey:               DefaultRPCKeyFile,
		RPCCert:              DefaultRPCCertFile,
		MinTxPrice:           DefaultMinTxPrice,
		BlkProductedTimeOut:  DefaultBlockProductedTimeOut,
		TxConnectTimeOut:     DefaultTxConnectTimeOut,
		UtxoValidateTimeOut:  DefaultUtxoValidateTimeOut,
		BlockSyncTime:        DefaultSyncBlockTime,
		MaxOrphanTxs:         DefaultMaxOrphanTransactions,
		MaxOrphanTxSize:      DefaultMaxOrphanTxSize,
		EmptyRound:           false,
		MaxTimeOffset:        DefaultMaxTimeOffsetSeconds,
		MergeLimit:           DefaultMergeLimit,

		HTTPEndpoint:     DefaultHTTPEndPoint,
		HTTPModules:      DefaultHttpModules,
		HTTPVirtualHosts: DefaultHTTPVirtualHosts,
		HTTPTimeouts:     rpc.DefaultHTTPTimeouts,
		WSEndpoint:       DefaultWSEndPoint,
		WSOrigins:        DefaultWSOrigins,
		WSModules:        DefaultWSModules,
	}

	// Service options which are only added on Windows.
	serviceOpts := serviceOptions{}

	// Pre-parse the command line options to see if an alternative FConfig
	// file or the Version flag was specified.  Any errors aside from the
	// help message error can be ignored here since they will be caught by
	// the final parse below.
	preCfg := cfg
	preParser := newConfigParser(&preCfg, &serviceOpts, flags.HelpFlag)
	_, err := preParser.Parse()
	if err != nil {
		if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
	}

	// Show the Version and exit if the Version flag was specified.
	appName := filepath.Base(os.Args[0])
	appName = strings.TrimSuffix(appName, filepath.Ext(appName))
	usageMessage := fmt.Sprintf("Use %s -h to show usage", appName)
	if preCfg.ShowVersion {
		fmt.Println(appName, "Version", Version())
		os.Exit(0)
	}

	// Perform service command and exit if specified.  Invalid service
	// commands show an appropriate error.  Only runs on Windows since
	// the runServiceCommand function will be nil when not on Windows.
	if serviceOpts.ServiceCommand != "" && runServiceCommand != nil {
		err := runServiceCommand(serviceOpts.ServiceCommand)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(0)
	}

	// Load additional FConfig from file.
	var configFileError error
	parser := newConfigParser(&cfg, &serviceOpts, flags.Default)
	if _, err := os.Stat(preCfg.ConfigFile); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Check the config file %s: %v\n",
			preCfg.ConfigFile, err)
		return nil, nil, err
	}

	err = flags.NewIniParser(parser).ParseFile(preCfg.ConfigFile)
	if err != nil {
		if _, ok := err.(*os.PathError); !ok {
			_, _ = fmt.Fprintf(os.Stderr, "Error parsing FConfig "+
				"file: %v\n", err)
			_, _ = fmt.Fprintln(os.Stderr, usageMessage)
			return nil, nil, err
		}
		configFileError = err
	}

	// Parse command line options again to ensure they take precedence.
	remainingArgs, err := parser.Parse()
	if err != nil {
		if e, ok := err.(*flags.Error); !ok || e.Type != flags.ErrHelp {
			fmt.Fprintln(os.Stderr, usageMessage)
		}
		return nil, nil, err
	}

	// Create the home directory if it doesn't already exist.
	funcName := "loadConfig"
	err = os.MkdirAll(DefaultAppDataDir, 0700)
	if err != nil {
		// Show a nicer error message if it's because a symlink is
		// linked to a directory that does not exist (probably because
		// it's not mounted).
		if e, ok := err.(*os.PathError); ok && os.IsExist(err) {
			if link, lerr := os.Readlink(e.Path); lerr == nil {
				str := "is symlink %s -> %s mounted"
				err = fmt.Errorf(str, e.Path, link)
			}
		}

		str := "%s: Failed to create home directory: %v"
		err := fmt.Errorf(str, funcName, err)
		fmt.Fprintln(os.Stderr, err)
		return nil, nil, err
	}

	// Multiple networks can't be selected simultaneously.
	numNets := 0
	genesisBlock := "mainnet.block"
	// Count number of network flags passed; assign active network params
	// while we're at it
	if cfg.TestNet {
		numNets++
		ActiveNetParams = &testNetParams
		genesisBlock = "testnet.block"
	}
	if cfg.DevelopNet {
		numNets++
		// Also disable dns seeding on the simulation test network.
		ActiveNetParams = &developNetParams
		cfg.DisableDNSSeed = true
		genesisBlock = "devnet.block"
	}
	if numNets > 1 {
		str := "%s: The testnet, devnet, regtest, and simnet params " +
			"can't be used together -- choose one of the four"
		err := fmt.Errorf(str, funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	} else if numNets == 0 {
		cfg.EmptyRound = false
	}
	cfg.GenesisBlockFile = filepath.Join(cfg.GenesisPath, genesisBlock)
	cfg.GenesisParamFile = filepath.Join(cfg.GenesisPath, DefaultGenesisFilename)

	consensus := common.GetConsensus(cfg.Consensustype)
	if consensus < 0 {
		str := "%s: The consensus type is not support"
		err := fmt.Errorf(str, funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Append the network type to the data directory so it is "namespaced"
	// per network.  In addition to the block database, there are other
	// pieces of data that are saved to disk such as address manager state.
	// All data is specific to a network, so namespacing the data directory
	// means each individual piece of serialized data does not have to
	// worry about changing names per network and such.
	cfg.DataDir = cleanAndExpandPath(cfg.DataDir)
	cfg.DataDir = filepath.Join(cfg.DataDir, ActiveNetParams.Name())

	// Append the network type to the logger directory so it is "namespaced"
	// per network in the same fashion as the data directory.
	cfg.LogDir = cleanAndExpandPath(cfg.LogDir)
	cfg.LogDir = filepath.Join(cfg.LogDir, ActiveNetParams.Name())

	cfg.StateDir = cleanAndExpandPath(cfg.StateDir)
	cfg.StateDir = filepath.Join(cfg.StateDir, ActiveNetParams.Name())

	// Special show command to list supported subsystems and exit.
	if cfg.DebugLevel == "show" {
		fmt.Println("Supported subsystems", logger.SupportedSubsystems())
		os.Exit(0)
	}

	// Parse, validate, and set debug log level(s).
	if err := ParseAndSetDebugLevels(cfg.DebugLevel); err != nil {
		err := fmt.Errorf("%s: %v", funcName, err.Error())
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Validate profile port number
	if cfg.Profile != "" {
		profilePort, err := strconv.Atoi(cfg.Profile)
		if err != nil || profilePort < 1024 || profilePort > 65535 {
			str := "%s: The profile port must be between 1024 and 65535"
			err := fmt.Errorf(str, funcName)
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, usageMessage)
			return nil, nil, err
		}
	}

	// Don't allow ban durations that are too short.
	if cfg.BanDuration < time.Second {
		str := "%s: The banduration option may not be less than 1s -- parsed [%v]"
		err := fmt.Errorf(str, funcName, cfg.BanDuration)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Validate any given whitelisted IP addresses and networks.
	if len(cfg.WhitelistsArr) > 0 {
		var ip net.IP
		cfg.Whitelists = make([]*net.IPNet, 0, len(cfg.WhitelistsArr))

		for _, addr := range cfg.WhitelistsArr {
			_, ipnet, err := net.ParseCIDR(addr)
			if err != nil {
				ip = net.ParseIP(addr)
				if ip == nil {
					str := "%s: The whitelist value of '%s' is invalid"
					err = fmt.Errorf(str, funcName, addr)
					fmt.Fprintln(os.Stderr, err)
					fmt.Fprintln(os.Stderr, usageMessage)
					return nil, nil, err
				}
				var bits int
				if ip.To4() == nil {
					// IPv6
					bits = 128
				} else {
					bits = 32
				}
				ipnet = &net.IPNet{
					IP:   ip,
					Mask: net.CIDRMask(bits, bits),
				}
			}
			cfg.Whitelists = append(cfg.Whitelists, ipnet)
		}
	}

	// --addPeer and --connect do not mix.
	if len(cfg.AddPeers) > 0 && len(cfg.ConnectPeers) > 0 {
		str := "%s: the --addpeer and --connect options can not be " +
			"mixed"
		err := fmt.Errorf(str, funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// --proxy or --connect without --listen disables listening.
	if (cfg.Proxy != "" || len(cfg.ConnectPeers) > 0) &&
		len(cfg.Listeners) == 0 {
		cfg.DisableListen = true
	}

	// Connect means no DNS seeding.
	if len(cfg.ConnectPeers) > 0 {
		cfg.DisableDNSSeed = true
	}

	// Add the default listener if none were specified. The default
	// listener is all addresses on the listen port for the network
	// we are to connect to.
	if len(cfg.Listeners) == 0 {
		cfg.Listeners = []string{
			net.JoinHostPort("", ActiveNetParams.DefaultPort),
		}
	}

	// Check to make sure limited and admin users don't have the same username
	if cfg.RPCUser == cfg.RPCLimitUser && cfg.RPCUser != "" {
		str := "%s: --rpcuser and --rpclimituser must not specify the " +
			"same username"
		err := fmt.Errorf(str, funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Check to make sure limited and admin users don't have the same password
	if cfg.RPCPass == cfg.RPCLimitPass && cfg.RPCPass != "" {
		str := "%s: --rpcpass and --rpclimitpass must not specify the " +
			"same password"
		err := fmt.Errorf(str, funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if cfg.DisableRPC {
		logger.GetLog().Infof("RPC service is disabled")
	}

	if cfg.RPCMaxConcurrentReqs < 0 {
		str := "%s: The rpcmaxwebsocketconcurrentrequests option may " +
			"not be less than 0 -- parsed [%d]"
		err := fmt.Errorf(str, funcName, cfg.RPCMaxConcurrentReqs)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if cfg.MinTxPrice < DefaultMinTxPrice {
		str := "%s: MinTxPrice is too low, at least %f"
		err := fmt.Errorf(str, funcName, DefaultMinTxPrice)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if cfg.BlkProductedTimeOut <= 0 || cfg.BlkProductedTimeOut >= 1 {
		str := "%s: The BlkProductedTimeOut is out of range (0, 1) " +
			"-- parsed [%f]"
		err := fmt.Errorf(str, funcName, cfg.BlkProductedTimeOut)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if cfg.TxConnectTimeOut <= 0 || cfg.TxConnectTimeOut >= 1 {
		str := "%s: The TxConnectTimeOut is out of range (0, 1) " +
			"-- parsed [%f]"
		err := fmt.Errorf(str, funcName, cfg.TxConnectTimeOut)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if cfg.UtxoValidateTimeOut <= 0 || cfg.UtxoValidateTimeOut >= 1 {
		str := "%s: The UtxoValidateTimeOut is out of range (0, 1) " +
			"-- parsed [%f]"
		err := fmt.Errorf(str, funcName, cfg.UtxoValidateTimeOut)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Limit the max orphan count to a sane vlue.
	if cfg.MaxOrphanTxs < 0 {
		str := "%s: The maxorphantx option may not be less than 0 " +
			"-- parsed [%d]"
		err := fmt.Errorf(str, funcName, cfg.MaxOrphanTxs)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if numNets == 0 && cfg.MaxTimeOffset > DefaultMaxTimeOffsetSeconds {
		str := "%s: The maxtimeoffset option may not be greater than %d " +
			"-- parsed [%d]"
		err := fmt.Errorf(str, funcName, DefaultMaxTimeOffsetSeconds, cfg.MaxTimeOffset)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}
	if cfg.MaxTimeOffset <= 5 {
		cfg.MaxTimeOffset = DefaultMaxTimeOffsetSeconds
	}

	// Look for illegal characters in the user agent comments.
	for _, uaComment := range cfg.UserAgentComments {
		if strings.ContainsAny(uaComment, "/:()") {
			err := fmt.Errorf("%s: The following characters must not "+
				"appear in user agent comments: '/', ':', '(', ')'",
				funcName)
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, usageMessage)
			return nil, nil, err
		}
	}

	// Add default port to all listener addresses if needed and remove
	// duplicate addresses.
	cfg.Listeners = fnet.NormalizeAddresses(cfg.Listeners, ActiveNetParams.DefaultPort)

	// Add default port to all added peer addresses if needed and remove
	// duplicate addresses.
	cfg.AddPeers = fnet.NormalizeAddresses(cfg.AddPeers,
		ActiveNetParams.DefaultPort)
	cfg.ConnectPeers = fnet.NormalizeAddresses(cfg.ConnectPeers,
		ActiveNetParams.DefaultPort)

	// --noonion and --onion do not mix.
	if cfg.NoOnion && cfg.OnionProxy != "" {
		err := fmt.Errorf("%s: the --noonion and --onion options may "+
			"not be activated at the same time", funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Check the checkpoints for syntax errors.
	cfg.AddCheckpoints, err = parseCheckpoints(cfg.AddCheckpointsArr)
	if err != nil {
		str := "%s: Error parsing checkpoints: %v"
		err := fmt.Errorf(str, funcName, err)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	// Tor stream isolation requires either proxy or onion proxy to be set.
	if cfg.TorIsolation && cfg.Proxy == "" && cfg.OnionProxy == "" {
		str := "%s: Tor stream isolation requires either proxy or " +
			"onionproxy to be set"
		err := fmt.Errorf(str, funcName)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}

	if cfg.Proxy != "" {
		_, _, err := net.SplitHostPort(cfg.Proxy)
		if err != nil {
			str := "%s: Proxy address '%s' is invalid: %v"
			err := fmt.Errorf(str, funcName, cfg.Proxy, err)
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, usageMessage)
			return nil, nil, err
		}
	}

	// Setup onion address dial function depending on the specified options.
	// The default is to use the same dial function selected above.  However,
	// when an onion-specific proxy is specified, the onion address dial
	// function is set to use the onion-specific proxy while leaving the
	// normal dial function as selected above.  This allows .onion address
	// traffic to be routed through a different proxy than normal traffic.
	if cfg.OnionProxy != "" {
		_, _, err := net.SplitHostPort(cfg.OnionProxy)
		if err != nil {
			str := "%s: Onion proxy address '%s' is invalid: %v"
			err := fmt.Errorf(str, funcName, cfg.OnionProxy, err)
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, usageMessage)
			return nil, nil, err
		}

		// Tor isolation flag means onion proxy credentials will be
		// overridden.
		if cfg.TorIsolation &&
			(cfg.OnionProxyUser != "" || cfg.OnionProxyPass != "") {
			fmt.Fprintln(os.Stderr, "Tor isolation set -- "+
				"overriding specified onionproxy user "+
				"credentials ")
		}
	}

	for _, param := range cfg.AddBtc {
		parts := strings.Split(param, ":")
		if len(parts) != 4 {
			err := fmt.Errorf("btc params format error")
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
		btcParam := &BitcoinParams{
			Host:        fmt.Sprintf("%s:%s", parts[0], parts[1]),
			RpcUser:     parts[2],
			RpcPassword: parts[3],
		}

		cfg.BtcParams = append(cfg.BtcParams, btcParam)
	}

	fmt.Printf("vm.ewasm:%s, vm.evm:%s\n", cfg.EwasmOptions, cfg.EvmOptions)
	// Warn about missing FConfig file only after all other configuration is
	// done.  This prevents the warning on help messages and invalid
	// options.  Note this should go directly before the return.
	if configFileError != nil {
		logger.GetLog().Warnf("%v", configFileError)
	}
	Cfg = &cfg

	return &cfg, remainingArgs, nil
}

// LoadGenesis loads genesis data from the given path
func LoadGenesis(path string) error {
	genesis, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	var params Params
	err = json.Unmarshal(genesis, &params)
	if err != nil {
		return err
	}

	if params.CollectHeight == 0 {
		return errors.New("CollectHeight must be greater than 0")
	}
	ActiveNetParams.CollectHeight = params.CollectHeight

	if params.ChainStartTime == 0 {
		return errors.New("ChainStartTime must be greater than 0")
	}
	ActiveNetParams.ChainStartTime = params.ChainStartTime
	return nil
}
