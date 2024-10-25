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
	_ "net/http/pprof"
	"os"

	"github.com/blinklabs-io/node"
	"github.com/blinklabs-io/node/config/cardano"
	"github.com/blinklabs-io/node/internal/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Run(logger *slog.Logger) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	logger.Debug(fmt.Sprintf("config: %+v", cfg))
	logger.Debug(
		fmt.Sprintf("topology: %+v", config.GetTopologyConfig()),
	)
	l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port))
	if err != nil {
		return err
	}
	var nodeCfg *cardano.CardanoNodeConfig
	if cfg.CardanoConfig != "" {
		tmpCfg, err := cardano.NewCardanoNodeConfigFromFile(cfg.CardanoConfig)
		if err != nil {
			return err
		}
		nodeCfg = tmpCfg
		logger.Debug(
			fmt.Sprintf(
				"node: cardano node config: %+v",
				nodeCfg,
			),
		)
	}
	logger.Info(
		fmt.Sprintf(
			"node: listening for ouroboros node-to-node connections on %s",
			fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port),
		),
	)
	n, err := node.New(
		node.NewConfig(
			node.WithLogger(logger),
			node.WithDatabasePath(cfg.DatabasePath),
			node.WithNetwork(cfg.Network),
			node.WithCardanoNodeConfig(nodeCfg),
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
	// Metrics and debug listener
	http.Handle("/metrics", promhttp.Handler())
	logger.Info(
		fmt.Sprintf(
			"node: serving prometheus metrics on %s",
			fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.MetricsPort),
		),
	)
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.MetricsPort), nil); err != nil {
			logger.Error(
				fmt.Sprintf("node: failed to start metrics listener: %s", err),
			)
			os.Exit(1)
		}
	}()
	if err := n.Run(); err != nil {
		return err
	}
	return nil
}
