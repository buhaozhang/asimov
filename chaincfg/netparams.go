// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package chaincfg

// ActiveNetParams is a pointer to the parameters specific to the
// currently active bitcoin network.
var ActiveNetParams = &mainNetParams

// netParams is used to group parameters for various networks such as the main
// network and test networks.
type netParams struct {
	*Params
}

// mainNetParams contains parameters specific to the main network
// (protos.MainNet).  NOTE: The RPC port is intentionally different than the
// reference implementation because btcd does not handle wallet requests.  The
// separate wallet process listens on the well-known port and forwards requests
// it does not handle on to btcd.  This approach allows the wallet process
// to emulate the full reference implementation RPC API.
var mainNetParams = netParams{
	Params:  &MainNetParams,
}

// developNetParams contains parameters specific to the develop test network
// (protos.DevelopNet).  NOTE: The RPC port is intentionally different than the
// reference implementation - see the mainNetParams comment for details.
var developNetParams = netParams{
	Params:  &DevelopNetParams,
}

// testNetParams contains parameters specific to the test network
// (protos.TestNet).  NOTE: The RPC port is intentionally different than the
// reference implementation - see the mainNetParams comment for details.
var testNetParams = netParams{
	Params:  &TestNetParams,
}
