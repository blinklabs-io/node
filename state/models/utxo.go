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
	"errors"
	"math/big"

	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/node/database"
	"github.com/dgraph-io/badger/v4"
	"gorm.io/gorm"
)

type Utxo struct {
	ID          uint `gorm:"primarykey"`
	TxId        []byte
	OutputIdx   uint32
	AddedSlot   uint64
	DeletedSlot uint64
	PaymentKey  []byte
	StakingKey  []byte
	Cbor        []byte `gorm:"-"` // This is here for convenience but not represented in the metadata DB
}

func (u Utxo) Decode() (ledger.TransactionOutput, error) {
	return ledger.NewTransactionOutputFromCbor(u.Cbor)
}

func (u *Utxo) loadCbor(badgerDb *badger.DB) error {
	key := UtxoBlobKey(u.TxId, u.OutputIdx)
	err := badgerDb.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		u.Cbor, err = item.ValueCopy(nil)
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

func UtxoByRef(db database.Database, txId []byte, outputIdx uint32) (Utxo, error) {
	var tmpUtxo Utxo
	result := db.Metadata().First(&tmpUtxo, "tx_id = ? AND output_idx = ?", txId, outputIdx)
	if err := tmpUtxo.loadCbor(db.Blob()); err != nil {
		return tmpUtxo, err
	}
	return tmpUtxo, result.Error
}

func UtxosByAddress(db database.Database, addr ledger.Address) ([]Utxo, error) {
	var ret []Utxo
	// Build sub-query for address
	var addrQuery *gorm.DB
	if addr.PaymentKeyHash() != ledger.NewBlake2b224(nil) {
		addrQuery = db.Metadata().Where("payment_key = ?", addr.PaymentKeyHash().Bytes())
	}
	if addr.StakeKeyHash() != ledger.NewBlake2b224(nil) {
		if addrQuery != nil {
			addrQuery = addrQuery.Or("staking_key = ?", addr.StakeKeyHash().Bytes())
		} else {
			addrQuery = db.Metadata().Where("staking_key = ?", addr.StakeKeyHash().Bytes())
		}
	}
	result := db.Metadata().Where("deleted_slot = 0").Where(addrQuery).Find(&ret)
	if result.Error != nil {
		return nil, result.Error
	}
	// Load CBOR from blob DB for each UTxO
	for idx, tmpUtxo := range ret {
		if err := tmpUtxo.loadCbor(db.Blob()); err != nil {
			return nil, err
		}
		ret[idx] = tmpUtxo
	}
	return ret, nil
}

func UtxoDelete(db database.Database, utxo Utxo) error {
	// Remove from metadata DB
	if result := db.Metadata().Delete(&utxo); result.Error != nil {
		return result.Error
	}
	// Remove from blob DB
	key := UtxoBlobKey(utxo.TxId, utxo.OutputIdx)
	err := db.Blob().Update(func(txn *badger.Txn) error {
		err := txn.Delete(key)
		return err
	})
	if err != nil {
		return err
	}
	return nil
}

func UtxoDeleteByRef(db database.Database, txId []byte, outputIdx uint32) error {
	utxo, err := UtxoByRef(db, txId, outputIdx)
	if err != nil {
		return err
	}
	return UtxoDelete(db, utxo)
}

func UtxoBlobKey(txId []byte, outputIdx uint32) []byte {
	key := []byte("u")
	key = append(key, txId...)
	// Convert index to bytes
	idxBytes := make([]byte, 4)
	new(big.Int).SetUint64(uint64(outputIdx)).FillBytes(idxBytes)
	key = append(key, idxBytes...)
	return key
}
