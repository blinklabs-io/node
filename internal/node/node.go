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
	"net/http"
	"os"

	"github.com/blinklabs-io/node"
	"github.com/blinklabs-io/node/internal/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Run(logger *slog.Logger) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("loaded config: %+v", cfg))
	l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port))
	if err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("listening for ouroboros node-to-node connections on %s", fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)))
	// Metrics listener
	http.Handle("/metrics", promhttp.Handler())
	logger.Info("listening for prometheus metrics connections on :12798")
	go func() {
		// TODO: make this configurable
		if err := http.ListenAndServe(":12798", nil); err != nil {
			logger.Error(fmt.Sprintf("failed to start metrics listener: %s", err))
			os.Exit(1)
		}
	}()
	n, err := node.New(
		node.NewConfig(
			node.WithIntersectTip(true),
			node.WithLogger(logger),
			// TODO: uncomment and make this configurable
			//node.WithDataDir(".data"),
			node.WithNetwork(cfg.Network),
			node.WithListeners(
				node.ListenerConfig{
					Listener: l,
				},
			),
			// Enable metrics with default prometheus registry
			node.WithPrometheusRegistry(prometheus.DefaultRegisterer),
			// TODO: make this configurable
			//node.WithTracing(true),
			node.WithTopologyConfig(config.GetTopologyConfig()),
		),
	)
	if err != nil {
		return err
	}
	if err := n.Run(); err != nil {
		return err
	}
	return nil
}
