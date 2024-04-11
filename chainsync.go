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
	"github.com/blinklabs-io/node/chainsync"

	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

func (n *Node) chainsyncServerFindIntersect(ctx ochainsync.CallbackContext, points []ocommon.Point) (ocommon.Point, ochainsync.Tip, error) {
	var retPoint ocommon.Point
	var retTip ochainsync.Tip
	// Find intersection
	var intersectPoint chainsync.ChainsyncPoint
	for _, block := range n.chainsyncState.RecentBlocks() {
		// Convert chainsync.ChainsyncPoint to ochainsync.Tip for easier comparison with ocommon.Point
		blockPoint := block.Point.ToTip().Point
		for _, point := range points {
			if point.Slot != blockPoint.Slot {
				continue
			}
			// Compare as string since we can't directly compare byte slices
			if string(point.Hash) != string(blockPoint.Hash) {
				continue
			}
			intersectPoint = block.Point
			break
		}
	}

	// Populate return tip
	retTip = n.chainsyncState.Tip().ToTip()

	if intersectPoint.SlotNumber == 0 {
		return retPoint, retTip, ochainsync.IntersectNotFoundError
	}

	// Add our client to the chainsync state
	_ = n.chainsyncState.AddClient(ctx.ConnectionId, intersectPoint)

	// Populate return point
	retPoint = intersectPoint.ToTip().Point

	return retPoint, retTip, nil
}

func (n *Node) chainsyncServerRequestNext(ctx ochainsync.CallbackContext) error {
	// Create/retrieve chainsync state for connection
	clientState := n.chainsyncState.AddClient(ctx.ConnectionId, n.chainsyncState.Tip())
	if clientState.NeedsInitialRollback {
		err := ctx.Server.RollBackward(
			clientState.Cursor.ToTip().Point,
			n.chainsyncState.Tip().ToTip(),
		)
		if err != nil {
			return err
		}
		clientState.NeedsInitialRollback = false
		return nil
	}
	for {
		sentAwaitReply := false
		select {
		case block := <-clientState.BlockChan:
			// Ignore blocks older than what we've already sent
			if clientState.Cursor.SlotNumber >= block.Point.SlotNumber {
				continue
			}
			return n.chainsyncServerSendNext(ctx, block)
		default:
			err := ctx.Server.AwaitReply()
			if err != nil {
				return err
			}
			// Wait for next block and send
			go func() {
				block := <-clientState.BlockChan
				_ = n.chainsyncServerSendNext(ctx, block)
			}()
			sentAwaitReply = true
		}
		if sentAwaitReply {
			break
		}
	}
	return nil
}

func (n *Node) chainsyncServerSendNext(ctx ochainsync.CallbackContext, block chainsync.ChainsyncBlock) error {
	var err error
	if block.Rollback {
		err = ctx.Server.RollBackward(
			block.Point.ToTip().Point,
			n.chainsyncState.Tip().ToTip(),
		)
	} else {
		blockBytes := block.Cbor[:]
		err = ctx.Server.RollForward(
			block.Type,
			blockBytes,
			n.chainsyncState.Tip().ToTip(),
		)
	}
	return err
}
