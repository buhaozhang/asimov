// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package minersync

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/common/serialization"
	"github.com/AsimovNetwork/asimov/database"
	"io"
	"sync"
	"time"
)

const (
	// getBtcPowerTime is the time interval of collect bitcoin power.
	getBtcPowerTime = 120 * time.Second
	// maxBtcRollback is the maximum number of bitcoin block rollbacks.
	maxBtcRollback = 6
	// maxUpdateMiner is the maximum amount of collect bitcoin power at one time.
	maxUpdateMiner = 256
)

var (
	bitcoinMinerBucketName   = []byte("btcMinerIdx")
	btcaddr2nameBuckerName   = []byte("btcaddr2nameIdx")
	validatorBucketName      = []byte("validatorIdx")
	validatorSerializedBytes = []byte{1}
)

// ValidatorTx is a candidate for mapped transactions.
type ValidatorTx struct {
	TxId       string
	Vin        []string
	OutAddress common.Address
}

// MinerInfo is the detail of collect bitcoin power.
type MinerInfo struct {
	height       int32
	minerName    string
	blockHash    common.Hash
	time         uint32
	minerAddress string
	txs          []*ValidatorTx
}

// Name returns pool name of this bitcoin block.
func (mi *MinerInfo) Name() string {
	return mi.minerName
}

// Address returns mining address of this bitcoin block.
func (mi *MinerInfo) Address() string {
	return mi.minerAddress
}

// Time returns time of this bitcoin block.
func (mi *MinerInfo) Time() uint32 {
	return mi.time
}

// ValidatorTxs returns the candidates for mapped transactions.
func (mi *MinerInfo) ValidatorTxs() []*ValidatorTx {
	return mi.txs
}

// SerializeSize returns the number of bytes it would take to
// serialize the miner info.
func (mi *MinerInfo) SerializeSize() int {
	// height 4 bytes + blockHash 32 bytes + time 4 bytes +
	// var bytes minerName + var bytes minerAddress + var bytes txs
	size := 40 + serialization.VarIntSerializeSize(uint64(len(mi.minerName))) + len(mi.minerName)
	size += serialization.VarIntSerializeSize(uint64(len(mi.minerAddress))) + len(mi.minerAddress)
	size += serialization.VarIntSerializeSize(uint64(len(mi.txs)))
	for _, tx := range mi.txs {
		size += serialization.VarIntSerializeSize(uint64(len(tx.TxId))) + len(tx.TxId)
		size += common.AddressLength + serialization.VarIntSerializeSize(uint64(len(tx.Vin)))
		for _, txin := range tx.Vin {
			size += serialization.VarIntSerializeSize(uint64(len(txin))) + len(txin)
		}
	}
	return size
}

// Serialize encodes miner info from w into the receiver using a format
// that is suitable for long-term storage such as a database.
func (mi *MinerInfo) Serialize(w io.Writer) error {
	if err := serialization.WriteUint32(w, uint32(mi.height)); err != nil {
		return err
	}
	if err := serialization.WriteNBytes(w, mi.blockHash.Bytes()); err != nil {
		return err
	}
	if err := serialization.WriteUint32(w, mi.time); err != nil {
		return err
	}
	if err := serialization.WriteVarString(w, 0, mi.minerName); err != nil {
		return err
	}
	if err := serialization.WriteVarString(w, 0, mi.minerAddress); err != nil {
		return err
	}
	if err := serialization.WriteVarInt(w, 0, uint64(len(mi.txs))); err != nil {
		return err
	}
	for _, tx := range mi.txs {
		if err := serialization.WriteVarString(w, 0, tx.TxId); err != nil {
			return err
		}
		if err := serialization.WriteVarInt(w, 0, uint64(len(tx.Vin))); err != nil {
			return err
		}
		for _, txin := range tx.Vin {
			if err := serialization.WriteVarString(w, 0, txin); err != nil {
				return err
			}
		}
		if err := serialization.WriteNBytes(w, tx.OutAddress.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// Deserialize decodes miner info from r into the receiver using a format
// that is suitable for long-term storage such as a database.
func (mi *MinerInfo) Deserialize(r io.Reader) (err error) {
	var height uint32
	if err = serialization.ReadUint32(r, &height); err != nil {
		return err
	}
	mi.height = int32(height)
	if err = serialization.ReadNBytes(r, mi.blockHash[:], common.HashLength); err != nil {
		return err
	}
	if err = serialization.ReadUint32(r, &mi.time); err != nil {
		return err
	}
	mi.minerName, err = serialization.ReadVarString(r, 0)
	if err != nil {
		return err
	}
	mi.minerAddress, err = serialization.ReadVarString(r, 0)
	if err != nil {
		return err
	}
	txLen, err := serialization.ReadVarInt(r, 0)
	if err != nil {
		return err
	}
	for i := uint64(0); i < txLen; i++ {
		var validatorTx ValidatorTx
		if validatorTx.TxId, err = serialization.ReadVarString(r, 0); err != nil {
			return err
		}
		vinLen, err := serialization.ReadVarInt(r, 0)
		if err != nil {
			return err
		}
		for j := uint64(0); j < vinLen; j++ {
			vin, err := serialization.ReadVarString(r, 0)
			if err != nil {
				return err
			}
			validatorTx.Vin = append(validatorTx.Vin, vin)
		}
		if err = serialization.ReadNBytes(r, validatorTx.OutAddress[:], common.AddressLength); err != nil {
			return err
		}
		mi.txs = append(mi.txs, &validatorTx)
	}

	return nil
}

// BtcPowerCollector is a collector for collect bitcoin power. It fetch
// the miner information from bitcoin and saves it in the database.
type BtcPowerCollector struct {
	sync.Mutex
	client        ainterface.IBtcClient
	minerInfos    []*MinerInfo
	wg            sync.WaitGroup
	existCh       chan interface{}
	timer         *time.Timer
	db            database.Transactor
	initBtcHeight int32
	latestHeight  int32
}

// NewBtcPowerCollector returns a new BtcPowerCollector using the
// bitcoin client and database.
func NewBtcPowerCollector(c ainterface.IBtcClient, db database.Transactor) *BtcPowerCollector {
	return &BtcPowerCollector{
		client:        c,
		db:            db,
		initBtcHeight: chaincfg.ActiveNetParams.CollectHeight,
	}
}

// encodeMinerInfoDbKey returns bytes of height.
func encodeMinerInfoDbKey(height int32) []byte {
	var buf = make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(height))
	return buf
}

// Start begins fetch the miner information from bitcoin.
func (bpc *BtcPowerCollector) Start() error {
	bpc.Lock()
	defer bpc.Unlock()
	if bpc.existCh != nil {
		return errors.New("btc power is already started")
	}
	log.Info("BtcPowerCollector start")

	// Try connect btc node.
	var result GetBlockChainInfoResult
	err := bpc.client.GetBitcoinBlockChainInfo(&result)
	if err != nil {
		return err
	}

	bpc.timer = time.NewTimer(0)
	bpc.existCh = make(chan interface{})
	bpc.wg.Add(1)
	go func() {
		existCh := bpc.existCh
	mainloop:
		for {
			select {
			case <-bpc.timer.C:
				_, _, err := bpc.UpdateMinerInfo(maxUpdateMiner)
				if err != nil {
					log.Errorf("BtcPowerCollector UpdateMinerInfo failed: %v", err)
					bpc.timer.Reset(10 * time.Second)
				} else {
					bpc.timer.Reset(getBtcPowerTime)
				}
			case <-existCh:
				break mainloop
			}
		}
		bpc.timer.Stop()
	cleanup:
		for {
			select {
			case <-bpc.timer.C:
			default:
				break cleanup
			}
		}
		bpc.wg.Done()
	}()
	return nil
}

// Halt ends fetch the miner information from bitcoin.
func (bpc *BtcPowerCollector) Halt() error {
	bpc.Lock()
	defer bpc.Unlock()
	log.Info("BtcPowerCollector Stop")

	if bpc.existCh != nil {
		close(bpc.existCh)
		bpc.existCh = nil
	}
	bpc.wg.Wait()
	return nil
}

// Init read old miner information from database and fetch the miner
// information from bitcoin when the count not enough.
func (bpc *BtcPowerCollector) Init(height, count int32) ([]*MinerInfo, error) {
	err := bpc.db.Update(func(dbTx database.Tx) error {
		_, err := dbTx.Metadata().CreateBucketIfNotExists(bitcoinMinerBucketName)
		if err != nil {
			return err
		}
		_, err = dbTx.Metadata().CreateBucketIfNotExists(btcaddr2nameBuckerName)
		if err != nil {
			return err
		}
		bucket := dbTx.Metadata().Bucket(validatorBucketName)
		if bucket == nil {
			bucket, err = dbTx.Metadata().CreateBucket(validatorBucketName)
			if err != nil {
				return err
			}
			for _, addr := range chaincfg.ActiveNetParams.GenesisCandidates {
				err = bucket.Put(addr[:], validatorSerializedBytes)
				if err != nil {
					return nil
				}
			}
		}
		return nil
	})

	miners, err := bpc.getMinerInfoFromDB(bpc.initBtcHeight+height, count)
	if err != nil {
		return nil, err
	}

	minerLen := len(miners)
	if minerLen > 0 {
		bpc.latestHeight = miners[minerLen-1].height
	} else {
		bpc.latestHeight = bpc.initBtcHeight + height - 1
	}
	// if miner not enough, sync from btc node.
	if int32(minerLen) < count {
		for i := int32(0); i < count - int32(minerLen); i += maxUpdateMiner {
			newminers, _, err := bpc.UpdateMinerInfo(maxUpdateMiner)
			if err != nil {
				return nil, err
			}
			miners = append(miners, newminers...)
		}
	} else {
		lastestHeight, err := bpc.getMinerInfoLastestHeight(bpc.latestHeight)
		if err == nil && lastestHeight > 0 {
			bpc.latestHeight = lastestHeight
		}
	}
	bpc.minerInfos = miners

	// Update minerInfo until it synced newest nodes.
	for ; true; {
		_, count, _ := bpc.UpdateMinerInfo(maxUpdateMiner)
		if maxUpdateMiner > count {
			break
		}
	}
	return miners, nil
}

// writeMinerInfo write miner information to database when fetch the
// miner information from bitcoin.
func (bpc *BtcPowerCollector) writeMinerInfo(newminers []*MinerInfo) error {
	return bpc.db.Update(func(dbTx database.Tx) error {
		minerbucket := dbTx.Metadata().Bucket(bitcoinMinerBucketName)
		mappingBucket := dbTx.Metadata().Bucket(btcaddr2nameBuckerName)
		validatorBucket := dbTx.Metadata().Bucket(validatorBucketName)
		for _, v := range newminers {
			w := bytes.NewBuffer(make([]byte, 0, v.SerializeSize()))
			if err := v.Serialize(w); err != nil {
				log.Error("Serialize miner err")
				return err
			}
			if err := minerbucket.Put(encodeMinerInfoDbKey(v.height), w.Bytes()); err != nil {
				log.Error("failed to push miner into db, height", v.height)
				return err
			}
			if len(v.Address()) > 0 {
				value := v.minerName
				if len(value) == 0 {
					value = "UNKNOWN"
				}
				if err := mappingBucket.Put([]byte(v.Address()), []byte(value)); err != nil {
					log.Error("failed to push miner addr 2 name mapping", v.Address(), value)
					return err
				}
			}
			for _, tx := range v.txs {
				err := validatorBucket.Put(tx.OutAddress[:], validatorSerializedBytes)
				if err != nil {
					log.Errorf("failed to save history validator ", tx.OutAddress.String(), err)
					return err
				}
			}
		}
		return nil
	})
}

// UpdateMinerInfo fetch the miner information about the number of count
// from bitcoin.
func (bpc *BtcPowerCollector) UpdateMinerInfo(count int32) ([]*MinerInfo, int32, error) {
	var chaininfo GetBlockChainInfoResult
	err := bpc.client.GetBitcoinBlockChainInfo(&chaininfo)
	if err != nil {
		return nil, 0, err
	}
	newheight := chaininfo.Blocks
	if newheight <= bpc.latestHeight+maxBtcRollback {
		return nil, 0, nil
	}

	min := func(x, y int32) int32 {
		if x < y {
			return x
		}
		return y
	}

	maxcount := newheight - bpc.latestHeight - maxBtcRollback
	get_count := int32(min(count, int32(maxcount)))
	var newminers []GetBitcoinBlockMinerInfoResult
	err = bpc.client.GetBitcoinMinerInfo(&newminers, bpc.latestHeight+1, get_count)
	if err != nil {
		return nil, 0, err
	}
	log.Infof("Sync miner from btc node from %d, count=%d, top at %d",
		bpc.latestHeight+1, len(newminers), newheight)

	curHeight := bpc.latestHeight + 1
	var bs []*MinerInfo
	for _, b := range newminers {
		if b.Height != curHeight {
			return nil, 0, errors.New("rpc result height error")
		}
		miner := &MinerInfo{
			height:       curHeight,
			minerName:    b.Pool,
			minerAddress: b.Address,
			blockHash:    common.HexToHash(b.Hash),
			time:         b.Time,
		}
		for _, vtx := range b.ValidatorTxs {
			outAddr, err := NewAddressFromBitcoinAddress(vtx.OutAddress, vtx.AddressType)
			if err != nil {
				log.Error("BTC out address err, failed to parse", err)
				continue
			}
			miner.txs = append(miner.txs, &ValidatorTx{
				TxId:       vtx.Txid,
				OutAddress: *outAddr,
				Vin:        vtx.Vin,
			})
		}
		bs = append(bs, miner)
		curHeight++
	}

	if err := bpc.writeMinerInfo(bs[:]); err != nil {
		return nil, 0, err
	}
	bpc.Lock()
	bpc.latestHeight = curHeight - 1
	bpc.Unlock()
	return bs, get_count, nil
}

// getMinerInfoFromDB read old miner information from database,
// the read range is height to height add count.
func (bpc *BtcPowerCollector) getMinerInfoFromDB(height, count int32) ([]*MinerInfo, error) {
	if count <= 0 {
		return nil, errors.New("count must be greater than 0")
	}
	res := make([]*MinerInfo, 0, count)
	err := bpc.db.View(func(dbTx database.Tx) error {
		bucket := dbTx.Metadata().Bucket(bitcoinMinerBucketName)
		for i := int32(0); i < count; i++ {
			serializedBytes := bucket.Get(encodeMinerInfoDbKey(height + i))
			if len(serializedBytes) == 0 {
				return nil
			}
			var info MinerInfo
			r := bytes.NewBuffer(serializedBytes)
			if err := info.Deserialize(r); err != nil {
				return err
			}
			info.height = height + i
			res = append(res, &info)
		}
		return nil
	})

	return res, err
}

//getMinerInfoLastestHeight returns lastest block height of miner
// information form database, search begin with from.
func (bpc *BtcPowerCollector) getMinerInfoLastestHeight(from int32) (int32, error) {
	lastHeight := from - 1
	err := bpc.db.View(func(dbTx database.Tx) error {
		bucket := dbTx.Metadata().Bucket(bitcoinMinerBucketName)
		end := from + 10000
		for ; ; end = end + 10000 {
			serializedBytes := bucket.Get(encodeMinerInfoDbKey(end))
			if len(serializedBytes) == 0 {
				break
			}
		}
		for ; from < end; {
			m := (from + end) / 2
			serializedBytes := bucket.Get(encodeMinerInfoDbKey(m))
			if len(serializedBytes) == 0 {
				end = m
			} else {
				from = m + 1
				lastHeight = m
			}
		}
		return nil
	})

	return lastHeight, err
}

// GetMinerName returns pool name of the miner. if not miner not
// find, return empty string.
func (bpc *BtcPowerCollector) GetMinerName(miner string) string {
	v := ""
	_ = bpc.db.View(func(dbTx database.Tx) error {
		bucket := dbTx.Metadata().Bucket(btcaddr2nameBuckerName)
		serializedBytes := bucket.Get([]byte(miner))
		if len(serializedBytes) > 0 {
			v = string(serializedBytes)
		}
		return nil
	})
	return v
}

// GetBitcoinMinerread fetch miner information from cache or database,
// the read range is height to height add count.
//
// This function is safe for concurrent access.
func (bpc *BtcPowerCollector) GetBitcoinMiner(height, count int32) ([]*MinerInfo, error) {
	height += bpc.initBtcHeight

	if count <= 0 {
		return nil, errors.New("count must be greater than 0")
	}

	var bs []*MinerInfo
	{
		bpc.Lock()
		bs = bpc.minerInfos[:]
		bpc.Unlock()
	}
	if len(bs) == 0 {
		return nil, errors.New("BtcPowerCollector not at work")
	}
	beginHeight := bs[0].height
	lastHeight := bs[len(bs)-1].height
	targetLastHeight := height + count - 1

	if height >= beginHeight && lastHeight >= targetLastHeight {
		return bs[height-beginHeight : targetLastHeight-beginHeight+1], nil
	}

	res := make([]*MinerInfo, 0, count)
	curHeight := height
	if height < beginHeight {
		var get_count int32
		if targetLastHeight < beginHeight {
			get_count = count
		} else {
			get_count = beginHeight - height
		}
		arr, err := bpc.getMinerInfoFromDB(height, get_count)
		if err != nil {
			return nil, err
		}
		for _, v := range arr {
			res = append(res, v)
		}
		curHeight = beginHeight
	}

	if len(res) == int(count) {
		return res, nil
	}

	for i := curHeight - beginHeight; int(i) < len(bs) && bs[i].height <= targetLastHeight; i++ {
		res = append(res, bs[i])
	}

	if len(res) == int(count) {
		return res, nil
	}

	if len(res) > 0 {
		curHeight = lastHeight + 1
	}
	arr, err := bpc.getMinerInfoFromDB(curHeight, count-int32(len(res)))
	if err != nil {
		return nil, err
	}
	for _, v := range arr {
		res = append(res, v)
	}

	if len(res) < int(count) {
		return nil, errors.New("not enough miners")
	}

	return res, nil
}

// NewAddressFromBitcoinAddress returns a new Address using the hex of
// btc address and type of btc address.
func NewAddressFromBitcoinAddress(hex string, addrType int32) (*common.Address, error) {
	addrBytes := common.FromHex(hex)
	if len(addrBytes) != common.AddressLength-1 {
		return nil, errors.New("address len not match")
	}
	var addr common.Address
	if addrType == 1 {
		addr[0] = common.PubKeyHashAddrID
		for i := 1; i < common.AddressLength; i++ {
			addr[i] = addrBytes[common.AddressLength-i-1]
		}
	} else if addrType == 2 {
		return nil, errors.New("asimov don't support script hash address to miner block")
	} else {
		return nil, errors.New("out address type unknown")
	}
	return &addr, nil
}
