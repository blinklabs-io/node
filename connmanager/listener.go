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

package connmanager

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"github.com/blinklabs-io/dingo/event"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"golang.org/x/sys/unix"
)

type ListenerConfig struct {
	UseNtC         bool
	Listener       net.Listener
	ListenNetwork  string
	ListenAddress  string
	ReuseAddress   bool
	ConnectionOpts []ouroboros.ConnectionOptionFunc
}

func (c *ConnectionManager) startListeners() error {
	for _, l := range c.config.Listeners {
		if err := c.startListener(l); err != nil {
			return err
		}
	}
	return nil
}

func (c *ConnectionManager) startListener(l ListenerConfig) error {
	// Create listener if none is provided
	if l.Listener == nil {
		listenConfig := net.ListenConfig{}
		if l.ReuseAddress {
			listenConfig.Control = listenerSocketControl
		}
		listener, err := listenConfig.Listen(
			context.Background(),
			l.ListenNetwork,
			l.ListenAddress,
		)
		if err != nil {
			return fmt.Errorf("failed to open listening socket: %s", err)
		}
		l.Listener = listener
	}
	// Build connection options
	defaultConnOpts := []ouroboros.ConnectionOptionFunc{
		ouroboros.WithLogger(c.config.Logger),
		ouroboros.WithNodeToNode(!l.UseNtC),
		ouroboros.WithServer(true),
	}
	defaultConnOpts = append(
		defaultConnOpts,
		l.ConnectionOpts...,
	)
	go func() {
		for {
			// Accept connection
			conn, err := l.Listener.Accept()
			if err != nil {
				c.config.Logger.Error(
					fmt.Sprintf("listener: accept failed: %s", err),
				)
				continue
			}
			c.config.Logger.Info(
				fmt.Sprintf(
					"listener: accepted connection from %s",
					conn.RemoteAddr(),
				),
			)
			// Setup Ouroboros connection
			connOpts := append(
				defaultConnOpts,
				ouroboros.WithConnection(conn),
			)
			oConn, err := ouroboros.NewConnection(connOpts...)
			if err != nil {
				c.config.Logger.Error(
					fmt.Sprintf(
						"listener: failed to setup connection: %s",
						err,
					),
				)
				continue
			}
			// Add to connection manager
			c.AddConnection(oConn)
			// Generate event
			c.config.EventBus.Publish(
				InboundConnectionEventType,
				event.NewEvent(
					InboundConnectionEventType,
					InboundConnectionEvent{
						ConnectionId: oConn.Id(),
						LocalAddr:    conn.LocalAddr(),
						RemoteAddr:   conn.RemoteAddr(),
					},
				),
			)
		}
	}()
	return nil
}

// listenerSocketControl is a helper function for setting socket options on listener sockets
func listenerSocketControl(network, address string, c syscall.RawConn) error {
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