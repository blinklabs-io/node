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

package config

import (
	"fmt"

	"github.com/blinklabs-io/node/topology"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	BindAddr      string `split_words:"true"`
	CardanoConfig string `envconfig:"config"`
	Network       string
	MetricsPort   uint `split_words:"true"`
	Port          uint
	Topology      string
}

var globalConfig = &Config{
	BindAddr:      "0.0.0.0",
	CardanoConfig: "./configs/cardano/preview/config.json",
	Network:       "preview",
	MetricsPort:   12798,
	Port:          3001,
	Topology:      "",
}

func LoadConfig() (*Config, error) {
	err := envconfig.Process("cardano", globalConfig)
	if err != nil {
		return nil, fmt.Errorf("error processing environment: %+v", err)
	}
	_, err = LoadTopologyConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading topology: %+v", err)
	}
	return globalConfig, nil
}

func GetConfig() *Config {
	return globalConfig
}

var globalTopologyConfig = &topology.TopologyConfig{
	BootstrapPeers: []topology.TopologyConfigP2PAccessPoint{
		{
			Address: "preview-node.play.dev.cardano.org",
			Port:    3001,
		},
	},
}

func LoadTopologyConfig() (*topology.TopologyConfig, error) {
	if globalConfig.Topology == "" {
		return globalTopologyConfig, nil
	}
	tc, err := topology.NewTopologyConfigFromFile(globalConfig.Topology)
	if err != nil {
		return nil, fmt.Errorf("failed to load topology file: %+v", err)
	}
	// update globalTopologyConfig
	globalTopologyConfig = tc
	return globalTopologyConfig, nil
}

func GetTopologyConfig() *topology.TopologyConfig {
	return globalTopologyConfig
}
