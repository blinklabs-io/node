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
	"github.com/blinklabs-io/gouroboros/ledger"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/node/event"
	"github.com/blinklabs-io/node/state/models"
)

const (
	ChainBlockEventType    = "ledger.chain-block"
	ChainRollbackEventType = "ledger.chain-rollback"
)

type ChainBlockEvent struct {
	Point ocommon.Point
	Block models.Block
}

type ChainRollbackEvent struct {
	Point ocommon.Point
}

const (
	ChainsyncEventType event.EventType = "chainsync.event"
)

// ChainsyncEvent represents either a RollForward or RollBackward chainsync event.
// We use a single event type for both to make synchronization easier.
type ChainsyncEvent struct {
	Point       ocommon.Point
	BlockNumber uint64
	Block       ledger.Block
	Type        uint
	Rollback    bool
}
