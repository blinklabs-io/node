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
	"encoding/hex"
	"fmt"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/node/chainsync"

	"github.com/blinklabs-io/gouroboros/ledger"
	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

func (n *Node) chainsyncServerConnOpts() []ochainsync.ChainSyncOptionFunc {
	return []ochainsync.ChainSyncOptionFunc{
		ochainsync.WithFindIntersectFunc(n.chainsyncServerFindIntersect),
		ochainsync.WithRequestNextFunc(n.chainsyncServerRequestNext),
	}
}

func (n *Node) chainsyncClientConnOpts() []ochainsync.ChainSyncOptionFunc {
	return []ochainsync.ChainSyncOptionFunc{
		ochainsync.WithRollForwardFunc(n.chainsyncClientRollForward),
		ochainsync.WithRollBackwardFunc(n.chainsyncClientRollBackward),
	}
}

func (n *Node) chainsyncClientStart(connId ouroboros.ConnectionId) error {
	conn := n.connManager.GetConnectionById(connId)
	if conn == nil {
		return fmt.Errorf("failed to lookup connection ID: %s", connId.String())
	}
	oConn := conn.Conn
	// TODO: use our recent blocks to build intersect points
	tip, err := oConn.ChainSync().Client.GetCurrentTip()
	if err != nil {
		return err
	}
	intersectPoints := []ocommon.Point{tip.Point}
	if err := oConn.ChainSync().Client.Sync(intersectPoints); err != nil {
		return err
	}
	return nil
}

func (n *Node) chainsyncServerFindIntersect(
	ctx ochainsync.CallbackContext,
	points []ocommon.Point,
) (ocommon.Point, ochainsync.Tip, error) {
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

func (n *Node) chainsyncServerRequestNext(
	ctx ochainsync.CallbackContext,
) error {
	// Create/retrieve chainsync state for connection
	clientState := n.chainsyncState.AddClient(
		ctx.ConnectionId,
		n.chainsyncState.Tip(),
	)
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
outerLoop:
	for {
		sentAwaitReply := false
		select {
		case block, ok := <-clientState.BlockChan:
			if !ok {
				break outerLoop
			}
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
				block, ok := <-clientState.BlockChan
				if !ok {
					return
				}
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

func (n *Node) chainsyncServerSendNext(
	ctx ochainsync.CallbackContext,
	block chainsync.ChainsyncBlock,
) error {
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

func (n *Node) chainsyncClientRollBackward(
	ctx ochainsync.CallbackContext,
	point ocommon.Point,
	tip ochainsync.Tip,
) error {
	n.config.logger.Info(fmt.Sprintf(
		"rollback: slot: %d, hash: %s",
		point.Slot,
		hex.EncodeToString(point.Hash),
	))
	n.chainsyncState.Rollback(
		point.Slot,
		hex.EncodeToString(point.Hash),
	)
	return nil
}

func (n *Node) chainsyncClientRollForward(
	ctx ochainsync.CallbackContext,
	blockType uint,
	blockData interface{},
	tip ochainsync.Tip,
) error {
	var blk ledger.Block
	switch v := blockData.(type) {
	case ledger.Block:
		blk = v
	case ledger.BlockHeader:
		conn := n.connManager.GetConnectionById(ctx.ConnectionId)
		if conn == nil {
			return fmt.Errorf("failed to lookup connection ID: %s", ctx.ConnectionId.String())
		}
		oConn := conn.Conn
		blockSlot := v.SlotNumber()
		blockHash, _ := hex.DecodeString(v.Hash())
		tmpBlock, err := oConn.BlockFetch().Client.GetBlock(ocommon.Point{Slot: blockSlot, Hash: blockHash})
		if err != nil {
			return err
		}
		blk = tmpBlock
	default:
		return fmt.Errorf("unexpected block data type: %T", v)
	}
	n.config.logger.Info(fmt.Sprintf(
		"chain extended, new tip: %s at slot %d",
		blk.Hash(),
		blk.SlotNumber(),
	))
	n.chainsyncState.AddBlock(
		chainsync.ChainsyncBlock{
			Point: chainsync.ChainsyncPoint{
				SlotNumber: blk.SlotNumber(),
				BlockHash:  blk.Hash(),
				// TODO: figure out something for Byron. this won't work, since the
				// block number isn't stored in the block itself
				BlockNumber: blk.BlockNumber(),
			},
			Cbor: blk.Cbor(),
			Type: blockType,
		},
	)
	return nil
}
