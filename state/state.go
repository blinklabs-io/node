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
	"github.com/blinklabs-io/gouroboros/ledger/byron"
	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
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
	// TODO: move this into the DB
	currentEra uint8
}

func NewLedgerState(
	dataDir string,
	eventBus *event.EventBus,
	logger *slog.Logger,
) (*LedgerState, error) {
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
			ls.Lock()
			defer func() {
				// Unlock ledger state
				ls.Unlock()
				// Schedule the next run
				ls.scheduleCleanupConsumedUtxos()
			}()
			// Get the current tip, since we're querying by slot
			tip, err := ls.Tip()
			if err != nil {
				ls.logger.Error(
					fmt.Sprintf("ledger: failed to get tip: %s", err),
				)
				return
			}
			for {
				// Perform updates in a transaction
				batchDone := false
				txn := ls.db.Transaction(true)
				err = txn.Do(func(txn *database.Txn) error {
					// Get UTxOs that are marked as deleted and older than our slot window
					var tmpUtxos []models.Utxo
					result := txn.Metadata().
						Where("deleted_slot <= ?", tip.Point.Slot-cleanupConsumedUtxosSlotWindow).
						Order("id DESC").Limit(1000).
						Find(&tmpUtxos)
					if result.Error != nil {
						return fmt.Errorf(
							"failed to query consumed UTxOs: %w",
							result.Error,
						)
					}
					if len(tmpUtxos) == 0 {
						batchDone = true
						return nil
					}
					// Delete the UTxOs
					for _, utxo := range tmpUtxos {
						if err := models.UtxoDeleteTxn(txn, utxo); err != nil {
							return fmt.Errorf(
								"failed to remove consumed UTxO: %w",
								err,
							)
						}
					}
					return nil
				})
				if err != nil {
					ls.logger.Error(fmt.Sprintf("ledger: %s", err))
					return
				}
				if batchDone {
					break
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
				fmt.Sprintf("ledger: failed to handle rollback: %s", err),
			)
			return
		}
	} else {
		if err := ls.handleEventChainSyncBlock(e); err != nil {
			// TODO: actually handle this error
			ls.logger.Error(
				fmt.Sprintf("ledger: failed to handle block: %s", err),
			)
			return
		}
	}
}

func (ls *LedgerState) handleEventChainSyncRollback(e ChainsyncEvent) error {
	// Start a transaction
	txn := ls.db.Transaction(true)
	err := txn.Do(func(txn *database.Txn) error {
		// Remove rolled-back blocks in reverse order
		var tmpBlocks []models.Block
		result := txn.Metadata().
			Where("slot > ?", e.Point.Slot).
			Order("slot DESC").
			Find(&tmpBlocks)
		if result.Error != nil {
			return fmt.Errorf("query blocks: %w", result.Error)
		}
		for _, tmpBlock := range tmpBlocks {
			if err := ls.removeBlock(txn, tmpBlock); err != nil {
				return fmt.Errorf("remove block: %w", err)
			}
		}
		// Delete rolled-back UTxOs
		var tmpUtxos []models.Utxo
		result = txn.Metadata().
			Where("added_slot > ?", e.Point.Slot).
			Order("id DESC").
			Find(&tmpUtxos)
		if result.Error != nil {
			return fmt.Errorf("remove rolled-backup UTxOs: %w", result.Error)
		}
		for _, utxo := range tmpUtxos {
			if err := models.UtxoDeleteTxn(txn, utxo); err != nil {
				return fmt.Errorf("remove rolled-back UTxO: %w", err)
			}
		}
		// Restore spent UTxOs
		result = txn.Metadata().
			Model(models.Utxo{}).
			Where("deleted_slot > ?", e.Point.Slot).
			Update("deleted_slot", 0)
		if result.Error != nil {
			return fmt.Errorf(
				"restore spent UTxOs after rollback: %w",
				result.Error,
			)
		}
		return nil
	})
	if err != nil {
		return err
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
		"ledger: chain rolled back, new tip: %x at slot %d",
		e.Point.Hash,
		e.Point.Slot,
	))
	return nil
}

func (ls *LedgerState) handleEventChainSyncBlock(e ChainsyncEvent) error {
	tmpBlock := models.Block{
		Slot: e.Point.Slot,
		Hash: e.Point.Hash,
		// TODO: figure out something for Byron. this won't work, since the
		// block number isn't stored in the block itself
		Number: e.Block.BlockNumber(),
		Type:   e.Type,
		Cbor:   e.Block.Cbor(),
	}
	// TODO: track this using protocol params and hard forks
	ls.currentEra = e.Block.Era().Id
	// Start a transaction
	txn := ls.db.Transaction(true)
	err := txn.Do(func(txn *database.Txn) error {
		// Check for epoch change
		latestEpoch, err := models.EpochLatestTxn(txn)
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			// Create initial epoch
			latestEpoch = models.Epoch{
				EpochId:             0,
				EraId:               ls.currentEra,
				StartSlot:           0,
				SlotLengthInSeconds: 20,
				LengthInSlots:       21600,
			}
			if result := txn.Metadata().Create(&latestEpoch); result.Error != nil {
				return result.Error
			}
			ls.logger.Debug("ledger: added initial epoch to DB", "epoch", fmt.Sprintf("%#v", latestEpoch))
		}
		if e.Point.Slot > latestEpoch.StartSlot+uint64(latestEpoch.LengthInSlots) {
			// Create next epoch record
			newEpoch := models.Epoch{
				EpochId:   latestEpoch.EpochId + 1,
				EraId:     ls.currentEra,
				StartSlot: latestEpoch.StartSlot + uint64(latestEpoch.LengthInSlots),
			}
			// TODO: replace with protocol param lookups
			if ls.currentEra == byron.EraIdByron {
				newEpoch.SlotLengthInSeconds = 20
				newEpoch.LengthInSlots = 21600
			} else {
				newEpoch.SlotLengthInSeconds = 1
				newEpoch.LengthInSlots = 432000
			}
			if result := txn.Metadata().Create(&newEpoch); result.Error != nil {
				return result.Error
			}
			ls.logger.Debug("ledger: added next epoch to DB", "epoch", fmt.Sprintf("%#v", newEpoch))
		}
		// Add block to database
		if err := ls.addBlock(txn, tmpBlock); err != nil {
			return fmt.Errorf("add block: %w", err)
		}
		// Process transactions
		for _, tx := range e.Block.Transactions() {
			// Process consumed UTxOs
			for _, consumed := range tx.Consumed() {
				if err := ls.consumeUtxo(txn, consumed, e.Point.Slot); err != nil {
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
				if err := ls.addUtxo(txn, tmpUtxo); err != nil {
					return fmt.Errorf("add produced UTxO: %w", err)
				}
			}
			// XXX: generate event for each TX/UTxO?
			// Protocol parameter updates
			if updateEpoch, paramUpdates := tx.ProtocolParameterUpdates(); updateEpoch > 0 {
				for genesisHash, update := range paramUpdates {
					tmpUpdate := models.PParamUpdate{
						AddedSlot:   e.Point.Slot,
						Epoch:       updateEpoch,
						GenesisHash: genesisHash,
						Cbor:        update.Cbor(),
					}
					if result := txn.Metadata().Create(&tmpUpdate); result.Error != nil {
						return result.Error
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
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
		"ledger: chain extended, new tip: %s at slot %d",
		e.Block.Hash(),
		e.Block.SlotNumber(),
	))
	return nil
}

func (ls *LedgerState) addUtxo(txn *database.Txn, utxo models.Utxo) error {
	// Add UTxO to blob DB
	key := models.UtxoBlobKey(utxo.TxId, utxo.OutputIdx)
	err := txn.Blob().Set(key, utxo.Cbor)
	if err != nil {
		return err
	}
	// Add to metadata DB
	if result := txn.Metadata().Create(&utxo); result.Error != nil {
		return result.Error
	}
	return nil
}

// consumeUtxo marks a UTxO as "deleted" without actually deleting it. This allows for a UTxO
// to be easily on rollback
func (ls *LedgerState) consumeUtxo(
	txn *database.Txn,
	utxoId ledger.TransactionInput,
	slot uint64,
) error {
	// Find UTxO
	utxo, err := models.UtxoByRefTxn(txn, utxoId.Id().Bytes(), utxoId.Index())
	if err != nil {
		// TODO: make this configurable?
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	// Mark as deleted in specified slot
	utxo.DeletedSlot = slot
	if result := txn.Metadata().Save(&utxo); result.Error != nil {
		return result.Error
	}
	return nil
}

func (ls *LedgerState) addBlock(txn *database.Txn, block models.Block) error {
	// Add block to blob DB
	key := models.BlockBlobKey(block.Slot, block.Hash)
	err := txn.Blob().Set(key, block.Cbor)
	if err != nil {
		return err
	}
	// Add to metadata DB
	if result := txn.Metadata().Create(&block); result.Error != nil {
		return result.Error
	}
	return nil
}

func (ls *LedgerState) removeBlock(
	txn *database.Txn,
	block models.Block,
) error {
	// Remove from metadata DB
	if result := txn.Metadata().Delete(&block); result.Error != nil {
		return result.Error
	}
	// Remove from blob DB
	key := models.BlockBlobKey(block.Slot, block.Hash)
	err := txn.Blob().Delete(key)
	if err != nil {
		return err
	}
	return nil
}

func (ls *LedgerState) GetBlock(point ocommon.Point) (*models.Block, error) {
	ls.RLock()
	defer ls.RUnlock()
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
	result := ls.db.Metadata().
		Order("number DESC").
		Limit(count).
		Find(&tmpBlocks)
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

// GetIntersectPoint returns the intersect between the specified points and the current chain
func (ls *LedgerState) GetIntersectPoint(
	points []ocommon.Point,
) (*ocommon.Point, error) {
	ls.RLock()
	defer ls.RUnlock()
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

// GetChainFromPoint returns a ChainIterator starting at the specified point. If inclusive is true, the iterator
// will start at the requested point, otherwise it will start at the next block.
func (ls *LedgerState) GetChainFromPoint(
	point ocommon.Point,
	inclusive bool,
) (*ChainIterator, error) {
	return newChainIterator(ls, point, inclusive)
}

// Tip returns the current chain tip
func (ls *LedgerState) Tip() (ochainsync.Tip, error) {
	var ret ochainsync.Tip
	var tmpBlock models.Block
	result := ls.db.Metadata().Order("number DESC").First(&tmpBlock)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// Return dummy data when we have no blocks
			ret = ochainsync.Tip{
				Point: ocommon.NewPointOrigin(),
			}
			return ret, nil
		}
		return ret, result.Error
	}
	ret = ochainsync.Tip{
		Point:       ocommon.NewPoint(tmpBlock.Slot, tmpBlock.Hash),
		BlockNumber: tmpBlock.Number,
	}
	return ret, nil
}

// UtxoByRef returns a single UTxO by reference
func (ls *LedgerState) UtxoByRef(
	txId []byte,
	outputIdx uint32,
) (models.Utxo, error) {
	ls.RLock()
	defer ls.RUnlock()
	return models.UtxoByRef(ls.db, txId, outputIdx)
}

// UtxosByAddress returns all UTxOs that belong to the specified address
func (ls *LedgerState) UtxosByAddress(
	addr ledger.Address,
) ([]models.Utxo, error) {
	ls.RLock()
	defer ls.RUnlock()
	return models.UtxosByAddress(ls.db, addr)
}
