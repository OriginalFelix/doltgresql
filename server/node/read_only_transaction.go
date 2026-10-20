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
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/transform"

	"github.com/dolthub/doltgresql/core"
	"github.com/dolthub/doltgresql/postgres/parser/pgcode"
	"github.com/dolthub/doltgresql/postgres/parser/pgerror"
)

// checkReadOnlyTransaction returns an error if the statement would write while the current transaction is read-only.
// A transaction is read-only when it was started with BEGIN READ ONLY, when default_transaction_read_only was on when
// it started (see SET SESSION CHARACTERISTICS), or when SET TRANSACTION READ ONLY was run within it. Like in Postgres,
// changes to temporary tables, as well as statements that only affect the session, remain permitted.
// https://www.postgresql.org/docs/current/sql-set-transaction.html
func checkReadOnlyTransaction(ctx *sql.Context, node sql.Node) error {
	if node.IsReadOnly() || ctx.GetTransaction() == nil {
		return nil
	}
	readOnly, err := ctx.GetSessionVariable(ctx, "transaction_read_only")
	if err != nil || !isOn(readOnly) {
		return err
	}
	switch node.(type) {
	case *SetSetting, *ResetSettings, *plan.Set, *plan.StartTransaction, *plan.Commit, *plan.Rollback,
		*plan.CreateSavepoint, *plan.RollbackSavepoint, *plan.ReleaseSavepoint, *plan.PrepareQuery,
		*plan.DeallocateQuery:
		return nil
	}
	command, writesData, writesPermanentTable := "", false, false
	transform.InspectWithOpaque(ctx, node, func(ctx *sql.Context, n sql.Node) bool {
		var target sql.Node
		switch n := n.(type) {
		case *plan.InsertInto:
			command, target = "INSERT", n.Destination
		case *plan.Update:
			command, target = "UPDATE", n.Child
		case *plan.DeleteFrom:
			command, target = "DELETE", n.Child
		default:
			return true
		}
		writesData = true
		if !onlyTemporaryTables(ctx, target) {
			writesPermanentTable = true
		}
		return !writesPermanentTable
	})
	if writesData && !writesPermanentTable {
		return nil
	}
	if tag := core.StatementTag(ctx); len(tag) > 0 {
		command = tag
	} else if !writesData {
		command = "this command"
	}
	return pgerror.Newf(pgcode.ReadOnlySQLTransaction, "cannot execute %s in a read-only transaction", command)
}

// onlyTemporaryTables returns whether every table within the node is a temporary table.
func onlyTemporaryTables(ctx *sql.Context, node sql.Node) bool {
	temporary := true
	transform.InspectWithOpaque(ctx, node, func(ctx *sql.Context, n sql.Node) bool {
		var table sql.Table
		switch n := n.(type) {
		case *plan.ResolvedTable:
			table = n.Table
		case *plan.IndexedTableAccess:
			table = n.TableNode.UnderlyingTable()
		default:
			return true
		}
		if tt, ok := sql.GetUnderlyingTable(table).(sql.TemporaryTable); !ok || !tt.IsTemporary() {
			temporary = false
		}
		return temporary
	})
	return temporary
}

// isOn returns whether the value of a boolean parameter is on.
func isOn(value any) bool {
	switch value := value.(type) {
	case bool:
		return value
	case int8:
		return value != 0
	case int64:
		return value != 0
	case string:
		return value == "on" || value == "true" || value == "1"
	default:
		return false
	}
}
