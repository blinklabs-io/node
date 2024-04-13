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
	"sync"
)

type MempoolConsumer struct {
	txChan     chan *MempoolTransaction
	cache      map[string]*MempoolTransaction
	cacheMutex sync.Mutex
}

func newConsumer() *MempoolConsumer {
	return &MempoolConsumer{
		txChan: make(chan *MempoolTransaction),
		cache:  make(map[string]*MempoolTransaction),
	}
}

func (m *MempoolConsumer) NextTx(blocking bool) *MempoolTransaction {
	var ret *MempoolTransaction
	if blocking {
		// Wait until a transaction is available
		tmpTx, ok := <-m.txChan
		if ok {
			ret = tmpTx
		}
	} else {
		select {
		case tmpTx, ok := <-m.txChan:
			if ok {
				ret = tmpTx
			}
		default:
			// No transaction available
		}
	}
	if ret != nil {
		// Add transaction to cache
		m.cacheMutex.Lock()
		m.cache[ret.Hash] = ret
		m.cacheMutex.Unlock()
	}
	return ret
}

func (m *MempoolConsumer) GetTxFromCache(hash string) *MempoolTransaction {
	m.cacheMutex.Lock()
	defer m.cacheMutex.Unlock()
	return m.cache[hash]
}

func (m *MempoolConsumer) ClearCache() {
	m.cacheMutex.Lock()
	defer m.cacheMutex.Unlock()
	m.cache = make(map[string]*MempoolTransaction)
}

func (m *MempoolConsumer) RemoveTxFromCache(hash string) {
	m.cacheMutex.Lock()
	defer m.cacheMutex.Unlock()
	delete(m.cache, hash)
}

func (m *MempoolConsumer) stop() {
	close(m.txChan)
}

func (m *MempoolConsumer) pushTx(tx *MempoolTransaction, wait bool) bool {
	if wait {
		// Block on write to channel
		m.txChan <- tx
		return true
	} else {
		// Return immediately if we can't write to channel
		select {
		case m.txChan <- tx:
			return true
		default:
			return false
		}
	}
}
