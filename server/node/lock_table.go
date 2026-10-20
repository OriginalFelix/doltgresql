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
	"strings"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
	vitess "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/dolthub/doltgresql/core"
	"github.com/dolthub/doltgresql/core/tablelock"
	"github.com/dolthub/doltgresql/postgres/parser/pgcode"
	"github.com/dolthub/doltgresql/postgres/parser/pgerror"
)

// LockTableTarget is a table named in a LOCK TABLE statement.
type LockTableTarget struct {
	Database string
	Schema   string
	Table    string
}

// String returns the name of the table as it was written.
func (t LockTableTarget) String() string {
	if len(t.Schema) > 0 {
		return t.Schema + "." + t.Table
	}
	return t.Table
}

// LockTable implements LOCK TABLE.
type LockTable struct {
	Tables []LockTableTarget
	Mode   tablelock.Mode
	NoWait bool
}

var _ sql.ExecSourceRel = (*LockTable)(nil)
var _ vitess.Injectable = (*LockTable)(nil)

// Children implements the interface sql.ExecSourceRel.
func (l *LockTable) Children() []sql.Node {
	return nil
}

// IsReadOnly implements the interface sql.ExecSourceRel. Like Postgres, read-only transactions may only take locks
// that do not conflict with other readers.
func (l *LockTable) IsReadOnly() bool {
	return l.Mode <= tablelock.RowExclusive
}

// Resolved implements the interface sql.ExecSourceRel.
func (l *LockTable) Resolved() bool {
	return true
}

// RowIter implements the interface sql.ExecSourceRel.
func (l *LockTable) RowIter(ctx *sql.Context, r sql.Row) (sql.RowIter, error) {
	// All tables are resolved before any lock is taken, so that a missing table does not leave some locks behind.
	keys := make([]tablelock.Key, 0, len(l.Tables))
	for _, target := range l.Tables {
		key, ok, err := l.resolve(ctx, target)
		if err != nil {
			return nil, err
		}
		if ok {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		if _, err := tablelock.AcquireForTransaction(ctx, key, l.Mode, l.NoWait); err != nil {
			return nil, err
		}
	}
	// Changes committed by other sessions before the locks were granted must be visible to the rest of the
	// transaction, otherwise the locks could not be used to serialize access to the tables.
	if _, err := tablelock.RefreshSnapshot(ctx); err != nil {
		return nil, err
	}
	return sql.RowsToRowIter(), nil
}

// resolve finds the table that the target refers to, returning false if the table is not one that can be locked.
func (l *LockTable) resolve(ctx *sql.Context, target LockTableTarget) (tablelock.Key, bool, error) {
	db, err := core.GetSqlDatabaseFromContext(ctx, target.Database)
	if err != nil {
		return tablelock.Key{}, false, err
	}
	if db == nil {
		return tablelock.Key{}, false, pgerror.Newf(pgcode.InvalidCatalogName, `database "%s" does not exist`, target.Database)
	}
	schemaDb, ok := db.(sql.SchemaDatabase)
	if !ok {
		return tablelock.Key{}, false, nil
	}
	searchPath := []string{target.Schema}
	if len(target.Schema) == 0 {
		if searchPath, err = core.SearchPath(ctx); err != nil {
			return tablelock.Key{}, false, err
		}
	}
	for _, schemaName := range searchPath {
		schema, ok, err := schemaDb.GetSchema(ctx, schemaName)
		if err != nil {
			return tablelock.Key{}, false, err
		}
		if !ok {
			continue
		}
		table, ok, err := schema.GetTableInsensitive(ctx, target.Table)
		if err != nil {
			return tablelock.Key{}, false, err
		}
		if !ok {
			continue
		}
		return tablelock.KeyForTable(ctx, table)
	}
	return tablelock.Key{}, false, pgerror.Newf(pgcode.UndefinedTable, `relation "%s" does not exist`, target)
}

// Schema implements the interface sql.ExecSourceRel.
func (l *LockTable) Schema(ctx *sql.Context) sql.Schema {
	return nil
}

// String implements the interface sql.ExecSourceRel.
func (l *LockTable) String() string {
	tables := make([]string, len(l.Tables))
	for i, target := range l.Tables {
		tables[i] = target.String()
	}
	return fmt.Sprintf("LOCK TABLE %s IN %s MODE", strings.Join(tables, ", "), l.Mode)
}

// WithChildren implements the interface sql.ExecSourceRel.
func (l *LockTable) WithChildren(ctx *sql.Context, children ...sql.Node) (sql.Node, error) {
	return plan.NillaryWithChildren(l, children...)
}

// WithResolvedChildren implements the interface vitess.Injectable.
func (l *LockTable) WithResolvedChildren(ctx context.Context, children []any) (any, error) {
	if len(children) != 0 {
		return nil, ErrVitessChildCount.New(0, len(children))
	}
	return l, nil
}
