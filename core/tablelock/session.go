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

package tablelock

import (
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/dolthub/dolt/go/libraries/doltcore/doltdb"
	"github.com/dolthub/dolt/go/libraries/doltcore/sqle/dsess"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/dolthub/doltgresql/core"
)

// KeyForTable returns the lock key of the table, and whether the table can be locked at all. Only tables that are
// stored in a Dolt database (user tables) can be locked, system and catalog tables are never locked.
func KeyForTable(ctx *sql.Context, table sql.Table) (Key, bool, error) {
	if table == nil {
		return Key{}, false, nil
	}
	// Dolt's user tables, including their indexed and writable variants, know the Dolt database that they belong to.
	doltTable, ok := sql.GetUnderlyingTable(table).(interface {
		DatabaseSchema() sql.DatabaseSchema
	})
	if !ok {
		return Key{}, false, nil
	}
	db := doltTable.DatabaseSchema()
	revDb, ok := db.(dsess.RevisionDatabase)
	if !ok {
		return Key{}, false, nil
	}
	schemaName := db.SchemaName()
	baseName, revision := doltdb.SplitRevisionDbName(revDb.RevisionQualifiedName())
	if revision == "" {
		head, ok, err := dsess.DSessFromSess(ctx.Session).CurrentHead(ctx, baseName)
		if err != nil {
			return Key{}, false, err
		}
		if ok {
			revision = head
		}
	}
	return Key{
		Database: strings.ToLower(baseName) + "/" + strings.ToLower(revision),
		Schema:   schemaName,
		Table:    table.Name(),
	}, true, nil
}

// AcquireForTransaction obtains the lock on behalf of the current session, and holds it until the current transaction
// commits or rolls back. Returns whether the call had to wait for another session.
func AcquireForTransaction(ctx *sql.Context, key Key, mode Mode, noWait bool) (waited bool, err error) {
	if ctx.GetTransaction() == nil {
		return false, errors.Errorf("table locks require an active transaction")
	}
	sessionID := ctx.Session.ID()
	opts := Options{NoWait: noWait}
	if opts.LockTimeout, err = durationVariable(ctx, "lock_timeout"); err != nil {
		return false, err
	}
	if opts.DeadlockTimeout, err = durationVariable(ctx, "deadlock_timeout"); err != nil {
		return false, err
	}
	// Every lock of the session shares one release callback, so it only needs to be registered with the first lock.
	alreadyRegistered := Global.HasLocks(sessionID)
	waited, err = Global.Acquire(ctx, sessionID, key, mode, opts)
	if err != nil {
		return waited, err
	}
	if !alreadyRegistered {
		if err = core.AddTransactionEndCallback(ctx, func() {
			Global.ReleaseAll(sessionID)
		}); err != nil {
			Global.ReleaseAll(sessionID)
			return waited, err
		}
	}
	return waited, nil
}

// RefreshSnapshot starts a new transaction for the session if the current one has not written anything yet, so that
// statements that run after a lock has been acquired see all changes committed before the lock was granted. Returns
// whether the snapshot was refreshed.
func RefreshSnapshot(ctx *sql.Context) (bool, error) {
	tx := ctx.GetTransaction()
	if tx == nil {
		return false, nil
	}
	sess := dsess.DSessFromSess(ctx.Session)
	if len(sess.DirtyDatabases()) > 0 {
		return false, nil
	}
	if hasSavepoints, err := core.HasSavepoints(ctx); err != nil || hasSavepoints {
		return false, err
	}
	characteristic := sql.ReadWrite
	if tx.IsReadOnly() {
		characteristic = sql.ReadOnly
	}
	if _, err := sess.StartTransaction(ctx, characteristic); err != nil {
		return false, err
	}
	// Cached root objects (such as sequences) were loaded from the previous snapshot. They must not be written back
	// over the new snapshot when the statement finishes.
	if err := core.ClearCachedRootObjects(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// durationVariable reads a session variable that holds a number of milliseconds.
func durationVariable(ctx *sql.Context, name string) (time.Duration, error) {
	val, err := ctx.GetSessionVariable(ctx, name)
	if err != nil {
		return 0, err
	}
	var ms int64
	switch val := val.(type) {
	case int64:
		ms = val
	case int32:
		ms = int64(val)
	case int:
		ms = int64(val)
	case uint64:
		ms = int64(val)
	default:
		return 0, nil
	}
	return time.Duration(ms) * time.Millisecond, nil
}
