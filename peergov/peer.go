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

package peergov

import (
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
)

type PeerSource uint16

const (
	PeerSourceUnknown               = 0
	PeerSourceTopologyLocalRoot     = 1
	PeerSourceTopologyPublicRoot    = 2
	PeerSourceTopologyBootstrapPeer = 3
	PeerSourceP2PLedger             = 4
	PeerSourceP2PGossip             = 5
	PeerSourceInboundConn           = 6
)

type Peer struct {
	Address        string
	Source         PeerSource
	ConnectionId   *ouroboros.ConnectionId
	Sharable       bool
	ReconnectCount int
	ReconnectDelay time.Duration
}
