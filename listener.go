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
	"fmt"
	"net"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/protocol/blockfetch"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	"github.com/blinklabs-io/gouroboros/protocol/peersharing"
	"github.com/blinklabs-io/gouroboros/protocol/txsubmission"
)

type ListenerConfig struct {
	UseNtC        bool
	Listener      net.Listener
	ListenNetwork string
	ListenAddress string
	ReuseAddress  bool
}

func (n *Node) startListener(l ListenerConfig) error {
	// Create listener if none is provided
	if l.Listener == nil {
		listenConfig := net.ListenConfig{}
		if l.ReuseAddress {
			listenConfig.Control = socketControl
		}
		listener, err := listenConfig.Listen(context.Background(), l.ListenNetwork, l.ListenAddress)
		if err != nil {
			return fmt.Errorf("failed to open listening socket: %s", err)
		}
		l.Listener = listener
	}
	// Build connection options
	defaultConnOpts := []ouroboros.ConnectionOptionFunc{
		ouroboros.WithNetworkMagic(n.config.networkMagic),
		ouroboros.WithNodeToNode(!l.UseNtC),
		ouroboros.WithServer(true),
		ouroboros.WithPeerSharing(n.config.peerSharing),
	}
	if l.UseNtC {
		// Node-to-client
		defaultConnOpts = append(
			defaultConnOpts,
			// TODO: add localtxsubmission
			// TODO: add localstatequery
			// TODO: add localtxmonitor
		)
	} else {
		// Node-to-node
		defaultConnOpts = append(
			defaultConnOpts,
			ouroboros.WithPeerSharingConfig(
				peersharing.NewConfig(
					mergeConnOpts(
						n.peersharingServerConnOpts(),
					)...,
				),
			),
			ouroboros.WithTxSubmissionConfig(
				txsubmission.NewConfig(
					mergeConnOpts(
						n.txsubmissionServerConnOpts(),
					)...,
				),
			),
			ouroboros.WithChainSyncConfig(
				chainsync.NewConfig(
					mergeConnOpts(
						n.chainsyncServerConnOpts(),
					)...,
				),
			),
			ouroboros.WithBlockFetchConfig(
				blockfetch.NewConfig(
					mergeConnOpts(
						n.blockfetchServerConnOpts(),
					)...,
				),
			),
		)
	}
	go func() {
		for {
			// Accept connection
			conn, err := l.Listener.Accept()
			if err != nil {
				n.config.logger.Error(fmt.Sprintf("accept failed: %s", err))
				continue
			}
			n.config.logger.Info(fmt.Sprintf("accepted connection from %s", conn.RemoteAddr()))
			// Setup Ouroboros connection
			connOpts := append(
				defaultConnOpts,
				ouroboros.WithConnection(conn),
			)
			oConn, err := ouroboros.NewConnection(connOpts...)
			if err != nil {
				n.config.logger.Error(fmt.Sprintf("failed to setup connection: %s", err))
				continue
			}
			// Add to connection manager
			// TODO: add tags for connection for later tracking
			n.connManager.AddConnection(oConn)
		}
	}()
	return nil
}
