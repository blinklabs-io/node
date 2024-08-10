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

package state

import (
	"errors"
	"io"
	"log/slog"
	"math/big"

	"github.com/blinklabs-io/node/database"
	"github.com/blinklabs-io/node/state/models"
	"gorm.io/gorm"

	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	badger "github.com/dgraph-io/badger/v4"
)

type LedgerState struct {
	logger  *slog.Logger
	dataDir string
	db      database.Database
}

func NewLedgerState(dataDir string, logger *slog.Logger) (*LedgerState, error) {
	ls := &LedgerState{
		dataDir: dataDir,
		logger:  logger,
	}
	if logger == nil {
		// Create logger to throw away logs
		// We do this so we don't have to add guards around every log operation
		ls.logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if dataDir == "" {
		db, err := database.NewInMemory(ls.logger)
		if err != nil {
			return nil, err
		}
		ls.db = db
	} else {
		db, err := database.NewPersistent(dataDir, ls.logger)
		if err != nil {
			return nil, err
		}
		ls.db = db
	}
	// Create the table schemas
	if err := ls.db.Metadata().AutoMigrate(&models.Block{}); err != nil {
		return nil, err
	}
	return ls, nil
}

func (ls *LedgerState) AddBlock(block models.Block) error {
	// Add block to blob DB
	slotBytes := make([]byte, 8)
	new(big.Int).SetUint64(block.Slot).FillBytes(slotBytes)
	key := []byte("b")
	key = append(key, slotBytes...)
	key = append(key, block.Hash...)
	err := ls.db.Blob().Update(func(txn *badger.Txn) error {
		err := txn.Set(key, block.Cbor)
		return err
	})
	if err != nil {
		return err
	}
	// Add to metadata DB
	if result := ls.db.Metadata().Create(&block); result.Error != nil {
		return result.Error
	}
	return nil
}

func (ls *LedgerState) GetBlock(point ocommon.Point) (*models.Block, error) {
	var ret models.Block
	// Block metadata
	result := ls.db.Metadata().First(&ret, "slot = ? AND hash = ?", point.Slot, point.Hash)
	if result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, result.Error
		}
		return nil, nil
	}
	// Block CBOR
	slotBytes := make([]byte, 8)
	new(big.Int).SetUint64(point.Slot).FillBytes(slotBytes)
	key := []byte("b")
	key = append(key, slotBytes...)
	key = append(key, point.Hash...)
	err := ls.db.Blob().View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		ret.Cbor, err = item.ValueCopy(nil)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, nil
		}
	}
	return &ret, nil
}
