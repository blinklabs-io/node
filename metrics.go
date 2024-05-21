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
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (n *Node) StartMetrics() error {
	http.Handle("/metrics", promhttp.Handler())
	n.config.logger.Info("listening for prometheus metrics connections on :12798")
	// TODO: make this configurable
	err := http.ListenAndServe(":12798", nil)
	if err != nil {
		return err
	}
	return nil
}
