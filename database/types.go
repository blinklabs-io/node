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

package database

import (
	"database/sql/driver"
	"fmt"
	"math/big"
)

type Rat struct {
	*big.Rat
}

func (r Rat) Value() (driver.Value, error) {
	if r.Rat == nil {
		return "", nil
	}
	return r.Rat.String(), nil
}

func (r *Rat) Scan(val any) error {
	if r.Rat == nil {
		r.Rat = new(big.Rat)
	}
	v, ok := val.(string)
	if !ok {
		return fmt.Errorf("value was not expected type, wanted string, got %T", val)
	}
	if _, ok := r.SetString(v); !ok {
		return fmt.Errorf("failed to set big.Rat value from string: %s", v)
	}
	return nil
}
