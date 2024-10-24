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

	"github.com/blinklabs-io/gouroboros/ledger"
	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

const (
	chainsyncIntersectPointCount = 100
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
	intersectPoints, err := n.ledgerState.RecentChainPoints(
		chainsyncIntersectPointCount,
	)
	if err != nil {
		return err
	}
	// Determine start point if we have no stored chain points
	if len(intersectPoints) == 0 {
		if n.config.intersectTip {
			// Start initial chainsync from current chain tip
			tip, err := oConn.ChainSync().Client.GetCurrentTip()
			if err != nil {
				return err
			}
			intersectPoints = append(
				intersectPoints,
				tip.Point,
			)
			return oConn.ChainSync().Client.Sync(intersectPoints)
		} else if len(n.config.intersectPoints) > 0 {
			// Start initial chainsync at specific point(s)
			intersectPoints = append(
				intersectPoints,
				n.config.intersectPoints...,
			)
		}
	}
	// Determine available block range between intersect point(s) and current tip
	bulkRangeStart, bulkRangeEnd, err := oConn.ChainSync().Client.GetAvailableBlockRange(
		intersectPoints,
	)
	if err != nil {
		return err
	}
	if bulkRangeStart.Slot == 0 && bulkRangeEnd.Slot == 0 {
		// We're already at chain tip, so start a normal sync
		return oConn.ChainSync().Client.Sync(intersectPoints)
	}
	// Use BlockFetch to request the entire available block range at once
	n.chainsyncBulkRangeEnd = bulkRangeEnd
	if err := oConn.BlockFetch().Client.GetBlockRange(bulkRangeStart, bulkRangeEnd); err != nil {
		return err
	}
	return nil
}

func (n *Node) chainsyncServerFindIntersect(
	ctx ochainsync.CallbackContext,
	points []ocommon.Point,
) (ocommon.Point, ochainsync.Tip, error) {
	n.ledgerState.RLock()
	defer n.ledgerState.RUnlock()
	var retPoint ocommon.Point
	var retTip ochainsync.Tip
	// Find intersection
	intersectPoint, err := n.ledgerState.GetIntersectPoint(points)
	if err != nil {
		return retPoint, retTip, err
	}

	// Populate return tip
	retTip, err = n.ledgerState.Tip()
	if err != nil {
		return retPoint, retTip, err
	}

	if intersectPoint == nil {
		return retPoint, retTip, ochainsync.IntersectNotFoundError
	}

	// Add our client to the chainsync state
	_, err = n.chainsyncState.AddClient(
		ctx.ConnectionId,
		*intersectPoint,
	)
	if err != nil {
		return retPoint, retTip, err
	}

	// Populate return point
	retPoint = *intersectPoint

	return retPoint, retTip, nil
}

func (n *Node) chainsyncServerRequestNext(
	ctx ochainsync.CallbackContext,
) error {
	n.ledgerState.RLock()
	defer n.ledgerState.RUnlock()
	// Create/retrieve chainsync state for connection
	tip, err := n.ledgerState.Tip()
	if err != nil {
		return err
	}
	clientState, err := n.chainsyncState.AddClient(
		ctx.ConnectionId,
		tip.Point,
	)
	if err != nil {
		return err
	}
	if clientState.NeedsInitialRollback {
		err := ctx.Server.RollBackward(
			clientState.Cursor,
			tip,
		)
		if err != nil {
			return err
		}
		clientState.NeedsInitialRollback = false
		return nil
	}
	// Check for available block
	next, err := clientState.ChainIter.Next(false)
	if err != nil {
		return err
	}
	if next != nil {
		return ctx.Server.RollForward(
			next.Block.Type,
			next.Block.Cbor,
			tip,
		)
	}
	// Send AwaitReply
	if err := ctx.Server.AwaitReply(); err != nil {
		return err
	}
	// Wait for next block and send
	go func() {
		next, _ := clientState.ChainIter.Next(true)
		if next == nil {
			return
		}
		tip, err := n.ledgerState.Tip()
		if err != nil {
			return
		}
		_ = ctx.Server.RollForward(
			next.Block.Type,
			next.Block.Cbor,
			tip,
		)
	}()
	return nil
}

func (n *Node) chainsyncClientRollBackward(
	ctx ochainsync.CallbackContext,
	point ocommon.Point,
	tip ochainsync.Tip,
) error {
	if err := n.chainsyncState.Rollback(
		point.Slot,
		hex.EncodeToString(point.Hash),
	); err != nil {
		return err
	}
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
	if err := n.chainsyncState.AddBlock(blk, blockType); err != nil {
		return err
	}
	return nil
}
