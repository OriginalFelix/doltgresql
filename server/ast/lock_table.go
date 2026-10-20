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

	vitess "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/dolthub/doltgresql/core/tablelock"
	"github.com/dolthub/doltgresql/postgres/parser/sem/tree"
	"github.com/dolthub/doltgresql/server/auth"
	pgnodes "github.com/dolthub/doltgresql/server/node"
)

// lockTableModes maps the parser's lock modes to the lock manager's modes.
var lockTableModes = map[tree.LockTableMode]tablelock.Mode{
	tree.LockModeAccessShare:          tablelock.AccessShare,
	tree.LockModeRowShare:             tablelock.RowShare,
	tree.LockModeRowExclusive:         tablelock.RowExclusive,
	tree.LockModeShareUpdateExclusive: tablelock.ShareUpdateExclusive,
	tree.LockModeShare:                tablelock.Share,
	tree.LockModeShareRowExclusive:    tablelock.ShareRowExclusive,
	tree.LockModeExclusive:            tablelock.Exclusive,
	tree.LockModeAccessExclusive:      tablelock.AccessExclusive,
}

// nodeLockTable handles *tree.LockTable nodes.
func nodeLockTable(ctx *Context, node *tree.LockTable) (vitess.Statement, error) {
	if node == nil {
		return nil, nil
	}
	mode, ok := lockTableModes[node.Mode]
	if !ok {
		return nil, errors.Errorf("unknown lock mode: %s", node.Mode)
	}
	targets := make([]pgnodes.LockTableTarget, len(node.Tables))
	targetNames := make([]string, 0, len(node.Tables)*3)
	for i := range node.Tables {
		tableName, err := nodeTableName(ctx, &node.Tables[i])
		if err != nil {
			return nil, err
		}
		targets[i] = pgnodes.LockTableTarget{
			Database: tableName.DbQualifier.String(),
			Schema:   tableName.SchemaQualifier.String(),
			Table:    tableName.Name.String(),
		}
		targetNames = append(targetNames, targets[i].Database, targets[i].Schema, targets[i].Table)
	}
	// Postgres permits weaker lock modes with weaker privileges.
	authType := auth.AuthType_LOCK
	switch {
	case mode == tablelock.AccessShare:
		authType = auth.AuthType_LOCK_ACCESS_SHARE
	case mode <= tablelock.RowExclusive:
		authType = auth.AuthType_LOCK_ROW_EXCLUSIVE
	}
	return vitess.InjectedStatement{
		Statement: &pgnodes.LockTable{
			Tables: targets,
			Mode:   mode,
			NoWait: node.NoWait,
		},
		Auth: vitess.AuthInformation{
			AuthType:    authType,
			TargetType:  auth.AuthTargetType_TableIdentifiers,
			TargetNames: targetNames,
		},
	}, nil
}
