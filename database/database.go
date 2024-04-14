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

package database

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/blinklabs-io/node/database/immutable"
	"github.com/blinklabs-io/node/database/models"

	"github.com/blinklabs-io/gouroboros/ledger"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type Database struct {
	logger      *slog.Logger
	dataDir     string
	metadata    *gorm.DB
	blob        *badger.DB
	blobGcTimer *time.Ticker
	immutable   *immutable.ImmutableDb
}

func New(dataDir string, logger *slog.Logger) (*Database, error) {
	if logger == nil {
		// Create logger to throw away logs
		// We do this so we don't have to add guards around every log operation
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	// Make sure that we can read data dir, and create if it doesn't exist
	if _, err := os.Stat(dataDir); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to read data dir: %w", err)
		}
		// Create data directory
		if err := os.MkdirAll(dataDir, fs.ModePerm); err != nil {
			return nil, fmt.Errorf("failed to create data dir: %w", err)
		}
	}
	// Open sqlite DB
	metadataDbPath := filepath.Join(
		dataDir,
		"metadata.sqlite",
	)
	metadataDb, err := gorm.Open(sqlite.Open(metadataDbPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	// Create the table schemas
	if err := metadataDb.AutoMigrate(&models.Block{}); err != nil {
		return nil, err
	}
	// Open immutable DB
	immutableDir := filepath.Join(
		dataDir,
		"immutable",
	)
	immutable, err := immutable.New(immutableDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to read immutable DB: %w", err)
		}
	}
	// Open Badger DB
	blobDir := filepath.Join(
		dataDir,
		"blob",
	)
	blobDb, err := badger.Open(badger.DefaultOptions(blobDir))
	if err != nil {
		return nil, err
	}
	d := &Database{
		dataDir:   dataDir,
		metadata:  metadataDb,
		blob:      blobDb,
		immutable: immutable,
		logger:    logger,
	}
	// TODO: move this to a subcommand
	/*
		if d.immutable != nil {
			if err := d.copyBlocksFromImmutableDb(); err != nil {
				return nil, err
			}
		}
	*/
	// Run GC periodically for Badger DB
	d.blobGcTimer = time.NewTicker(5 * time.Minute)
	go func() {
		for range d.blobGcTimer.C {
		again:
			err := d.blob.RunValueLogGC(0.5)
			if err != nil {
				// Log any actual errors
				if !errors.Is(err, badger.ErrNoRewrite) {
					logger.Warn(
						fmt.Sprintf("blob DB: GC failure: %s", err),
					)
				}
			} else {
				// Run it again if it just ran successfully
				goto again
			}
		}
	}()
	return d, nil
}

func (d *Database) copyBlocksFromImmutableDb() error {
	// Get last block in immutable DB
	tip, err := d.immutable.GetTip()
	if err != nil {
		return err
	}
	// Check our DB for the tip point
	blk, err := d.GetBlock(*tip)
	if err != nil {
		return err
	}
	if blk != nil {
		return nil
	}
	// Copy all blocks
	d.logger.Info("copying blocks from immutable DB")
	iter, err := d.immutable.BlocksFromPoint(ocommon.NewPointOrigin())
	if err != nil {
		return nil
	}
	wb := d.blob.NewWriteBatch()
	defer wb.Cancel()
	var byronBlockNumber uint64
	var blocksCopied int
	//var blockBatch []models.Block
	blocksWaiting := 0
	for {
		next, err := iter.Next()
		if err != nil {
			return err
		}
		if next == nil {
			if blocksWaiting > 0 {
				/*
					if result := d.metadata.Create(&blockBatch); result.Error != nil {
						return result.Error
					}
				*/
				if err := wb.Flush(); err != nil {
					return err
				}
			}
			break
		}
		tmpBlock, err := ledger.NewBlockFromCbor(next.Type, next.Cbor)
		if err != nil {
			return err
		}
		blockNumber := tmpBlock.BlockNumber()
		if blockNumber == 0 && !next.IsEbb {
			byronBlockNumber++
			blockNumber = byronBlockNumber
		}
		/*
			dbBlock := models.Block{
				Number: blockNumber,
				Slot:   next.Slot,
				Hash:   next.Hash[:],
				Cbor:   next.Cbor[:],
			}
		*/
		//blockBatch = append(blockBatch, dbBlock)
		key := pointToBadgerKey(next.Slot, next.Hash[:])
		if err := wb.Set(key, next.Cbor[:]); err != nil {
			return err
		}
		//blocksWaiting++
		if blocksWaiting >= 100 {
			/*
				if result := d.metadata.Create(&blockBatch); result.Error != nil {
					return result.Error
				}
			*/
			if err := wb.Flush(); err != nil {
				return err
			}
			blocksWaiting = 0
		}
		/*
			if err := d.AddBlock(dbBlock); err != nil {
				return err
			}
		*/
		blocksCopied++
		if blocksCopied > 0 && blocksCopied%10000 == 0 {
			d.logger.Info(
				fmt.Sprintf("copying blocks from immutable DB (%d blocks copied)", blocksCopied),
			)
		}
	}
	if err := wb.Flush(); err != nil {
		return err
	}
	d.logger.Info("finished copying blocks from immutable DB")
	return nil
}

func (d *Database) AddBlock(block models.Block) error {
	// Add block to blob DB
	slotBytes := make([]byte, 8)
	new(big.Int).SetUint64(block.Slot).FillBytes(slotBytes)
	key := []byte("b")
	key = append(key, slotBytes...)
	key = append(key, block.Hash...)
	//fmt.Printf("key = %q (%x)\n", key, key)
	err := d.blob.Update(func(txn *badger.Txn) error {
		err := txn.Set(key, block.Cbor)
		return err
	})
	if err != nil {
		return err
	}
	// Add to metadata DB
	if result := d.metadata.Create(&block); result.Error != nil {
		return result.Error
	}
	return nil
}

func (d *Database) GetBlock(point ocommon.Point) (*models.Block, error) {
	var ret = models.Block{
		Slot: point.Slot,
		Hash: point.Hash,
	}
	/*
		result := d.metadata.First(&ret, "slot = ? AND hash = ?", point.Slot, point.Hash)
		if result.Error != nil {
			if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return nil, result.Error
			}
			return nil, nil
		}
	*/
	slotBytes := make([]byte, 8)
	new(big.Int).SetUint64(point.Slot).FillBytes(slotBytes)
	key := []byte("b")
	key = append(key, slotBytes...)
	key = append(key, point.Hash...)
	err := d.blob.View(func(txn *badger.Txn) error {
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
	// TODO: build return value
	return &ret, nil
}

func pointToBadgerKey(slot uint64, hash []byte) []byte {
	slotBytes := make([]byte, 8)
	new(big.Int).SetUint64(slot).FillBytes(slotBytes)
	key := []byte("b")
	key = append(key, slotBytes...)
	key = append(key, hash...)
	return key
}

/*
func (d *Database) BlocksFromPoint(point ocommon.Point) (*BlockIterator, error) {
	var tmpBlock *models.Block
	result := d.db.First(tmpBlock, "slot = ? AND hash = ?", point.Slot, point.Hash)
	if result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, result.Error
		}
	}
	// TODO
		chunkNames, err := i.getChunkNamesFromPoint(point)
		if err != nil {
			return nil, err
		}
		ret := &BlockIterator{
			db:         i,
			chunkNames: chunkNames[:],
			startPoint: point,
		}
	return ret, nil
}

// TODO
type BlockIterator struct {
	db                *Database
	startPoint        ocommon.Point
	currentSlot       uint64
	immutableIterator *immutable.BlockIterator
}
*/
