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
	"os"
	"path"

	"github.com/blinklabs-io/node/topology"
	"gopkg.in/yaml.v3"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	path         string
	BindAddr     string `split_words:"true" yaml:"bindAddr"`
	Metrics      MetricsConfig
	Network      string
	NetworkMagic uint32 `yaml:"networkMagic"`
	Port         uint
	Topology     string
}

type MetricsConfig struct {
	BindAddr string `yaml:"bindAddr"`
	Port     uint
}

var globalConfig = &Config{
	BindAddr: "0.0.0.0",
	Network:  "preview",
	Metrics: MetricsConfig{
		BindAddr: "127.0.0.1",
		Port:     12798,
	},
	Port: 3001,
}

func LoadConfig(configFile string) (*Config, error) {
	// Load YAML config if provided
	if configFile != "" {
		f, err := os.Open(configFile)
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(f)
		// Require all fields provided in YAML to exist in our target object
		dec.KnownFields(true)
		if err := dec.Decode(&globalConfig); err != nil {
			return nil, err
		}
		globalConfig.path = path.Dir(configFile)
	}
	// Load environment variables
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
	topologyPath := path.Join(
		globalConfig.path,
		globalConfig.Topology,
	)
	tc, err := topology.NewTopologyConfigFromFile(topologyPath)
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
