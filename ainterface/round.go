// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package ainterface

import (
    "github.com/AsimovNetwork/asimov/common"
    "github.com/AsimovNetwork/asimov/common/serialization"
    "github.com/AsimovNetwork/asimov/database"
    "io"
    "unsafe"
)

const (
    // Round payload = RoundStartUnix 8 bytes + duration 8 bytes
    RoundPayLoad = 16
)

type ValidatorInfo struct {
    Address   common.Address
    MinerName string
}

type IBtcClient interface {
    GetBitcoinMinerInfo(result interface{}, height int32, count int32) error
    GetBitcoinBlockChainInfo(result interface{}) error
}

type GetValidatorsCallBack func([]string) ([]common.Address, []int32, error)

type IRoundManager interface {
    Init(round uint32, db database.Transactor, c IBtcClient) error
    // let the manager run
    Start()
    Halt()
    GetContract() common.Address

    GetHsMappingByRound(round uint32) (map[string]*ValidatorInfo, error)

    GetRoundInterval(round int64) int64
    GetNextRound(round *Round) (*Round, error)

    HasValidator(validator common.Address) bool
    GetValidators(blockHash common.Hash, round uint32, fn GetValidatorsCallBack) ([]*common.Address, map[common.Address]uint16, error)
}

type Round struct {
    Round         uint32
    RoundStartUnix int64
    Duration      int64
}


// Deserialize decodes a round from r into the receiver using a format
// that is suitable for long-term storage such as a database while respecting
// the Version field.
func (round *Round) Deserialize(r io.Reader) error {
    if err := serialization.ReadUint64(r, (*uint64)(unsafe.Pointer(&round.RoundStartUnix))); err != nil {
        return err
    }
    if err := serialization.ReadUint64(r, (*uint64)(unsafe.Pointer(&round.Duration))); err != nil {
        return err
    }
    return nil
}

// Serialize encodes a round from w into the receiver using a format
// that is suitable for long-term storage such as a database while respecting
// the Version field.
func (round *Round) Serialize(w io.Writer) error {
    if err := serialization.WriteUint64(w, uint64(round.RoundStartUnix)); err != nil {
        return err
    }
    if err := serialization.WriteUint64(w, uint64(round.Duration)); err != nil {
        return err
    }

    return nil
}

func (round *Round) SerializeSize() int {
    return RoundPayLoad
}
