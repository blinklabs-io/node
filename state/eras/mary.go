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

	"github.com/blinklabs-io/dingo/config/cardano"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/allegra"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
)

var MaryEraDesc = EraDesc{
	Id:                      mary.EraIdMary,
	Name:                    mary.EraNameMary,
	DecodePParamsFunc:       DecodePParamsMary,
	DecodePParamsUpdateFunc: DecodePParamsUpdateMary,
	PParamsUpdateFunc:       PParamsUpdateMary,
	HardForkFunc:            HardForkMary,
	EpochLengthFunc:         EpochLengthShelley,
}

func DecodePParamsMary(data []byte) (any, error) {
	var ret mary.MaryProtocolParameters
	if _, err := cbor.Decode(data, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func DecodePParamsUpdateMary(data []byte) (any, error) {
	var ret mary.MaryProtocolParameterUpdate
	if _, err := cbor.Decode(data, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func PParamsUpdateMary(currentPParams any, pparamsUpdate any) (any, error) {
	maryPParams, ok := currentPParams.(mary.MaryProtocolParameters)
	if !ok {
		return nil, fmt.Errorf(
			"current PParams (%T) is not expected type",
			currentPParams,
		)
	}
	maryPParamsUpdate, ok := pparamsUpdate.(mary.MaryProtocolParameterUpdate)
	if !ok {
		return nil, fmt.Errorf(
			"PParams update (%T) is not expected type",
			pparamsUpdate,
		)
	}
	maryPParams.Update(&maryPParamsUpdate)
	return maryPParams, nil
}

func HardForkMary(
	nodeConfig *cardano.CardanoNodeConfig,
	prevPParams any,
) (any, error) {
	allegraPParams, ok := prevPParams.(allegra.AllegraProtocolParameters)
	if !ok {
		return nil, fmt.Errorf(
			"previous PParams (%T) are not expected type",
			prevPParams,
		)
	}
	ret := mary.UpgradePParams(allegraPParams)
	return ret, nil
}
