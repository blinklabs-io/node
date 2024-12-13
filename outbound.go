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

package dingo

import (
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	initialReconnectDelay  = 1 * time.Second
	maxReconnectDelay      = 128 * time.Second
	reconnectBackoffFactor = 2
)

type outboundPeer struct {
	Address        string
	ReconnectCount int
	ReconnectDelay time.Duration
	sharable       bool
}

func (n *Node) startOutboundConnections() {
	n.config.logger.Debug(
		"starting connections",
		"component", "network",
		"role", "client",
	)
	var tmpPeers []outboundPeer
	for _, host := range n.config.topologyConfig.BootstrapPeers {
		n.config.logger.Debug(
			fmt.Sprintf(
				"adding bootstrap peer topology host: %s:%d",
				host.Address,
				host.Port,
			),
			"component", "network",
			"role", "client",
		)
		tmpPeers = append(
			tmpPeers,
			outboundPeer{
				Address: net.JoinHostPort(
					host.Address,
					strconv.Itoa(int(host.Port)),
				),
			},
		)
	}
	for _, localRoot := range n.config.topologyConfig.LocalRoots {
		for _, host := range localRoot.AccessPoints {
			n.config.logger.Debug(
				fmt.Sprintf(
					"adding localRoot topology host: %s:%d",
					host.Address,
					host.Port,
				),
				"component", "network",
				"role", "client",
			)
			tmpPeers = append(
				tmpPeers,
				outboundPeer{
					Address: net.JoinHostPort(
						host.Address,
						strconv.Itoa(int(host.Port)),
					),
					sharable: localRoot.Advertise,
				},
			)
		}
	}
	for _, publicRoot := range n.config.topologyConfig.PublicRoots {
		for _, host := range publicRoot.AccessPoints {
			n.config.logger.Debug(
				fmt.Sprintf(
					"adding publicRoot topology host: %s:%d",
					host.Address,
					host.Port,
				),
				"component", "network",
				"role", "client",
			)
			tmpPeers = append(
				tmpPeers,
				outboundPeer{
					Address: net.JoinHostPort(
						host.Address,
						strconv.Itoa(int(host.Port)),
					),
					sharable: publicRoot.Advertise,
				},
			)
		}
	}
	// Start outbound connections
	for _, tmpPeer := range tmpPeers {
		go func(peer outboundPeer) {
			if err := n.createOutboundConnection(peer); err != nil {
				n.config.logger.Error(
					fmt.Sprintf(
						"outbound: failed to establish connection to %s: %s",
						peer.Address,
						err,
					),
					"component", "network",
				)
				go n.reconnectOutboundConnection(peer)
			}
		}(tmpPeer)
	}

}

func (n *Node) createOutboundConnection(peer outboundPeer) error {
	conn, err := n.connManager.CreateOutboundConn(peer.Address)
	if err != nil {
		return err
	}
	connId := conn.Id()
	peer.ReconnectCount = 0
	peer.ReconnectDelay = 0
	// Add to outbound connection tracking
	n.outboundConnsMutex.Lock()
	n.outboundConns[connId] = peer
	n.outboundConnsMutex.Unlock()
	// TODO: replace this with handling for multiple chainsync clients
	// Start chainsync client if we don't have another
	n.chainsyncState.Lock()
	defer n.chainsyncState.Unlock()
	chainsyncClientConnId := n.chainsyncState.GetClientConnId()
	if chainsyncClientConnId == nil {
		if err := n.chainsyncClientStart(connId); err != nil {
			return err
		}
		n.chainsyncState.SetClientConnId(connId)
	}
	// Start txsubmission client
	if err := n.txsubmissionClientStart(connId); err != nil {
		return err
	}
	return nil
}

func (n *Node) reconnectOutboundConnection(peer outboundPeer) {
	for {
		if peer.ReconnectDelay == 0 {
			peer.ReconnectDelay = initialReconnectDelay
		} else if peer.ReconnectDelay < maxReconnectDelay {
			peer.ReconnectDelay = peer.ReconnectDelay * reconnectBackoffFactor
		}
		peer.ReconnectCount += 1
		n.config.logger.Info(
			fmt.Sprintf(
				"outbound: delaying %s (retry %d) before reconnecting to %s",
				peer.ReconnectDelay,
				peer.ReconnectCount,
				peer.Address,
			),
			"component", "network",
		)
		time.Sleep(peer.ReconnectDelay)
		if err := n.createOutboundConnection(peer); err != nil {
			n.config.logger.Error(
				fmt.Sprintf(
					"outbound: failed to establish connection to %s: %s",
					peer.Address,
					err,
				),
				"component", "network",
			)
			continue
		}
		peer.ReconnectCount = 0
		return
	}
}

// outboundSocketControl is a helper function for setting socket options outbound sockets
func outboundSocketControl(network, address string, c syscall.RawConn) error {
	var innerErr error
	err := c.Control(func(fd uintptr) {
		err := unix.SetsockoptInt(
			int(fd),
			unix.SOL_SOCKET,
			unix.SO_REUSEADDR,
			1,
		)
		if err != nil {
			innerErr = err
			return
		}
		err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		if err != nil {
			innerErr = err
			return
		}
	})
	if innerErr != nil {
		return innerErr
	}
	if err != nil {
		return err
	}
	return nil
}
