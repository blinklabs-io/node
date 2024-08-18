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
	"time"

	"github.com/blinklabs-io/node/database"
	"github.com/blinklabs-io/node/event"
	"github.com/blinklabs-io/node/state/models"
	"gorm.io/gorm"

	"github.com/blinklabs-io/gouroboros/ledger"
	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	badger "github.com/dgraph-io/badger/v4"
)

const (
	cleanupConsumedUtxosInterval   = 15 * time.Minute
	cleanupConsumedUtxosSlotWindow = 50000 // TODO: calculate this from params
)

type LedgerState struct {
	sync.RWMutex
	logger                    *slog.Logger
	dataDir                   string
	db                        database.Database
	eventBus                  *event.EventBus
	timerCleanupConsumedUtxos *time.Timer
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
	for _, model := range models.MigrateModels {
		if err := ls.db.Metadata().AutoMigrate(model); err != nil {
			return nil, err
		}
	}
	// Setup event handlers
	ls.eventBus.SubscribeFunc(ChainsyncEventType, ls.handleEventChainSync)
	// Schedule periodic process to purge consumed UTxOs outside of the rollback window
	ls.scheduleCleanupConsumedUtxos()
	// TODO: schedule process to scan/clean blob DB for keys that don't have a corresponding metadata DB entry
	return ls, nil
}

func (ls *LedgerState) scheduleCleanupConsumedUtxos() {
	ls.Lock()
	defer ls.Unlock()
	if ls.timerCleanupConsumedUtxos != nil {
		ls.timerCleanupConsumedUtxos.Stop()
	}
	ls.timerCleanupConsumedUtxos = time.AfterFunc(
		cleanupConsumedUtxosInterval,
		func() {
			// Schedule the next run when we finish
			defer ls.scheduleCleanupConsumedUtxos()
			// Get the current tip, since we're querying by slot
			tip, err := ls.Tip()
			if err != nil {
				ls.logger.Error(
					fmt.Sprintf("failed to get tip: %s", err),
				)
				return
			}
			// Get UTxOs that are marked as deleted and older than our slot window
			var tmpUtxos []models.Utxo
			result := ls.db.Metadata().Where("deleted_slot <= ?", tip.Point.Slot-cleanupConsumedUtxosSlotWindow).Order("id DESC").Find(&tmpUtxos)
			if result.Error != nil {
				ls.logger.Error(
					fmt.Sprintf("failed to query consumed UTxOs: %s", result.Error),
				)
				return
			}
			// Delete the UTxOs
			for _, utxo := range tmpUtxos {
				if err := models.UtxoDelete(ls.db, utxo); err != nil {
					ls.logger.Error(
						fmt.Sprintf("failed to remove consumed UTxO: %s", err),
					)
					return
				}
			}
		},
	)
}

func (ls *LedgerState) handleEventChainSync(evt event.Event) {
	ls.Lock()
	defer ls.Unlock()
	e := evt.Data.(ChainsyncEvent)
	if e.Rollback {
		if err := ls.handleEventChainSyncRollback(e); err != nil {
			// TODO: actually handle this error
			ls.logger.Error(
				fmt.Sprintf("failed to handle rollback: %s", err),
			)
			return
		}
	} else {
		if err := ls.handleEventChainSyncBlock(e); err != nil {
			// TODO: actually handle this error
			ls.logger.Error(
				fmt.Sprintf("failed to handle block: %s", err),
			)
			return
		}
	}
}

func (ls *LedgerState) handleEventChainSyncRollback(e ChainsyncEvent) error {
	// Remove rolled-back blocks in reverse order
	var tmpBlocks []models.Block
	result := ls.db.Metadata().Where("slot > ?", e.Point.Slot).Order("slot DESC").Find(&tmpBlocks)
	if result.Error != nil {
		return fmt.Errorf("query blocks: %w", result.Error)
	}
	for _, tmpBlock := range tmpBlocks {
		if err := ls.removeBlock(tmpBlock); err != nil {
			return fmt.Errorf("remove block: %w", err)
		}
	}
	// Delete rolled-back UTxOs
	var tmpUtxos []models.Utxo
	result = ls.db.Metadata().Where("added_slot > ?", e.Point.Slot).Order("id DESC").Find(&tmpUtxos)
	if result.Error != nil {
		return fmt.Errorf("remove rolled-backup UTxOs: %w", result.Error)
	}
	for _, utxo := range tmpUtxos {
		if err := models.UtxoDelete(ls.db, utxo); err != nil {
			return fmt.Errorf("remove rolled-back UTxO: %w", err)
		}
	}
	// Restore spent UTxOs
	result = ls.db.Metadata().Model(models.Utxo{}).Where("deleted_slot > ?", e.Point.Slot).Update("deleted_slot", 0)
	if result.Error != nil {
		return fmt.Errorf("restore spent UTxOs after rollback: %w", result.Error)
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
	return nil
}

func (ls *LedgerState) handleEventChainSyncBlock(e ChainsyncEvent) error {
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
		return fmt.Errorf("add block: %w", err)
	}
	// Process transactions
	for _, tx := range e.Block.Transactions() {
		// Process consumed UTxOs
		for _, consumed := range tx.Consumed() {
			if err := ls.consumeUtxo(consumed, e.Point.Slot); err != nil {
				return fmt.Errorf("remove consumed UTxO: %w", err)
			}
		}
		// Process produced UTxOs
		for _, produced := range tx.Produced() {
			outAddr := produced.Output.Address()
			tmpUtxo := models.Utxo{
				TxId:       produced.Id.Id().Bytes(),
				OutputIdx:  produced.Id.Index(),
				AddedSlot:  e.Point.Slot,
				PaymentKey: outAddr.PaymentKeyHash().Bytes(),
				StakingKey: outAddr.StakeKeyHash().Bytes(),
				Cbor:       produced.Output.Cbor(),
			}
			if err := ls.addUtxo(tmpUtxo); err != nil {
				return fmt.Errorf("add produced UTxO: %w", err)
			}
		}
		// XXX: generate event for each TX/UTxO?
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
	return nil
}

func (ls *LedgerState) addUtxo(utxo models.Utxo) error {
	// Add UTxO to blob DB
	key := models.UtxoBlobKey(utxo.TxId, utxo.OutputIdx)
	err := ls.db.Blob().Update(func(txn *badger.Txn) error {
		err := txn.Set(key, utxo.Cbor)
		return err
	})
	if err != nil {
		return err
	}
	// Add to metadata DB
	if result := ls.db.Metadata().Create(&utxo); result.Error != nil {
		return result.Error
	}
	return nil
}

// consumeUtxo marks a UTxO as "deleted" without actually deleting it. This allows for a UTxO
// to be easily on rollback
func (ls *LedgerState) consumeUtxo(utxoId ledger.TransactionInput, slot uint64) error {
	// Find UTxO
	utxo, err := models.UtxoByRef(ls.db, utxoId.Id().Bytes(), utxoId.Index())
	if err != nil {
		return err
	}
	// Mark as deleted in specified slot
	utxo.DeletedSlot = slot
	if result := ls.db.Metadata().Save(&utxo); result.Error != nil {
		return result.Error
	}
	return nil
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
