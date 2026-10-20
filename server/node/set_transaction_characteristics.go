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

package node

import (
	"context"
	"fmt"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
	vitess "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/dolthub/doltgresql/server/settings"
)

// TransactionSetting is a parameter that a transaction mode sets, along with its value.
type TransactionSetting struct {
	Name  string
	Value string
}

// SetTransactionCharacteristics implements SET TRANSACTION, which sets the characteristics of the current
// transaction, and SET SESSION CHARACTERISTICS AS TRANSACTION, which sets the default characteristics of the
// session's subsequent transactions. Both are equivalent to setting the corresponding parameters.
type SetTransactionCharacteristics struct {
	// Session states that the session defaults are set, rather than the characteristics of the current transaction.
	Session  bool
	Settings []TransactionSetting
}

var _ sql.ExecSourceRel = (*SetTransactionCharacteristics)(nil)
var _ vitess.Injectable = (*SetTransactionCharacteristics)(nil)

// Children implements the interface sql.ExecSourceRel.
func (s *SetTransactionCharacteristics) Children() []sql.Node {
	return nil
}

// IsReadOnly implements the interface sql.ExecSourceRel. Like other SET statements, this is permitted in read-only
// transactions.
func (s *SetTransactionCharacteristics) IsReadOnly() bool {
	return true
}

// Resolved implements the interface sql.ExecSourceRel.
func (s *SetTransactionCharacteristics) Resolved() bool {
	return true
}

// RowIter implements the interface sql.ExecSourceRel.
func (s *SetTransactionCharacteristics) RowIter(ctx *sql.Context, r sql.Row) (sql.RowIter, error) {
	for _, setting := range s.Settings {
		if err := settings.Set(ctx, setting.Name, setting.Value, false, !s.Session); err != nil {
			return nil, err
		}
	}
	return sql.RowsToRowIter(), nil
}

// Schema implements the interface sql.ExecSourceRel.
func (s *SetTransactionCharacteristics) Schema(ctx *sql.Context) sql.Schema {
	return nil
}

// String implements the interface sql.ExecSourceRel.
func (s *SetTransactionCharacteristics) String() string {
	if s.Session {
		return fmt.Sprintf("SET SESSION CHARACTERISTICS AS TRANSACTION %v", s.Settings)
	}
	return fmt.Sprintf("SET TRANSACTION %v", s.Settings)
}

// WithChildren implements the interface sql.ExecSourceRel.
func (s *SetTransactionCharacteristics) WithChildren(ctx *sql.Context, children ...sql.Node) (sql.Node, error) {
	return plan.NillaryWithChildren(s, children...)
}

// WithResolvedChildren implements the interface vitess.Injectable.
func (s *SetTransactionCharacteristics) WithResolvedChildren(ctx context.Context, children []any) (any, error) {
	if len(children) != 0 {
		return nil, ErrVitessChildCount.New(0, len(children))
	}
	return s, nil
}
