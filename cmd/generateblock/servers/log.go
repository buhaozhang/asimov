// Copyright (c) 2018 The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package servers

import (
	"github.com/AsimovNetwork/asimov/logger"
)

// log is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var srvrLog logger.Logger
var peerLog logger.Logger
var txmpLog logger.Logger
var rpcsLog logger.Logger
var amgrLog logger.Logger

// The default amount of logging is none.
func init() {
	DisableLog()
}

// DisableLog disables all library log output.  Logging output is disabled
// by default until either UseLogger or SetLogWriter are called.
func DisableLog() {
	srvrLog = logger.Disabled
	peerLog = logger.Disabled
	txmpLog = logger.Disabled
	rpcsLog = logger.Disabled
	amgrLog = logger.Disabled
}

// UseLogger uses a specified Logger to output package logging info.
// This should be used in preference to SetLogWriter if the caller is also
// using btclog.
func UseLogger() {
	srvrLog = logger.GetLogger("SRVR")
	peerLog = logger.GetLogger("PEER")
	txmpLog = logger.GetLogger("TXMP")
	rpcsLog = logger.GetLogger("RPCS")
	amgrLog = logger.GetLogger("AMGR")
}

