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
	"context"
	"errors"
	"fmt"
	"sync"

	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/node/chainsync"
	"github.com/blinklabs-io/node/event"
	"github.com/blinklabs-io/node/mempool"
	"github.com/blinklabs-io/node/state"

	ouroboros "github.com/blinklabs-io/gouroboros"
)

type Node struct {
	config                Config
	connManager           *ConnectionManager
	chainsyncState        *chainsync.State
	chainsyncBulkRangeEnd ocommon.Point
	eventBus              *event.EventBus
	outboundConns         map[ouroboros.ConnectionId]outboundPeer
	outboundConnsMutex    sync.Mutex
	mempool               *mempool.Mempool
	ledgerState           *state.LedgerState
	shutdownFuncs         []func(context.Context) error
}

func New(cfg Config) (*Node, error) {
	eventBus := event.NewEventBus(cfg.promRegistry)
	n := &Node{
		config:        cfg,
		eventBus:      eventBus,
		mempool:       mempool.NewMempool(cfg.logger, eventBus, cfg.promRegistry),
		outboundConns: make(map[ouroboros.ConnectionId]outboundPeer),
	}
	if err := n.configPopulateNetworkMagic(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %s", err)
	}
	if err := n.configValidate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %s", err)
	}
	return n, nil
}

func (n *Node) Run() error {
	// Configure tracing
	if n.config.tracing {
		if err := n.setupTracing(); err != nil {
			return err
		}
	}
	// Load state
	state, err := state.NewLedgerState(
		n.config.dataDir,
		n.eventBus,
		n.config.logger,
	)
	if err != nil {
		return fmt.Errorf("failed to load state database: %w", err)
	}
	n.ledgerState = state
	// Initialize chainsync state
	n.chainsyncState = chainsync.NewState(n.eventBus, n.ledgerState, n.config.promRegistry)
	// Configure connection manager
	n.connManager = NewConnectionManager(
		ConnectionManagerConfig{
			ConnClosedFunc: n.connectionManagerConnClosed,
		},
	)
	// Start listeners
	for _, l := range n.config.listeners {
		if err := n.startListener(l); err != nil {
			return err
		}
	}
	// Start outbound connections
	if n.config.topologyConfig != nil {
		n.connManager.AddHostsFromTopology(n.config.topologyConfig)
	}
	n.startOutboundConnections()

	// Wait forever
	select {}
}

func (n *Node) Stop() error {
	// TODO: use a cancelable context and wait for it above to call shutdown
	return n.shutdown()
}

func (n *Node) shutdown() error {
	ctx := context.TODO()
	var err error
	for _, fn := range n.shutdownFuncs {
		err = errors.Join(err, fn(ctx))
	}
	n.shutdownFuncs = nil
	return err
}

func (n *Node) connectionManagerConnClosed(
	connId ouroboros.ConnectionId,
	err error,
) {
	if err != nil {
		n.config.logger.Error(
			fmt.Sprintf(
				"unexpected connection failure: %s: %s",
				connId.String(),
				err,
			),
		)
	} else {
		n.config.logger.Info(fmt.Sprintf("connection closed: %s", connId.String()))
	}
	conn := n.connManager.GetConnectionById(connId)
	if conn == nil {
		return
	}
	// Remove connection
	n.connManager.RemoveConnection(connId)
	// Remove any chainsync client state
	n.chainsyncState.RemoveClient(connId)
	// Remove mempool consumer
	n.mempool.RemoveConsumer(connId)
	// Outbound connections
	n.outboundConnsMutex.Lock()
	if peer, ok := n.outboundConns[connId]; ok {
		// Release chainsync client
		n.chainsyncState.RemoveClientConnId(connId)
		// Reconnect outbound connection
		go n.reconnectOutboundConnection(peer)
	}
	n.outboundConnsMutex.Unlock()
}
