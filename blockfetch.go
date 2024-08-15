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
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/blockfetch"
	oblockfetch "github.com/blinklabs-io/gouroboros/protocol/blockfetch"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

func (n *Node) blockfetchServerConnOpts() []oblockfetch.BlockFetchOptionFunc {
	return []oblockfetch.BlockFetchOptionFunc{
		oblockfetch.WithRequestRangeFunc(n.blockfetchServerRequestRange),
	}
}

func (n *Node) blockfetchClientConnOpts() []oblockfetch.BlockFetchOptionFunc {
	return []oblockfetch.BlockFetchOptionFunc{
		oblockfetch.WithBlockFunc(n.blockfetchClientBlock),
	}
}

func (n *Node) blockfetchServerRequestRange(
	ctx oblockfetch.CallbackContext,
	start ocommon.Point,
	end ocommon.Point,
) error {
	// TODO: check if we have requested block range available and send NoBlocks if not
	chainIter, err := n.ledgerState.GetChainFromPoint(start)
	if err != nil {
		return err
	}
	// Start async process to send requested block range
	go func() {
		if err := ctx.Server.StartBatch(); err != nil {
			return
		}
		for {
			next, _ := chainIter.Next(false)
			if next == nil {
				break
			}
			if next.Block.Slot > end.Slot {
				break
			}
			blockBytes := next.Block.Cbor[:]
			err := ctx.Server.Block(
				next.Block.Type,
				blockBytes,
			)
			if err != nil {
				// TODO: push this error somewhere
				return
			}
			// Make sure we don't hang waiting for the next block if we've already hit the end
			if next.Block.Slot == end.Slot {
				break
			}
		}
		if err := ctx.Server.BatchDone(); err != nil {
			return
		}
	}()
	return nil
}

func (n *Node) blockfetchClientBlock(ctx blockfetch.CallbackContext, blockType uint, block ledger.Block) error {
	if err := n.chainsyncState.AddBlock(block, blockType); err != nil {
		return err
	}
	// Start normal chain-sync if we've reached the last block of our bulk range
	if block.SlotNumber() == n.chainsyncBulkRangeEnd.Slot {
		if err := n.chainsyncClientStart(ctx.ConnectionId); err != nil {
			return err
		}
	}
	return nil
}
