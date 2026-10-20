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
	"sort"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/transform"

	"github.com/dolthub/doltgresql/core/tablelock"
)

// tableLockRequest is a lock that a statement must hold on a table while it runs.
type tableLockRequest struct {
	key  tablelock.Key
	mode tablelock.Mode
}

// acquireImplicitTableLocks acquires the table locks that Postgres takes implicitly for the statement: reading a table
// takes ACCESS SHARE, modifying its rows takes ROW EXCLUSIVE, and changing its definition takes ACCESS EXCLUSIVE. The
// locks only matter in relation to locks taken explicitly with LOCK TABLE, as these implicit modes never conflict with
// each other, apart from DDL.
// https://www.postgresql.org/docs/current/explicit-locking.html#LOCKING-TABLES
func acquireImplicitTableLocks(ctx *sql.Context, node sql.Node) error {
	if ctx.GetTransaction() == nil {
		return nil
	}
	waited, err := acquireTableLocks(ctx, node)
	if err != nil || !waited || !node.IsReadOnly() {
		return err
	}
	// A read that had to wait for a lock must see the changes committed by the session that held it. Statements that
	// write keep their snapshot, as their tables are bound to the session state before execution begins.
	_, err = tablelock.RefreshSnapshot(ctx)
	return err
}

// acquireTableLocks acquires the implicit locks of the node, returning whether any of them had to wait.
func acquireTableLocks(ctx *sql.Context, node sql.Node) (waited bool, err error) {
	requests := make(map[tableLockRequest]struct{})
	collector := implicitLockCollector{ctx: ctx, requests: requests}
	if err := collector.collect(node); err != nil {
		return false, err
	}
	if len(requests) == 0 {
		return false, nil
	}
	// Acquiring the locks in a consistent order prevents deadlocks between statements that lock the same tables.
	sorted := make([]tableLockRequest, 0, len(requests))
	for request := range requests {
		sorted = append(sorted, request)
	}
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.key != b.key {
			return a.key.Less(b.key)
		}
		return a.mode < b.mode
	})
	for _, request := range sorted {
		requestWaited, err := tablelock.AcquireForTransaction(ctx, request.key, request.mode, false)
		if err != nil {
			return waited, err
		}
		waited = waited || requestWaited
	}
	return waited, nil
}

// implicitLockCollector gathers the implicit locks of a plan.
type implicitLockCollector struct {
	ctx      *sql.Context
	requests map[tableLockRequest]struct{}
}

// collect walks the node and its children, including subqueries within expressions.
func (c implicitLockCollector) collect(node sql.Node) error {
	if node == nil {
		return nil
	}
	var err error
	transform.InspectWithOpaque(c.ctx, node, func(ctx *sql.Context, n sql.Node) bool {
		if err != nil {
			return false
		}
		if err = c.visit(n); err != nil {
			return false
		}
		if expressioner, ok := n.(sql.Expressioner); ok {
			for _, expr := range expressioner.Expressions() {
				transform.InspectExpr(ctx, expr, func(ctx *sql.Context, e sql.Expression) bool {
					if subquery, ok := e.(*plan.Subquery); ok && err == nil {
						err = c.collect(subquery.Query)
					}
					return err != nil
				})
			}
		}
		return err == nil
	})
	return err
}

// visit records the locks that the node itself requires.
func (c implicitLockCollector) visit(n sql.Node) error {
	switch n := n.(type) {
	case *plan.ResolvedTable:
		return c.add(n.Table, tablelock.AccessShare)
	case *plan.IndexedTableAccess:
		return c.add(n.TableNode.UnderlyingTable(), tablelock.AccessShare)
	case *plan.InsertInto:
		return c.addTablesUnder(n.Destination, tablelock.RowExclusive)
	case *plan.Update:
		if table, err := plan.GetUpdatable(n.Child); err == nil {
			return c.add(table, tablelock.RowExclusive)
		}
		// Joined updates may modify any of their tables.
		return c.addTablesUnder(n.Child, tablelock.RowExclusive)
	case *plan.DeleteFrom:
		targets := n.GetDeleteTargets()
		if !n.HasExplicitTargets() {
			targets = []sql.Node{n.Child}
		}
		for _, target := range targets {
			if table, err := plan.GetDeletable(target); err == nil {
				if err = c.add(table, tablelock.RowExclusive); err != nil {
					return err
				}
			} else if err = c.addTablesUnder(target, tablelock.RowExclusive); err != nil {
				return err
			}
		}
	case *plan.Truncate:
		return c.addTablesUnder(n.Child, tablelock.AccessExclusive)
	case *plan.DropTable:
		for _, table := range n.Tables {
			if err := c.addTablesUnder(table, tablelock.AccessExclusive); err != nil {
				return err
			}
		}
	case *plan.AlterIndex:
		// CREATE INDEX blocks writes but not reads, while the other index changes are part of ALTER TABLE.
		mode := tablelock.AccessExclusive
		if n.Action == plan.IndexAction_Create {
			mode = tablelock.Share
		}
		return c.addTablesUnder(n.Table, mode)
	case *plan.AddColumn:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	case *plan.DropColumn:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	case *plan.RenameColumn:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	case *plan.ModifyColumn:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	case *plan.AlterPK:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	case *plan.AlterDefaultSet:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	case *plan.AlterDefaultDrop:
		return c.addTablesUnder(n.Table, tablelock.AccessExclusive)
	}
	return nil
}

// addTablesUnder records the lock for every table within the node.
func (c implicitLockCollector) addTablesUnder(node sql.Node, mode tablelock.Mode) error {
	if node == nil {
		return nil
	}
	var err error
	transform.InspectWithOpaque(c.ctx, node, func(ctx *sql.Context, n sql.Node) bool {
		if err != nil {
			return false
		}
		switch n := n.(type) {
		case *plan.ResolvedTable:
			err = c.add(n.Table, mode)
		case *plan.IndexedTableAccess:
			err = c.add(n.TableNode.UnderlyingTable(), mode)
		}
		return err == nil
	})
	return err
}

// add records the lock for the table, if it is a table that can be locked.
func (c implicitLockCollector) add(table sql.Table, mode tablelock.Mode) error {
	key, ok, err := tablelock.KeyForTable(c.ctx, table)
	if err != nil || !ok {
		return err
	}
	c.requests[tableLockRequest{key: key, mode: mode}] = struct{}{}
	return nil
}
