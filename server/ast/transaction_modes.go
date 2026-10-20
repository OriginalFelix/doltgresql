// Copyright 2026 Dolthub, Inc.
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

	"github.com/dolthub/doltgresql/postgres/parser/sem/tree"
	pgnodes "github.com/dolthub/doltgresql/server/node"
)

// TransactionModeSettings returns the parameters that the transaction modes set. These are the characteristics of
// the current transaction (transaction_isolation, transaction_read_only and transaction_deferrable), or the defaults
// for the session's subsequent transactions if |sessionDefaults| is true (default_transaction_isolation, etc.).
func TransactionModeSettings(modes *tree.TransactionModes, sessionDefaults bool) ([]pgnodes.TransactionSetting, error) {
	if modes == nil {
		return nil, nil
	}
	if modes.UserPriority != tree.UnspecifiedUserPriority {
		return nil, errors.Errorf("user priority is not yet supported")
	}
	if modes.AsOf.Expr != nil {
		return nil, errors.Errorf("AS OF is not yet supported")
	}
	prefix := "transaction_"
	if sessionDefaults {
		prefix = "default_transaction_"
	}
	var settings []pgnodes.TransactionSetting
	switch modes.Isolation {
	case tree.UnspecifiedIsolation:
	case tree.ReadUncommittedIsolation:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "isolation", Value: "read uncommitted"})
	case tree.ReadCommittedIsolation:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "isolation", Value: "read committed"})
	case tree.RepeatableReadIsolation:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "isolation", Value: "repeatable read"})
	case tree.SerializableIsolation:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "isolation", Value: "serializable"})
	default:
		return nil, errors.Errorf("isolation level %s is not supported", modes.Isolation)
	}
	switch modes.ReadWriteMode {
	case tree.UnspecifiedReadWriteMode:
	case tree.ReadOnly:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "read_only", Value: "on"})
	case tree.ReadWrite:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "read_only", Value: "off"})
	default:
		return nil, errors.Errorf("unknown READ/WRITE setting")
	}
	switch modes.Deferrable {
	case tree.UnspecifiedDeferrableMode:
	case tree.Deferrable:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "deferrable", Value: "on"})
	case tree.NotDeferrable:
		settings = append(settings, pgnodes.TransactionSetting{Name: prefix + "deferrable", Value: "off"})
	default:
		return nil, errors.Errorf("unknown DEFERRABLE setting")
	}
	return settings, nil
}
