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

package eras

import (
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/node/config/cardano"
)

var ShelleyEraDesc = EraDesc{
	Id:                shelley.EraIdShelley,
	Name:              shelley.EraNameShelley,
	DecodePParamsFunc: DecodePParamsShelley,
	HardForkFunc:      HardForkShelley,
}

func DecodePParamsShelley(data []byte) (any, error) {
	var ret shelley.ShelleyProtocolParameters
	if _, err := cbor.Decode(data, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func HardForkShelley(nodeConfig *cardano.CardanoNodeConfig, prevPParams any) (any, error) {
	// There's no Byron protocol parameters to upgrade from, so this is mostly
	// a dummy call for consistency
	ret := shelley.UpgradePParams(nil)
	shelleyGenesis, err := nodeConfig.ShelleyGenesis()
	if err != nil {
		return nil, err
	}
	ret.UpdateFromGenesis(shelleyGenesis)
	return ret, nil
}
