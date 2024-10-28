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
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/node/config/cardano"
)

var BabbageEraDesc = EraDesc{
	Id:                babbage.EraIdBabbage,
	Name:              babbage.EraNameBabbage,
	DecodePParamsFunc: DecodePParamsBabbage,
	HardForkFunc:      HardForkBabbage,
}

func DecodePParamsBabbage(data []byte) (any, error) {
	var ret babbage.BabbageProtocolParameters
	if _, err := cbor.Decode(data, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func HardForkBabbage(nodeConfig *cardano.CardanoNodeConfig, prevPParams any) (any, error) {
	alonzoPParams, ok := prevPParams.(alonzo.AlonzoProtocolParameters)
	if !ok {
		return nil, fmt.Errorf("previous PParams (%T) are not expected type", prevPParams)
	}
	ret := babbage.UpgradePParams(alonzoPParams)
	return ret, nil
}
