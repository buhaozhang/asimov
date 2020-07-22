// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/cmd/generateblock/servers"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/database/dbdriver"
	"github.com/AsimovNetwork/asimov/database/dbimpl/ethdb"
	"github.com/AsimovNetwork/asimov/limits"
	"github.com/AsimovNetwork/asimov/logger"
	"io/ioutil"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
)

// shutdownRequestChannel is used to initiate shutdown from one of the
// subsystems using the same code paths as when an interrupt signal is received.
var shutdownRequestChannel = make(chan struct{})

// interruptSignals defines the default signals to catch in order to do a proper
// shutdown.  This may be modified during init depending on the platform.
var interruptSignals = []os.Signal{os.Interrupt}

// interruptListener listens for OS Signals such as SIGINT (Ctrl+C) and shutdown
// requests from shutdownRequestChannel.  It returns a channel that is closed
// when either signal is received.
func interruptListener() <-chan struct{} {
	c := make(chan struct{})
	go func() {
		interruptChannel := make(chan os.Signal, 1)
		signal.Notify(interruptChannel, interruptSignals...)

		// Listen for initial shutdown signal and close the returned
		// channel to notify the caller.
		select {
		case sig := <-interruptChannel:
			mainLog.Infof("Received signal (%s).  Shutting down...",
				sig)

		case <-shutdownRequestChannel:
			mainLog.Info("Shutdown requested.  Shutting down...")
		}
		close(c)
		dumpStacks()

		// Listen for repeated signals and display a message so the user
		// knows the shutdown is in progress and the process is not
		// hung.
		for {
			select {
			case sig := <-interruptChannel:
				mainLog.Infof("Received signal (%s).  Already "+
					"shutting down...", sig)

			case <-shutdownRequestChannel:
				mainLog.Info("Shutdown requested.  Already " +
					"shutting down...")
			}
		}
	}()

	return c
}

// interruptRequested returns true when the channel returned by
// interruptListener was closed.  This simplifies early shutdown slightly since
// the caller can just use an if statement instead of a select.
func interruptRequested(interrupted <-chan struct{}) bool {
	select {
	case <-interrupted:
		return true
	default:
	}

	return false
}

func dumpStacks() {
	buf := make([]byte, 1638400)
	buf = buf[:runtime.Stack(buf, true)]
	writeStack(buf)
}

func writeStack(buf []byte) {
	mainLog.Infof("\n\n\n\n\n")
	mainLog.Infof(" dumpstack:" + "\n\n")
	mainLog.Infof(string(buf))
}

const (
	// blockDbNamePrefix is the prefix for the block database name.  The
	// database type is appended to this value to form the full block
	// database name.
	blockDbNamePrefix  = "blocks"
	defaultLogFilename = "asimovd.log"
)

var (
	// A log for main package
	mainLog logger.Logger
)

// winServiceMain is only invoked on Windows.  It detects when flowd is running
// as a service and reacts accordingly.
var winServiceMain func() (bool, error)

// flowdMain is the real main function for asimovd.  It is necessary to work around
// the fact that deferred functions do not run when os.Exit() is called.  The
// optional serverChan parameter is mainly used by the service code to be
// notified with the server once it is setup so it can gracefully stop it when
// requested from the service control manager.
func flowdMain(serverChan chan<- *servers.NodeServer) error {
	// Load configuration and parse command line.  This function also
	// initializes logging and configures it accordingly.
	cfg, _, err := chaincfg.LoadConfig()
	if err != nil {
		return err
	}

	// Initialize logger rotation.  After logger rotation has been initialized, the
	// logger variables may be used.
	logger.InitLogRotator(filepath.Join(cfg.LogDir, defaultLogFilename))

	defer func() {
		logger.CloseLogRotator()
	}()

	mainLog = logger.GetLog()
	if err := chaincfg.LoadGenesis(cfg.GenesisParamFile); err != nil {
		mainLog.Errorf("load genesis error: %v", err)
		return err
	}

	// Get a channel that will be closed when a shutdown signal has been
	// triggered either from an OS signal such as SIGINT (Ctrl+C) or from
	// another subsystem such as the RPC server.
	interrupt := interruptListener()
	defer mainLog.Info("Shutdown complete")

	// Show version at startup.
	mainLog.Infof("Version %s", chaincfg.Version())

	// Enable http profiling server if requested.
	if cfg.Profile != "" {
		go func() {
			listenAddr := net.JoinHostPort("", cfg.Profile)
			mainLog.Infof("Profile server listening on %s", listenAddr)
			profileRedirect := http.RedirectHandler("/debug/pprof",
				http.StatusSeeOther)
			http.Handle("/", profileRedirect)
			mainLog.Errorf("%v", http.ListenAndServe(listenAddr, nil))
		}()
	}

	// Write cpu profile if requested.
	if cfg.CPUProfile != "" {
		f, err := os.Create(cfg.CPUProfile)
		if err != nil {
			mainLog.Errorf("Unable to create cpu profile: %v", err)
			return err
		}
		defer f.Close()

		if err := pprof.StartCPUProfile(f); err != nil {
			mainLog.Errorf("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	// Return now if an interrupt signal was triggered.
	if interruptRequested(interrupt) {
		return nil
	}

	// Load the block database.
	db, err := loadBlockDB(cfg)
	if err != nil {
		mainLog.Errorf("%v", err)
		return err
	}
	defer func() {
		// Ensure the database is sync'd and closed on shutdown.
		mainLog.Infof("Gracefully shutting down the database...")
		db.Close()
	}()

	// Load StateDB
	stateDB, err := ethdb.NewLDBDatabase(cfg.StateDir, 768, 1024)
	if err != nil {
		mainLog.Errorf("%v", err)
		return err
	}
	defer func() {
		// Ensure the database is sync'd and closed on shutdown.
		mainLog.Infof("Gracefully shutting down the State DB...")
		stateDB.Close()
	}()

	// Return now if an interrupt signal was triggered.
	if interruptRequested(interrupt) {
		return nil
	}

	// Create server and start it.
	server, err := servers.NewServer(db, stateDB, cfg.AgentBlacklist,
		cfg.AgentWhitelist, chaincfg.ActiveNetParams.Params, interrupt, shutdownRequestChannel)
	if err != nil {
		// TODO: this logging could do with some beautifying.
		mainLog.Errorf("Unable to start server on %v: %v",
			cfg.Listeners, err)
		return err
	}

	defer func() {
		mainLog.Infof("Gracefully shutting down the server...")
		server.Stop()
		server.WaitForShutdown()
	}()

	server.Start()

	if chaincfg.Cfg.Consensustype != "satoshiplus" {
		mainLog.Info("only support satoshiplus")
		return nil
	}

	c := generateConfig{
		BlockTemplateGenerator: server.BlkTmplGenerator(),
		ProcessBlock:           server.SyncManager().ProcessBlock,
		IsCurrent:              server.SyncManager().IsCurrentAndCheckAccepted,
		Chain:                  server.BlockChain(),
		GasFloor:               common.GasFloor,
		GasCeil:                common.GasCeil,
		RoundManager:           server.RoundManger(),
		ProcessSig:             server.SigMemPool().ProcessSig,
	}

	genesis, err := ioutil.ReadFile(filepath.Join(chaincfg.DefaultAppDataDir, "gen.json"))
	if err != nil {
		return err
	}
	params := struct {
		StartHeight int32
		PrivateKeys []string
	}{}
	err = json.Unmarshal(genesis, &params)
	if err != nil {
		return err
	}

	c.StartHeight = params.StartHeight
	for _, key := range params.PrivateKeys {
		acc, _ := crypto.NewAccount(key)
		if acc == nil {
			mainLog.Warn("private key error")
		}
		c.Accounts = append(c.Accounts, acc)
	}

	if generate(&c) {
		mainLog.Info("geneblock success!!")
	} else {
		mainLog.Info("geneblock failed!!")
	}

	return nil
}

// dbPath returns the path to the block database given a database type.
func blockDbPath(dataDir, dbType string) string {
	// The database name is based on the database type.
	dbName := blockDbNamePrefix + "_" + dbType
	if dbType == "sqlite" {
		dbName = dbName + ".db"
	}
	dbPath := filepath.Join(dataDir, dbName)
	return dbPath
}

// loadBlockDB loads (or creates when needed) the block database taking into
// account the selected database backend and returns a handle to it.  It also
// contains additional logic such warning the user if there are multiple
// databases which consume space on the file system and ensuring the regression
// test database is clean when in regression test mode.
func loadBlockDB(cfg *chaincfg.FConfig) (database.Transactor, error) {
	// The database name is based on the database type.
	dbPath := blockDbPath(cfg.DataDir, database.FFLDB)

	mainLog.Infof("Loading block database from '%s'", dbPath)
	db, err := dbdriver.Open(database.FFLDB, dbPath, chaincfg.ActiveNetParams.Net)
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
		db, err = dbdriver.Create(database.FFLDB, dbPath, chaincfg.ActiveNetParams.Net)
		if err != nil {
			return nil, err
		}
	}

	mainLog.Info("Block database loaded")
	return db, nil
}

func main() {
	// Use all processor cores.
	runtime.GOMAXPROCS(runtime.NumCPU())

	// Block and transaction processing can cause bursty allocations.  This
	// limits the garbage collector from excessively overallocating during
	// bursts.  This value was arrived at with the help of profiling live
	// usage.
	debug.SetGCPercent(10)

	// Up some limits.
	if err := limits.SetLimits(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to set limits: %v\n", err)
		os.Exit(1)
	}

	// Call serviceMain on Windows to handle running as a service.  When
	// the return isService flag is true, exit now since we ran as a
	// service.  Otherwise, just fall through to normal operation.
	if runtime.GOOS == "windows" {
		isService, err := winServiceMain()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if isService {
			os.Exit(0)
		}
	}

	// Work around defer not working after os.Exit()
	if err := flowdMain(nil); err != nil {
		os.Exit(1)
	}
}
