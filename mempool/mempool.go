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

package mempool

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
)

const (
	txsubmissionMempoolExpiration       = 1 * time.Hour
	txSubmissionMempoolExpirationPeriod = 1 * time.Minute
)

type MempoolTransaction struct {
	Hash     string
	Type     uint
	Cbor     []byte
	LastSeen time.Time
}

type Mempool struct {
	sync.Mutex
	logger             *slog.Logger
	consumers          map[ouroboros.ConnectionId]*MempoolConsumer
	consumersMutex     sync.Mutex
	consumerIndex      map[ouroboros.ConnectionId]int
	consumerIndexMutex sync.Mutex
	transactions       []*MempoolTransaction
}

func NewMempool(logger *slog.Logger) *Mempool {
	m := &Mempool{
		consumers: make(map[ouroboros.ConnectionId]*MempoolConsumer),
	}
	if logger == nil {
		// Create logger to throw away logs
		// We do this so we don't have to add guards around every log operation
		m.logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	// TODO: replace this with purging based on on-chain TXs
	// Schedule initial mempool expired cleanup
	m.scheduleRemoveExpired()
	return m
}

func (m *Mempool) AddConsumer(connId ouroboros.ConnectionId) *MempoolConsumer {
	// Create consumer
	m.consumersMutex.Lock()
	defer m.consumersMutex.Unlock()
	consumer := newConsumer()
	m.consumers[connId] = consumer
	// Start goroutine to send existing TXs to consumer
	go func(consumer *MempoolConsumer) {
		for {
			m.Lock()
			m.consumerIndexMutex.Lock()
			nextTxIdx, ok := m.consumerIndex[connId]
			if !ok {
				// Our consumer has disappeared
				return
			}
			if nextTxIdx >= len(m.transactions) {
				// We've reached the current end of the mempool
				return
			}
			nextTx := m.transactions[nextTxIdx]
			if consumer.pushTx(nextTx, true) {
				nextTxIdx++
				m.consumerIndex[connId] = nextTxIdx
			}
			m.consumerIndexMutex.Unlock()
			m.Unlock()
		}
	}(consumer)
	return consumer
}

func (m *Mempool) RemoveConsumer(connId ouroboros.ConnectionId) {
	m.consumersMutex.Lock()
	m.consumerIndexMutex.Lock()
	defer func() {
		m.consumerIndexMutex.Unlock()
		m.consumersMutex.Unlock()
	}()
	if consumer, ok := m.consumers[connId]; ok {
		consumer.stop()
		delete(m.consumers, connId)
		delete(m.consumerIndex, connId)
	}
}

func (m *Mempool) Consumer(connId ouroboros.ConnectionId) *MempoolConsumer {
	m.consumersMutex.Lock()
	defer m.consumersMutex.Unlock()
	return m.consumers[connId]
}

// TODO: replace this with purging based on on-chain TXs
func (m *Mempool) removeExpired() {
	m.Lock()
	defer m.Unlock()
	expiredBefore := time.Now().Add(-txsubmissionMempoolExpiration)
	for _, tx := range m.transactions {
		if tx.LastSeen.Before(expiredBefore) {
			m.removeTransaction(tx.Hash)
			m.logger.Debug(
				fmt.Sprintf(
					"removed expired transaction %s from mempool",
					tx.Hash,
				),
			)
		}
	}
	m.scheduleRemoveExpired()
}

func (m *Mempool) scheduleRemoveExpired() {
	_ = time.AfterFunc(txSubmissionMempoolExpirationPeriod, m.removeExpired)
}

func (m *Mempool) AddTransaction(tx MempoolTransaction) error {
	m.Lock()
	m.consumersMutex.Lock()
	m.consumerIndexMutex.Lock()
	defer func() {
		m.consumerIndexMutex.Unlock()
		m.consumersMutex.Unlock()
		m.Unlock()
	}()
	// Update last seen for existing TX
	existingTx := m.getTransaction(tx.Hash)
	if existingTx != nil {
		tx.LastSeen = time.Now()
		m.logger.Debug(
			fmt.Sprintf(
				"updated last seen for transaction %s in mempool",
				tx.Hash,
			),
		)
		return nil
	}
	// Add transaction record
	m.transactions = append(m.transactions, &tx)
	m.logger.Debug(
		fmt.Sprintf("added transaction %s to mempool", tx.Hash),
	)
	// Send new TX to consumers that are ready for it
	newTxIdx := len(m.transactions) - 1
	for connId, consumerIdx := range m.consumerIndex {
		if consumerIdx == newTxIdx {
			consumer := m.consumers[connId]
			if consumer.pushTx(&tx, false) {
				consumerIdx++
				m.consumerIndex[connId] = consumerIdx
			}
		}
	}
	return nil
}

func (m *Mempool) GetTransaction(txHash string) (MempoolTransaction, bool) {
	m.Lock()
	defer m.Unlock()
	ret := m.getTransaction(txHash)
	if ret == nil {
		return MempoolTransaction{}, false
	}
	return *ret, true
}

func (m *Mempool) getTransaction(txHash string) *MempoolTransaction {
	for _, tx := range m.transactions {
		if tx.Hash == txHash {
			return tx
		}
	}
	return nil
}

func (m *Mempool) RemoveTransaction(hash string) {
	m.Lock()
	defer m.Unlock()
	if m.removeTransaction(hash) {
		m.logger.Debug(
			fmt.Sprintf("removed transaction %s from mempool", hash),
		)
	}
}

func (m *Mempool) removeTransaction(hash string) bool {
	for txIdx, tx := range m.transactions {
		if tx.Hash == hash {
			m.consumerIndexMutex.Lock()
			m.transactions = slices.Delete(
				m.transactions,
				txIdx,
				txIdx+1,
			)
			// Update consumer indexes to reflect removed TX
			for connId, consumerIdx := range m.consumerIndex {
				// Decrement consumer index if the consumer has reached the removed TX
				if consumerIdx >= txIdx {
					consumerIdx--
				}
				m.consumerIndex[connId] = consumerIdx
			}
			m.consumerIndexMutex.Unlock()
			return true
		}
	}
	return false
}
