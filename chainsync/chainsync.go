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

package chainsync

import (
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/blinklabs-io/node/event"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/connection"
	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	maxRecentBlocks       = 20 // Number of recent blocks to cache
	clientBlockBufferSize = 50 // Size of per-client block buffer
)

var (
	blockNum_int = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "cardano_node_metrics_blockNum_int",
		Help: "current block number",
	})
	slotNum_int = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "cardano_node_metrics_slotNum_int",
		Help: "current slot number",
	})
)

const (
	ChainsyncEventType event.EventType = "chainsync.event"
)

// ChainsyncEvent represents either a RollForward or RollBackward chainsync event.
// We use a single event type for both to make synchronization easier.
type ChainsyncEvent struct {
	Point    ChainsyncPoint
	Cbor     []byte
	Type     uint
	Rollback bool
}

type ChainsyncPoint struct {
	SlotNumber  uint64
	BlockHash   string
	BlockNumber uint64
}

func (c ChainsyncPoint) String() string {
	return fmt.Sprintf(
		"< slot_number = %d, block_number = %d, block_hash = %s >",
		c.SlotNumber,
		c.BlockNumber,
		c.BlockHash,
	)
}

func (c ChainsyncPoint) ToTip() ochainsync.Tip {
	hashBytes, _ := hex.DecodeString(c.BlockHash)
	return ochainsync.Tip{
		BlockNumber: c.BlockNumber,
		Point: ocommon.Point{
			Slot: c.SlotNumber,
			Hash: hashBytes[:],
		},
	}
}

type ChainsyncBlock struct {
	Point    ChainsyncPoint
	Cbor     []byte
	Type     uint
	Rollback bool
}

func (c ChainsyncBlock) String() string {
	return fmt.Sprintf(
		"%s (%d bytes)",
		c.Point.String(),
		len(c.Cbor),
	)
}

type ChainsyncClientState struct {
	Cursor               ChainsyncPoint
	BlockChan            chan ChainsyncBlock
	NeedsInitialRollback bool
}

type State struct {
	sync.Mutex
	eventBus     *event.EventBus
	tip          ChainsyncPoint
	clients      map[ouroboros.ConnectionId]*ChainsyncClientState
	recentBlocks []ChainsyncBlock // TODO: replace with hook(s) for block storage/retrieval
	subs         map[ouroboros.ConnectionId]chan ChainsyncBlock
	clientConnId *ouroboros.ConnectionId // TODO: replace with handling of multiple chainsync clients
}

func NewState(eventBus *event.EventBus) *State {
	return &State{
		eventBus: eventBus,
		clients:  make(map[ouroboros.ConnectionId]*ChainsyncClientState),
	}
}

func (s *State) Tip() ChainsyncPoint {
	return s.tip
}

func (s *State) RecentBlocks() []ChainsyncBlock {
	// TODO: replace with hook to get recent blocks
	return s.recentBlocks[:]
}

func (s *State) AddClient(
	connId connection.ConnectionId,
	cursor ChainsyncPoint,
) *ChainsyncClientState {
	s.Lock()
	defer s.Unlock()
	// Create initial chainsync state for connection
	if _, ok := s.clients[connId]; !ok {
		s.clients[connId] = &ChainsyncClientState{
			Cursor:               cursor,
			BlockChan:            s.sub(connId),
			NeedsInitialRollback: true,
		}
	}
	return s.clients[connId]
}

func (s *State) RemoveClient(connId connection.ConnectionId) {
	s.Lock()
	defer s.Unlock()
	if clientState, ok := s.clients[connId]; ok {
		// Unsub from chainsync updates
		if clientState.BlockChan != nil {
			s.unsub(connId)
		}
		// Remove client state entry
		delete(s.clients, connId)
	}
}

// TODO: replace with handling of multiple chainsync clients
func (s *State) GetClientConnId() *ouroboros.ConnectionId {
	return s.clientConnId
}

// TODO: replace with handling of multiple chainsync clients
func (s *State) SetClientConnId(connId ouroboros.ConnectionId) {
	s.clientConnId = &connId
}

// TODO: replace with handling of multiple chainsync clients
func (s *State) RemoveClientConnId(connId ouroboros.ConnectionId) {
	if s.clientConnId != nil && *s.clientConnId == connId {
		s.clientConnId = nil
	}
}

func (s *State) sub(key ouroboros.ConnectionId) chan ChainsyncBlock {
	tmpChan := make(chan ChainsyncBlock, clientBlockBufferSize)
	if s.subs == nil {
		s.subs = make(map[ouroboros.ConnectionId]chan ChainsyncBlock)
	}
	s.subs[key] = tmpChan
	// Send all current blocks
	for _, block := range s.recentBlocks {
		tmpChan <- block
	}
	return tmpChan
}

func (s *State) unsub(key ouroboros.ConnectionId) {
	if _, ok := s.subs[key]; ok {
		close(s.subs[key])
		delete(s.subs, key)
	}
}

func (s *State) AddBlock(block ChainsyncBlock) {
	s.Lock()
	defer s.Unlock()
	// TODO: add hooks for storing new blocks
	s.recentBlocks = append(
		s.recentBlocks,
		block,
	)
	blockNum_int.Set(float64(block.Point.BlockNumber))
	slotNum_int.Set(float64(block.Point.SlotNumber))
	// Update tip
	s.tip = block.Point
	// Prune older blocks
	if len(s.recentBlocks) > maxRecentBlocks {
		s.recentBlocks = s.recentBlocks[len(s.recentBlocks)-maxRecentBlocks:]
	}
	// Publish new block to chainsync subscribers
	for _, pubChan := range s.subs {
		pubChan <- block
	}
	// Generate event
	s.eventBus.Publish(
		ChainsyncEventType,
		event.NewEvent(
			ChainsyncEventType,
			ChainsyncEvent{
				Point: block.Point,
				Type:  block.Type,
				Cbor:  block.Cbor[:],
			},
		),
	)
}

func (s *State) Rollback(slot uint64, hash string) {
	s.Lock()
	defer s.Unlock()
	// TODO: add hook for getting recent blocks
	// Remove recent blocks newer than the rollback block
	for idx, block := range s.recentBlocks {
		if block.Point.SlotNumber > slot {
			s.recentBlocks = s.recentBlocks[:idx]
			break
		}
	}
	// Publish rollback to chainsync subscribers
	for _, pubChan := range s.subs {
		pubChan <- ChainsyncBlock{
			Rollback: true,
			Point: ChainsyncPoint{
				SlotNumber: slot,
				BlockHash:  hash,
			},
		}
	}
	// Generate event
	s.eventBus.Publish(
		ChainsyncEventType,
		event.NewEvent(
			ChainsyncEventType,
			ChainsyncEvent{
				Rollback: true,
				Point: ChainsyncPoint{
					SlotNumber: slot,
					BlockHash:  hash,
				},
			},
		),
	)
}
