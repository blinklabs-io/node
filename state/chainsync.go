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
	"encoding/hex"
	"fmt"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger"

	"github.com/blinklabs-io/node/database"
	"github.com/blinklabs-io/node/event"
	"github.com/blinklabs-io/node/state/models"
)

const (
	blockfetchBatchSize          = 500
	blockfetchBatchSlotThreshold = 2500 * 20 // TODO: calculate from protocol params

	// Timeout for updates on a blockfetch operation. This is based on a 2s BatchStart
	// and a 2s Block timeout for blockfetch
	blockfetchBusyTimeout = 5 * time.Second
)

func (ls *LedgerState) handleEventChainsync(evt event.Event) {
	ls.chainsyncMutex.Lock()
	defer ls.chainsyncMutex.Unlock()
	e := evt.Data.(ChainsyncEvent)
	if e.Rollback {
		if err := ls.handleEventChainsyncRollback(e); err != nil {
			// TODO: actually handle this error
			ls.config.Logger.Error(
				"failed to handle rollback",
				"component", "ledger",
				"error", err,
			)
			return
		}
	} else if e.BlockHeader != nil {
		if err := ls.handleEventChainsyncBlockHeader(e); err != nil {
			// TODO: actually handle this error
			ls.config.Logger.Error(
				fmt.Sprintf("ledger: failed to handle block header: %s", err),
			)
			return
		}
	}
}

func (ls *LedgerState) handleEventBlockfetch(evt event.Event) {
	ls.chainsyncMutex.Lock()
	defer ls.chainsyncMutex.Unlock()
	e := evt.Data.(BlockfetchEvent)
	if e.BatchDone {
		if err := ls.handleEventBlockfetchBatchDone(e); err != nil {
			// TODO: actually handle this error
			ls.config.Logger.Error(
				fmt.Sprintf(
					"ledger: failed to handle blockfetch batch done: %s",
					err,
				),
			)
		}
	} else if e.Block != nil {
		if err := ls.handleEventBlockfetchBlock(e); err != nil {
			// TODO: actually handle this error
			ls.config.Logger.Error(
				fmt.Sprintf("ledger: failed to handle block: %s", err),
			)
		}
	}
}

func (ls *LedgerState) handleEventChainsyncRollback(e ChainsyncEvent) error {
	ls.Lock()
	defer ls.Unlock()
	return ls.rollback(e.Point)
}

func (ls *LedgerState) handleEventChainsyncBlockHeader(e ChainsyncEvent) error {
	// Add to cached header points
	ls.chainsyncHeaderPoints = append(
		ls.chainsyncHeaderPoints,
		e.Point,
	)
	// Wait for additional block headers before fetching block bodies if we're
	// far enough out from tip
	if e.Point.Slot < e.Tip.Point.Slot &&
		(e.Tip.Point.Slot-e.Point.Slot > blockfetchBatchSlotThreshold) &&
		len(ls.chainsyncHeaderPoints) < blockfetchBatchSize {
		return nil
	}
	// Don't start fetch if there's already one in progress
	if ls.chainsyncBlockfetchBusy {
		// Clear busy flag on timeout
		if time.Since(ls.chainsyncBlockfetchBusyTime) > blockfetchBusyTimeout {
			ls.chainsyncBlockfetchBusy = false
			ls.chainsyncBlockfetchWaiting = false
			ls.config.Logger.Warn(
				fmt.Sprintf(
					"blockfetch operation timed out after %s",
					blockfetchBusyTimeout,
				),
				"component",
				"ledger",
			)
			return nil
		}
		ls.chainsyncBlockfetchWaiting = true
		return nil
	}
	// Request current bulk range
	err := ls.config.BlockfetchRequestRangeFunc(
		e.ConnectionId,
		ls.chainsyncHeaderPoints[0],
		ls.chainsyncHeaderPoints[len(ls.chainsyncHeaderPoints)-1],
	)
	if err != nil {
		return err
	}
	ls.chainsyncBlockfetchBusy = true
	ls.chainsyncBlockfetchBusyTime = time.Now()
	// Reset cached header points
	ls.chainsyncHeaderPoints = nil
	return nil
}

func (ls *LedgerState) handleEventBlockfetchBlock(e BlockfetchEvent) error {
	ls.chainsyncBlockEvents = append(
		ls.chainsyncBlockEvents,
		e,
	)
	// Update busy time in order to detect fetch timeout
	ls.chainsyncBlockfetchBusyTime = time.Now()
	return nil
}

func (ls *LedgerState) processBlockEvents() error {
	// XXX: move this into the loop?
	ls.Lock()
	defer ls.Unlock()
	batchOffset := 0
	for {
		batchSize := min(
			10, // Chosen to stay well under badger transaction size limit
			len(ls.chainsyncBlockEvents)-batchOffset,
		)
		if batchSize <= 0 {
			break
		}
		// Start a transaction
		txn := ls.db.Transaction(true)
		err := txn.Do(func(txn *database.Txn) error {
			for _, evt := range ls.chainsyncBlockEvents[batchOffset : batchOffset+batchSize] {
				if err := ls.processBlockEvent(txn, evt); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		batchOffset += batchSize
	}
	ls.chainsyncBlockEvents = nil
	ls.config.Logger.Info(
		fmt.Sprintf(
			"chain extended, new tip: %x at slot %d",
			ls.currentTip.Point.Hash,
			ls.currentTip.Point.Slot,
		),
		"component",
		"ledger",
	)
	return nil
}

func (ls *LedgerState) processBlockEvent(
	txn *database.Txn,
	e BlockfetchEvent,
) error {
	tmpBlock := models.Block{
		Slot: e.Point.Slot,
		Hash: e.Point.Hash,
		// TODO: figure out something for Byron. this won't work, since the
		// block number isn't stored in the block itself
		Number: e.Block.BlockNumber(),
		Type:   e.Type,
		Cbor:   e.Block.Cbor(),
	}
	// Special handling for genesis block
	if ls.currentEpoch.ID == 0 {
		// Check for era change
		if uint(e.Block.Era().Id) != ls.currentEra.Id {
			targetEraId := uint(e.Block.Era().Id)
			// Transition through every era between the current and the target era
			for nextEraId := ls.currentEra.Id + 1; nextEraId <= targetEraId; nextEraId++ {
				if err := ls.transitionToEra(txn, nextEraId, ls.currentEpoch.EpochId, e.Point.Slot); err != nil {
					return err
				}
			}
		}
		// Create initial epoch record
		epochSlotLength, epochLength, err := ls.currentEra.EpochLengthFunc(
			ls.config.CardanoNodeConfig,
		)
		if err != nil {
			return err
		}
		newEpoch := models.Epoch{
			EpochId:       0,
			EraId:         ls.currentEra.Id,
			StartSlot:     0,
			SlotLength:    epochSlotLength,
			LengthInSlots: epochLength,
		}
		if result := txn.Metadata().Create(&newEpoch); result.Error != nil {
			return result.Error
		}
		ls.currentEpoch = newEpoch
		ls.config.Logger.Debug(
			"added initial epoch to DB",
			"epoch", fmt.Sprintf("%+v", newEpoch),
			"component", "ledger",
		)
	}
	// Check for epoch rollover
	if e.Point.Slot > ls.currentEpoch.StartSlot+uint64(
		ls.currentEpoch.LengthInSlots,
	) {
		// Apply pending pparam updates
		if err := ls.applyPParamUpdates(txn, ls.currentEpoch.EpochId, e.Point.Slot); err != nil {
			return err
		}
		// Create next epoch record
		epochSlotLength, epochLength, err := ls.currentEra.EpochLengthFunc(
			ls.config.CardanoNodeConfig,
		)
		if err != nil {
			return err
		}
		newEpoch := models.Epoch{
			EpochId:       ls.currentEpoch.EpochId + 1,
			EraId:         uint(e.Block.Era().Id),
			SlotLength:    epochSlotLength,
			LengthInSlots: epochLength,
			StartSlot: ls.currentEpoch.StartSlot + uint64(
				ls.currentEpoch.LengthInSlots,
			),
		}
		if result := txn.Metadata().Create(&newEpoch); result.Error != nil {
			return result.Error
		}
		ls.currentEpoch = newEpoch
		ls.metrics.epochNum.Set(float64(newEpoch.EpochId))
		ls.config.Logger.Debug(
			"added next epoch to DB",
			"epoch", fmt.Sprintf("%+v", newEpoch),
			"component", "ledger",
		)
	}

	// TODO: actually validate here
	if e.Block.Era().Id >= 5 { // Babbage
		ls.config.Logger.Debug(
			"attempting block validation...",
			"slot_number",
			tmpBlock.Slot,
		)
		// Validate block
		headerCbor := e.Block.Header().Cbor()
		header, err := ledger.NewBabbageBlockHeaderFromCbor(
			headerCbor,
		)
		if err != nil {
			ls.config.Logger.Debug("error getting block header")
		} else {
			vrfResult := header.Body.VrfResult.([]interface{})
			// TODO: figure out derivation here
			vrfOutputBytes := vrfResult[0].([]byte)
			verifyError, isValid, vrfHex, blockNo, slotNo := ledger.VerifyBlock(ledger.BlockHexCbor{
				HeaderCbor:    hex.EncodeToString(headerCbor),
				Eta0:          hex.EncodeToString(vrfOutputBytes),
				Spk:           int(129600), // TODO: get from protocol params
				BlockBodyCbor: hex.EncodeToString(tmpBlock.Cbor),
			})
			ls.config.Logger.Debug(
				fmt.Sprintf("use the things: error: %+v, isValid: %+v, vrfHex: %s, blockNo: %d, slotNo: %d",
					verifyError, isValid, vrfHex, blockNo, slotNo,
				),
			)
		}
	}

	// TODO: track this using protocol params and hard forks
	// Check for era change
	if uint(e.Block.Era().Id) != ls.currentEra.Id {
		targetEraId := uint(e.Block.Era().Id)
		// Transition through every era between the current and the target era
		for nextEraId := ls.currentEra.Id + 1; nextEraId <= targetEraId; nextEraId++ {
			if err := ls.transitionToEra(txn, nextEraId, ls.currentEpoch.EpochId, e.Point.Slot); err != nil {
				return err
			}
		}
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
					GenesisHash: genesisHash.Bytes(),
					Cbor:        update.Cbor(),
				}
				if result := txn.Metadata().Create(&tmpUpdate); result.Error != nil {
					return result.Error
				}
			}
		}
		// Certificates
		if err := ls.processTransactionCertificates(txn, e.Point, tx); err != nil {
			return err
		}
	}
	// Generate event
	ls.config.EventBus.Publish(
		ChainBlockEventType,
		event.NewEvent(
			ChainBlockEventType,
			ChainBlockEvent{
				Point: e.Point,
				Block: tmpBlock,
			},
		),
	)
	return nil
}

func (ls *LedgerState) handleEventBlockfetchBatchDone(e BlockfetchEvent) error {
	// Process pending block events
	if err := ls.processBlockEvents(); err != nil {
		return err
	}
	// Check for pending block range request
	if !ls.chainsyncBlockfetchWaiting ||
		len(ls.chainsyncHeaderPoints) == 0 {
		ls.chainsyncBlockfetchBusy = false
		ls.chainsyncBlockfetchWaiting = false
		return nil
	}
	// Request waiting bulk range
	err := ls.config.BlockfetchRequestRangeFunc(
		e.ConnectionId,
		ls.chainsyncHeaderPoints[0],
		ls.chainsyncHeaderPoints[len(ls.chainsyncHeaderPoints)-1],
	)
	if err != nil {
		return err
	}
	ls.chainsyncBlockfetchBusyTime = time.Now()
	ls.chainsyncBlockfetchWaiting = false
	// Reset cached header points
	ls.chainsyncHeaderPoints = nil
	return nil
}
