// Copyright 2024 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/blinklabs-io/gouroboros/ledger"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/node/database"
	"github.com/dgraph-io/badger/v4"
)

type Block struct {
	ID     uint `gorm:"primarykey"`
	Slot   uint64
	Number uint64
	Hash   []byte
	Type   uint
	Cbor   []byte `gorm:"-"` // This is here for convenience but not represented in the metadata DB
}

func (b Block) Decode() (ledger.Block, error) {
	return ledger.NewBlockFromCbor(b.Type, b.Cbor)
}

func (b *Block) loadCbor(badgerDb *badger.DB) error {
	key := BlockBlobKey(b.Slot, b.Hash)
	err := badgerDb.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		b.Cbor, err = item.ValueCopy(nil)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func BlockByPoint(db database.Database, point ocommon.Point) (Block, error) {
	var tmpBlock Block
	result := db.Metadata().First(&tmpBlock, "slot = ? AND hash = ?", point.Slot, point.Hash)
	if err := tmpBlock.loadCbor(db.Blob()); err != nil {
		return tmpBlock, err
	}
	return tmpBlock, result.Error
}

func BlockByNumber(db database.Database, blockNumber uint64) (Block, error) {
	var tmpBlock Block
	result := db.Metadata().First(&tmpBlock, "number = ?", blockNumber)
	if err := tmpBlock.loadCbor(db.Blob()); err != nil {
		return tmpBlock, err
	}
	return tmpBlock, result.Error
}

func BlockBlobKey(slot uint64, hash []byte) []byte {
	key := []byte("b")
	// Convert slot to bytes
	slotBytes := make([]byte, 8)
	new(big.Int).SetUint64(slot).FillBytes(slotBytes)
	key = append(key, slotBytes...)
	key = append(key, hash...)
	return key
}

func BlockBlobKeyHashHex(slot uint64, hashHex string) ([]byte, error) {
	hashBytes, err := hex.DecodeString(hashHex)
	if err != nil {
		return nil, err
	}
	return BlockBlobKey(slot, hashBytes), nil
}
