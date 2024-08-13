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
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/blinklabs-io/node/database"
	"github.com/blinklabs-io/node/event"
	"github.com/blinklabs-io/node/state/models"
	"gorm.io/gorm"

	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	badger "github.com/dgraph-io/badger/v4"
)

type LedgerState struct {
	sync.RWMutex
	logger   *slog.Logger
	dataDir  string
	db       database.Database
	eventBus *event.EventBus
}

func NewLedgerState(dataDir string, eventBus *event.EventBus, logger *slog.Logger) (*LedgerState, error) {
	ls := &LedgerState{
		dataDir:  dataDir,
		logger:   logger,
		eventBus: eventBus,
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
	// Setup event handlers
	ls.eventBus.SubscribeFunc(ChainsyncEventType, ls.handleEventChainSync)
	return ls, nil
}

func (ls *LedgerState) handleEventChainSync(evt event.Event) {
	ls.Lock()
	defer ls.Unlock()
	e := evt.Data.(ChainsyncEvent)
	if e.Rollback {
		// Remove rolled-back blocks in reverse order
		var tmpBlocks []models.Block
		result := ls.db.Metadata().Where("slot > ?", e.Point.Slot).Order("slot DESC").Find(&tmpBlocks)
		if result.Error != nil {
			ls.logger.Error(
				fmt.Sprintf("failed to query blocks from ledger: %s", result.Error),
			)
			return
		}
		for _, tmpBlock := range tmpBlocks {
			if err := ls.removeBlock(tmpBlock); err != nil {
				ls.logger.Error(
					fmt.Sprintf("failed to remove block: %s", err),
				)
				return
			}
		}
		// Generate event
		ls.eventBus.Publish(
			ChainRollbackEventType,
			event.NewEvent(
				ChainRollbackEventType,
				ChainRollbackEvent{
					Point: e.Point,
				},
			),
		)
		ls.logger.Info(fmt.Sprintf(
			"chain rolled back, new tip: %x at slot %d",
			e.Point.Hash,
			e.Point.Slot,
		))
	} else {
		// Add block to database
		tmpBlock := models.Block{
			Slot: e.Point.Slot,
			Hash: e.Point.Hash,
			// TODO: figure out something for Byron. this won't work, since the
			// block number isn't stored in the block itself
			Number: e.Block.BlockNumber(),
			Type:   e.Type,
			Cbor:   e.Block.Cbor(),
		}
		if err := ls.AddBlock(tmpBlock); err != nil {
			ls.logger.Error(
				fmt.Sprintf("failed to add block to ledger state: %s", err),
			)
			return
		}
		// Generate event
		ls.eventBus.Publish(
			ChainBlockEventType,
			event.NewEvent(
				ChainBlockEventType,
				ChainBlockEvent{
					Point: e.Point,
					Block: tmpBlock,
				},
			),
		)
		ls.logger.Info(fmt.Sprintf(
			"chain extended, new tip: %s at slot %d",
			e.Block.Hash(),
			e.Block.SlotNumber(),
		))
	}
}

func (ls *LedgerState) AddBlock(block models.Block) error {
	// Add block to blob DB
	key := models.BlockBlobKey(block.Slot, block.Hash)
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

func (ls *LedgerState) removeBlock(block models.Block) error {
	// Remove from metadata DB
	if result := ls.db.Metadata().Delete(&block); result.Error != nil {
		return result.Error
	}
	// Remove from blob DB
	key := models.BlockBlobKey(block.Slot, block.Hash)
	err := ls.db.Blob().Update(func(txn *badger.Txn) error {
		err := txn.Delete(key)
		return err
	})
	if err != nil {
		return err
	}
	return nil
}

func (ls *LedgerState) GetBlock(point ocommon.Point) (*models.Block, error) {
	ret, err := models.BlockByPoint(ls.db, point)
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

// RecentChainPoints returns the requested count of recent chain points in descending order. This is used mostly
// for building a set of intersect points when acting as a chainsync client
func (ls *LedgerState) RecentChainPoints(count int) ([]ocommon.Point, error) {
	ls.RLock()
	defer ls.RUnlock()
	var tmpBlocks []models.Block
	result := ls.db.Metadata().Order("number DESC").Limit(count).Find(&tmpBlocks)
	if result.Error != nil {
		return nil, result.Error
	}
	var ret []ocommon.Point
	for _, tmpBlock := range tmpBlocks {
		ret = append(
			ret,
			ocommon.NewPoint(tmpBlock.Slot, tmpBlock.Hash),
		)
	}
	return ret, nil
}

func (ls *LedgerState) GetIntersectPoint(points []ocommon.Point) (*ocommon.Point, error) {
	var ret ocommon.Point
	for _, point := range points {
		// Ignore points with a slot earlier than an existing match
		if point.Slot < ret.Slot {
			continue
		}
		// Lookup block in metadata DB
		tmpBlock, err := models.BlockByPoint(ls.db, point)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		// Update return value
		ret.Slot = tmpBlock.Slot
		ret.Hash = tmpBlock.Hash
	}
	if ret.Slot > 0 {
		return &ret, nil
	}
	return nil, nil
}

func (ls *LedgerState) GetChainFromPoint(point ocommon.Point) (*ChainIterator, error) {
	return newChainIterator(ls, point)
}

func (ls *LedgerState) Tip() (ochainsync.Tip, error) {
	var ret ochainsync.Tip
	var tmpBlock models.Block
	result := ls.db.Metadata().Order("number DESC").First(&tmpBlock)
	if result.Error != nil {
		return ret, result.Error
	}
	ret = ochainsync.Tip{
		Point:       ocommon.NewPoint(tmpBlock.Slot, tmpBlock.Hash),
		BlockNumber: tmpBlock.Number,
	}
	return ret, nil
}
