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
		// TODO
		/*
			oblockfetch.WithBlockFunc(n.blockfetchClientBlock),
		*/
	}
}

func (n *Node) blockfetchServerRequestRange(
	ctx oblockfetch.CallbackContext,
	start ocommon.Point,
	end ocommon.Point,
) error {
	// TODO: check if we have requested block range available and send NoBlocks if not
	// Start async process to send requested block range
	go func() {
		if err := ctx.Server.StartBatch(); err != nil {
			return
		}
		for _, block := range n.chainsyncState.RecentBlocks() {
			if block.Point.SlotNumber < start.Slot {
				continue
			}
			if block.Point.SlotNumber > end.Slot {
				break
			}
			blockBytes := block.Cbor[:]
			err := ctx.Server.Block(
				block.Type,
				blockBytes,
			)
			if err != nil {
				return
			}
		}
		if err := ctx.Server.BatchDone(); err != nil {
			return
		}
	}()
	return nil
}
