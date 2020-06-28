// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package main

import (
    "github.com/AsimovNetwork/asimov/chaincfg"
    "github.com/AsimovNetwork/asimov/crypto"
    "github.com/AsimovNetwork/asimov/logger"
    "os"
    "path/filepath"
)

var (
    // A log for main package
    mainLog            logger.Logger
    defaultLogFilename = "autotx.log"
)

func automain() error {
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

    // Get a channel that will be closed when a shutdown signal has been
    // triggered either from an OS signal such as SIGINT (Ctrl+C) or from
    // another subsystem such as the RPC server.
    interrupt := interruptListener()
    defer mainLog.Info("Shutdown complete")

    // Show version at startup.
    mainLog.Infof("Version %s", chaincfg.Version())

    // Return now if an interrupt signal was triggered.
    if interruptRequested(interrupt) {
        return nil
    }
    // Create a new block chain instance with the appropriate configuration.
    acc, err := crypto.NewAccount(chaincfg.Cfg.Privatekey)
    if acc == nil {
        mainLog.Error("No account configuration")
        return nil
    }

    server := Server{
        quit:          make(chan struct{}),
        config:        cfg,
        account:       acc,
        mergeUtxosThd: DefaultMergeUtxoThreshold,
    }
    err = server.Start()
    if err != nil {
        return err
    }

    defer func() {
        mainLog.Infof("Gracefully shutting down the server...")
        server.Stop()
        server.WaitForShutdown()
    }()
    // Wait until the interrupt signal is received from an OS signal or
    // shutdown is requested through one of the subsystems such as the RPC
    // server.
    <-interrupt
    return nil
}

func main() {
    // Work around defer not working after os.Exit()
    if err := automain(); err != nil {
        mainLog.Errorf("could not run auto server: %v", err)
        os.Exit(1)
    }
}
