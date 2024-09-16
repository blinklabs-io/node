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
	"fmt"
	"log/slog"
	"net"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/node"
)

func Run(logger *slog.Logger) error {
	// TODO: make this configurable
	l, err := net.Listen("tcp", ":3001")
	if err != nil {
		return err
	}
	logger.Info("listening for ouroboros node-to-node connections on :3001")
	n, err := node.New(
		node.NewConfig(
			node.WithIntersectTip(true),
			node.WithLogger(logger),
			// TODO: uncomment and make this configurable
			//node.WithDataDir(".data"),
			// TODO: make this configurable
			node.WithNetwork("preview"),
			node.WithListeners(
				node.ListenerConfig{
					Listener: l,
				},
			),
			// TODO: make this configurable
			//node.WithTracing(true),
			// TODO: replace with parsing topology file
			node.WithTopologyConfig(
				&ouroboros.TopologyConfig{
					PublicRoots: []ouroboros.TopologyConfigP2PPublicRoot{
						{
							AccessPoints: []ouroboros.TopologyConfigP2PAccessPoint{
								{
									Address: "preview-node.play.dev.cardano.org",
									Port:    3001,
								},
							},
						},
					},
				},
			),
		),
	)
	if err != nil {
		return err
	}
	go func() {
		if err := n.StartMetrics(); err != nil {
			logger.Error(fmt.Sprintf("failed to start metrics listener %v", err))
		}
	}()
	if err := n.Run(); err != nil {
		return err
	}
	return nil
}
