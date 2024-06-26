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
	opeersharing "github.com/blinklabs-io/gouroboros/protocol/peersharing"
)

func (n *Node) peersharingServerConnOpts() []opeersharing.PeerSharingOptionFunc {
	return []opeersharing.PeerSharingOptionFunc{
		opeersharing.WithShareRequestFunc(n.peersharingShareRequest),
	}
}

func (n *Node) peersharingClientConnOpts() []opeersharing.PeerSharingOptionFunc {
	return []opeersharing.PeerSharingOptionFunc{
		// TODO
	}
}

func (n *Node) peersharingShareRequest(
	ctx opeersharing.CallbackContext,
	amount int,
) ([]opeersharing.PeerAddress, error) {
	// TODO: add hooks for getting peers to share
	return []opeersharing.PeerAddress{}, nil
}
