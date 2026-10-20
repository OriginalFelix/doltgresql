// Copyright 2023 Dolthub, Inc.
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

package ast

import (
	"github.com/cockroachdb/errors"

	vitess "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/dolthub/doltgresql/postgres/parser/sem/tree"
)

// nodeBeginTransaction handles *tree.BeginTransaction nodes.
func nodeBeginTransaction(ctx *Context, node *tree.BeginTransaction) (*vitess.Begin, error) {
	if node == nil {
		return nil, nil
	}

	// Dolt only implements a single isolation level, so the isolation level and deferrability are not passed to the
	// engine. The connection handler records all modes in the transaction's parameters once the transaction has begun
	// (see TransactionModeSettings), which is also where a read-only transaction is enforced.
	if _, err := TransactionModeSettings(&node.Modes, false); err != nil {
		return nil, err
	}
	var characteristic string
	switch node.Modes.ReadWriteMode {
	case tree.UnspecifiedReadWriteMode:
		characteristic = vitess.TxReadWrite
	case tree.ReadOnly:
		characteristic = vitess.TxReadOnly
	case tree.ReadWrite:
		characteristic = vitess.TxReadWrite
	default:
		return nil, errors.Errorf("unknown READ/WRITE setting")
	}
	return &vitess.Begin{
		TransactionCharacteristic: characteristic,
	}, nil
}
