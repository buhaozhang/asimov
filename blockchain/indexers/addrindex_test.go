// Copyright (c) 2018-2020. The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package indexers

import (
	"bytes"
	"fmt"
	"github.com/AsimovNetwork/asimov/blockchain/mock"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/database"
	"github.com/AsimovNetwork/asimov/txscript"
	"github.com/AsimovNetwork/asimov/asiutil"
	"testing"

	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/blockchain/txo"
)

// addrIndexBucket provides a mock address index database bucket by implementing
// the internalBucket interface.
type addrIndexBucket struct {
	levels map[[levelKeySize]byte][]byte
}

// Clone returns a deep copy of the mock address index bucket.
func (b *addrIndexBucket) Clone() *addrIndexBucket {
	levels := make(map[[levelKeySize]byte][]byte)
	for k, v := range b.levels {
		vCopy := make([]byte, len(v))
		copy(vCopy, v)
		levels[k] = vCopy
	}
	return &addrIndexBucket{levels: levels}
}

// Get returns the value associated with the key from the mock address index
// bucket.
//
// This is part of the internalBucket interface.
func (b *addrIndexBucket) Get(key []byte) []byte {
	var levelKey [levelKeySize]byte
	copy(levelKey[:], key)
	return b.levels[levelKey]
}

// Put stores the provided key/value pair to the mock address index bucket.
//
// This is part of the internalBucket interface.
func (b *addrIndexBucket) Put(key []byte, value []byte) error {
	var levelKey [levelKeySize]byte
	copy(levelKey[:], key)
	b.levels[levelKey] = value
	return nil
}

// Delete removes the provided key from the mock address index bucket.
//
// This is part of the internalBucket interface.
func (b *addrIndexBucket) Delete(key []byte) error {
	var levelKey [levelKeySize]byte
	copy(levelKey[:], key)
	delete(b.levels, levelKey)
	return nil
}

// printLevels returns a string with a visual representation of the provided
// address key taking into account the max size of each level.  It is useful
// when creating and debugging test cases.
func (b *addrIndexBucket) printLevels(addrKey [addrKeySize]byte) string {
	highestLevel := uint8(0)
	for k := range b.levels {
		if !bytes.Equal(k[:levelOffset], addrKey[:]) {
			continue
		}
		level := uint8(k[levelOffset])
		if level > highestLevel {
			highestLevel = level
		}
	}

	var levelBuf bytes.Buffer
	_, _ = levelBuf.WriteString("\n")
	maxEntries := level0MaxEntries
	for level := uint8(0); level <= highestLevel; level++ {
		data := b.levels[keyForLevel(addrKey, level)]
		numEntries := len(data) / txEntrySize
		for i := 0; i < numEntries; i++ {
			start := i * txEntrySize
			num := byteOrder.Uint32(data[start:])
			_, _ = levelBuf.WriteString(fmt.Sprintf("%02d ", num))
		}
		for i := numEntries; i < maxEntries; i++ {
			_, _ = levelBuf.WriteString("_  ")
		}
		_, _ = levelBuf.WriteString("\n")
		maxEntries *= 2
	}

	return levelBuf.String()
}

// sanityCheck ensures that all data stored in the bucket for the given address
// adheres to the level-based rules described by the address index
// documentation.
func (b *addrIndexBucket) sanityCheck(addrKey [addrKeySize]byte, expectedTotal int) error {
	// Find the highest level for the key.
	highestLevel := uint8(0)
	for k := range b.levels {
		if !bytes.Equal(k[:levelOffset], addrKey[:]) {
			continue
		}
		level := uint8(k[levelOffset])
		if level > highestLevel {
			highestLevel = level
		}
	}

	// Ensure the expected total number of entries are present and that
	// all levels adhere to the rules described in the address index
	// documentation.
	var totalEntries int
	maxEntries := level0MaxEntries
	for level := uint8(0); level <= highestLevel; level++ {
		// Level 0 can'have more entries than the max allowed if the
		// levels after it have data and it can't be empty.  All other
		// levels must either be half full or full.
		data := b.levels[keyForLevel(addrKey, level)]
		numEntries := len(data) / txEntrySize
		totalEntries += numEntries
		if level == 0 {
			if (highestLevel != 0 && numEntries == 0) ||
				numEntries > maxEntries {

				return fmt.Errorf("level %d has %d entries",
					level, numEntries)
			}
		} else if numEntries != maxEntries && numEntries != maxEntries/2 {
			return fmt.Errorf("level %d has %d entries", level,
				numEntries)
		}
		maxEntries *= 2
	}
	if totalEntries != expectedTotal {
		return fmt.Errorf("expected %d entries - got %d", expectedTotal,
			totalEntries)
	}

	// Ensure all of the numbers are in order starting from the highest
	// level moving to the lowest level.
	expectedNum := uint32(0)
	for level := highestLevel + 1; level > 0; level-- {
		data := b.levels[keyForLevel(addrKey, level)]
		numEntries := len(data) / txEntrySize
		for i := 0; i < numEntries; i++ {
			start := i * txEntrySize
			num := byteOrder.Uint32(data[start:])
			if num != expectedNum {
				return fmt.Errorf("level %d offset %d does "+
					"not contain the expected number of "+
					"%d - got %d", level, i, num,
					expectedNum)
			}
			expectedNum++
		}
	}

	return nil
}

// TestAddrIndexLevels ensures that adding and deleting entries to the address
// index creates multiple levels as described by the address index
// documentation.
func TestAddrIndexLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		key         [addrKeySize]byte
		numInsert   int
		printLevels bool // Set to help debug a specific test.
	}{
		{
			name:      "level 0 not full",
			numInsert: level0MaxEntries - 1,
		},
		{
			name:      "level 1 half",
			numInsert: level0MaxEntries + 1,
		},
		{
			name:      "level 1 full",
			numInsert: level0MaxEntries*2 + 1,
		},
		{
			name:      "level 2 half, level 1 half",
			numInsert: level0MaxEntries*3 + 1,
		},
		{
			name:      "level 2 half, level 1 full",
			numInsert: level0MaxEntries*4 + 1,
		},
		{
			name:      "level 2 full, level 1 half",
			numInsert: level0MaxEntries*5 + 1,
		},
		{
			name:      "level 2 full, level 1 full",
			numInsert: level0MaxEntries*6 + 1,
		},
		{
			name:      "level 3 half, level 2 half, level 1 half",
			numInsert: level0MaxEntries*7 + 1,
		},
		{
			name:      "level 3 full, level 2 half, level 1 full",
			numInsert: level0MaxEntries*12 + 1,
		},
	}

nextTest:
	for testNum, test := range tests {
		// Insert entries in order.
		populatedBucket := &addrIndexBucket{
			levels: make(map[[levelKeySize]byte][]byte),
		}
		for i := 0; i < test.numInsert; i++ {
			txLoc := protos.TxLoc{TxStart: i * 2}
			err := dbPutAddrIndexEntry(populatedBucket, test.key,
				uint32(i), database.BlockNormal, txLoc)
			if err != nil {
				t.Errorf("dbPutAddrIndexEntry #%d (%s) - "+
					"unexpected error: %v", testNum,
					test.name, err)
				continue nextTest
			}
		}
		if test.printLevels {
			t.Log(populatedBucket.printLevels(test.key))
		}

		// Delete entries from the populated bucket until all entries
		// have been deleted.  The bucket is reset to the fully
		// populated bucket on each iteration so every combination is
		// tested.  Notice the upper limit purposes exceeds the number
		// of entries to ensure attempting to delete more entries than
		// there are works correctly.
		for numDelete := 0; numDelete <= test.numInsert+1; numDelete++ {
			// Clone populated bucket to run each delete against.
			bucket := populatedBucket.Clone()

			// Remove the number of entries for this iteration.
			err := dbRemoveAddrIndexEntries(bucket, test.key,
				numDelete)
			if err != nil {
				if numDelete <= test.numInsert {
					t.Errorf("dbRemoveAddrIndexEntries (%s) "+
						" delete %d - unexpected error: "+
						"%v", test.name, numDelete, err)
					continue nextTest
				}
			}
			if test.printLevels {
				t.Log(bucket.printLevels(test.key))
			}

			// Sanity check the levels to ensure the adhere to all
			// rules.
			numExpected := test.numInsert
			if numDelete <= test.numInsert {
				numExpected -= numDelete
			}
			err = bucket.sanityCheck(test.key, numExpected)
			if err != nil {
				t.Errorf("sanity check fail (%s) delete %d: %v",
					test.name, numDelete, err)
				continue nextTest
			}
		}
	}
}

func TestConnectBlock(t *testing.T)  {
	t.Parallel()
	tx := mock.NewMockTx()
	addrBucket, _ := tx.Metadata().CreateBucket(addrIndexKey)
	txidBucket, _ := tx.Metadata().CreateBucket(idByHashIndexBucketName)

	addrIndex := NewAddrIndex(nil)

	stxo := make([]txo.SpentTxOut, 0)
	pblock := protos.MsgBlock{}
	pblock.Header.Height = 101

	normalAddress0, _ := common.NewAddressWithId(common.PubKeyHashAddrID, []byte{01,02,03,04})
	normalAddress1, _ := common.NewAddressWithId(common.PubKeyHashAddrID, []byte{01,01,01,01})
	normalAddress2, _ := common.NewAddressWithId(common.PubKeyHashAddrID, []byte{02,02,02,02})
	normalAddress3, _ := common.NewAddressWithId(common.PubKeyHashAddrID, []byte{03,03,03,03})
	contractAddress, _ := common.NewAddressWithId(common.ContractHashAddrID, []byte{11,11,11,11})

	// add some tx into pblock
	for i:= 0; i < 4; i++ {
		msgtx0 := protos.NewMsgTx(0)
		msgtx0.AddTxIn(&protos.TxIn{
			PreviousOutPoint:protos.OutPoint{
				Hash: common.BytesToHash([]byte{01}),
				Index:uint32(i),
			},
		})
		pkscript1, _ := txscript.PayToAddrScript(normalAddress1)
		msgtx0.AddTxOut(&protos.TxOut{
			Value:    100,
			Asset:    asiutil.AsimovAsset,
			PkScript: pkscript1,
		})
		pblock.AddTransaction(msgtx0)

		msgtx1 := protos.NewMsgTx(0)
		msgtx1.AddTxIn(&protos.TxIn{
			PreviousOutPoint:protos.OutPoint{
				Hash: common.BytesToHash([]byte{02}),
				Index:uint32(i),
			},
		})
		pkscriptc0, _ := txscript.PayToAddrScript(contractAddress)
		msgtx1.AddTxOut(&protos.TxOut{
			Value:    200,
			Asset:    asiutil.AsimovAsset,
			PkScript: pkscriptc0,
		})
		pblock.AddTransaction(msgtx1)
	}

	coinbase := protos.NewMsgTx(0)
	coinbase.AddTxIn(&protos.TxIn{
		PreviousOutPoint:protos.OutPoint{
			Hash: common.Hash{},
		},
	})
	pkscript, _ := txscript.PayToAddrScript(normalAddress3)
	coinbase.AddTxOut(&protos.TxOut{
		Value:    10000,
		Asset:    asiutil.AsimovAsset,
		PkScript: pkscript,
	})
	pblock.AddTransaction(coinbase)

	block := asiutil.NewBlock(&pblock)
	pvblock := protos.MsgVBlock{}
	vblock := asiutil.NewVBlock(&pvblock, block.Hash())
	// add some vtx into vpblock and fill stxo
	for i:= 0; i < 4; i++ {
		msgtx0 := protos.NewMsgTx(uint32(i*2 + 1))
		msgtx0.AddTxIn(&protos.TxIn{
			PreviousOutPoint:protos.OutPoint{
				Hash: common.BytesToHash([]byte{03}),
				Index:uint32(i),
			},
		})
		pkscriptc0, _ := txscript.PayToAddrScript(contractAddress)
		pkscript0, _ := txscript.PayToAddrScript(normalAddress0)
		pkscript2, _ := txscript.PayToAddrScript(normalAddress2)
		msgtx0.AddTxOut(&protos.TxOut{
			Value:    300,
			Asset:    asiutil.AsimovAsset,
			PkScript: pkscript2,
		})
		pvblock.AddTransaction(msgtx0)

		stxo = append(stxo, txo.SpentTxOut{
			Amount:   100,
			Height:   11,
			Asset:    &asiutil.AsimovAsset,
			PkScript: pkscript0,
		})
		stxo = append(stxo, txo.SpentTxOut{
			Amount:   200,
			Height:   22,
			Asset:    &asiutil.AsimovAsset,
			PkScript: pkscript2,
		})
		stxo = append(stxo, txo.SpentTxOut{
			Amount:   300,
			Height:   33,
			Asset:    &asiutil.AsimovAsset,
			PkScript: pkscriptc0,
		})
	}

	height := 111
	txidBucket.Put(block.Hash().Bytes(), []byte{111,0,0,0})


	err := addrIndex.ConnectBlock(tx, block, stxo, vblock)
	if err != nil {
		t.Errorf("connect block failed: %v", err)
		return
	}

	blockLoc, _ := block.TxLoc()
	vblockLoc, _ := vblock.TxLoc()

	type region struct{
		blocktype byte
		txLoc     protos.TxLoc
	}
	tests := []struct {
		key   [levelKeySize]byte
		values []region
	}{
		{
			key:keyForLevel(*normalAddress0, 0),
			values: []region {
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[0],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[2],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[4],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[6],
				},
			},
		},
		{
			key:keyForLevel(*normalAddress1, 0),
			values: []region {
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[0],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[2],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[4],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[6],
				},
			},
		},
		{
			key:keyForLevel(*normalAddress2, 0),
			values: []region {
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[1],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[3],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[5],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[7],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[0],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[1],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[2],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[3],
				},
			},
		},
		{
			key:keyForLevel(*contractAddress, 0),
			values: []region {
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[1],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[3],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[5],
				},
				{
					blocktype: byte(database.BlockNormal),
					txLoc:     blockLoc[7],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[0],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[1],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[2],
				},
				{
					blocktype: byte(database.BlockVirtual),
					txLoc:     vblockLoc[3],
				},
			},
		},
	}

	for i, test := range tests {
		addrBytes := addrBucket.Get(test.key[:])
		if len(addrBytes) != txEntrySize * len(test.values) {
			t.Errorf("#%d, txEntry length invalid, actual %d, expected %d",
				i, len(addrBytes), txEntrySize * len(test.values))
			fmt.Println(addrBytes)
			continue
		}
		offset := 0
		for j, v := range test.values {
			serialized := addrBytes[offset:offset + txEntrySize]
			offset += txEntrySize
			if byteOrder.Uint32(serialized[0:4]) != uint32(height) {
				t.Errorf("#%d, txEntry %d height invalid, actual %d, expected %d",
					i, j, byteOrder.Uint32(serialized[0:4]), height)
				break
			}
			if serialized[4] != v.blocktype {
				t.Errorf("#%d, txEntry %d blocktype invalid", i, j)
				break
			}
			if byteOrder.Uint32(serialized[5:9]) != uint32(v.txLoc.TxStart) {
				t.Errorf("#%d, txEntry %d TxStart invalid", i, j)
				break
			}
			if byteOrder.Uint32(serialized[9:13]) != uint32(v.txLoc.TxLen) {
				t.Errorf("#%d, txEntry %d TxLen invalid", i, j)
				break
			}
		}
	}
}

func TestSerializeAddrIndexEntry(t *testing.T)  {
	blkID := uint32(1)
	blkType := database.BlockNormal
	txLoc := protos.TxLoc{
		TxStart:100,
		TxLen:300,
	}

	addrIdxEntryEncoded := []byte{
		0x01, 0x00, 0x00, 0x00, // blkID
		0x00,                   // blkType
		0x64, 0x00, 0x00, 0x00, // TxStart
		0x2c, 0x01, 0x00, 0x00, // TxLen
	}

	tests := []struct {
		inputblkID uint32
		inputblkType database.BlockType
		inputtxLoc protos.TxLoc
		buf []byte       // Serialized data
	}{
		{
			blkID,
			blkType,
			txLoc,
			addrIdxEntryEncoded,
		},
	}

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		// SerializeAddrIndexEntry.
		buf := serializeAddrIndexEntry(test.inputblkID, test.inputblkType, test.inputtxLoc)
		if !bytes.Equal(buf, test.buf) {
			t.Errorf("Serialize #%d\n got: %v want: %v", i,
				buf, test.buf)
			continue
		}
	}
}
