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

package node

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger"
	otxsubmission "github.com/blinklabs-io/gouroboros/protocol/txsubmission"
)

const (
	txsubmissionRequestTxIdsCount = 10 // Number of TxIds to request from peer at one time
)

func (n *Node) txsubmissionServerConnOpts() []otxsubmission.TxSubmissionOptionFunc {
	return []otxsubmission.TxSubmissionOptionFunc{
		otxsubmission.WithInitFunc(n.txsubmissionServerInit),
	}
}

func (n *Node) txsubmissionClientConnOpts() []otxsubmission.TxSubmissionOptionFunc {
	return []otxsubmission.TxSubmissionOptionFunc{
		// TODO
		/*
			txsubmission.WithRequestTxIdsFunc(
				n.txsubmissionClientRequestTxIds,
			),
			txsubmission.WithRequestTxsFunc(
				n.txsubmissionClientRequestTxs,
			),
		*/
	}
}

func (n *Node) txsubmissionServerInit(ctx otxsubmission.CallbackContext) error {
	// Start async loop to request transactions from the peer's mempool
	go func() {
		for {
			// Request available TX IDs (era and TX hash) and sizes
			// We make the request blocking to avoid looping on our side
			txIds, err := ctx.Server.RequestTxIds(true, txsubmissionRequestTxIdsCount)
			if err != nil {
				n.config.logger.Error(fmt.Sprintf("failed to request TxIds: %s", err))
				return
			}
			if len(txIds) > 0 {
				// Unwrap inner TxId from TxIdAndSize
				var requestTxIds []otxsubmission.TxId
				for _, txId := range txIds {
					requestTxIds = append(requestTxIds, txId.TxId)
				}
				// Request TX content for TxIds from above
				txs, err := ctx.Server.RequestTxs(requestTxIds)
				if err != nil {
					n.config.logger.Error(fmt.Sprintf("failed to request Txs: %s", err))
					return
				}
				for _, txBody := range txs {
					// Decode TX from CBOR
					tx, err := ledger.NewTransactionFromCbor(uint(txBody.EraId), txBody.TxBody)
					if err != nil {
						n.config.logger.Error(fmt.Sprintf("failed to parse transaction CBOR: %s", err))
						return
					}
					n.config.logger.Debug(fmt.Sprintf("received TX %s via TxSubmission", tx.Hash()))
					// TODO: add hooks to do something with TX
				}
			}
		}
	}()
	return nil
}
