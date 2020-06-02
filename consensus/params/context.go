// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package params

// Context used in consensus service
type Context struct {
    // RoundInterval indicates the interval a round cost, unit in seconds
    RoundInterval  int64
    // the round
    Round          int64
    // the slot in a round
    Slot           int64
    // RoundStartTime indicates the timestamp when a round start, unit in seconds
    RoundStartTime int64
    // the round size
    RoundSize      int64
}
