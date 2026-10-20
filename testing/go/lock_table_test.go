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

package _go

import (
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestLockTable(t *testing.T) {
	RunScripts(t, []ScriptTest{
		{
			Name: "LOCK TABLE syntax and lock modes",
			SetUpScript: []string{
				"CREATE TABLE lock_test (pk INT PRIMARY KEY)",
				"CREATE TABLE lock_test2 (pk INT PRIMARY KEY)",
				"INSERT INTO lock_test VALUES (1), (2)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "BEGIN"},
				{Query: "LOCK TABLE lock_test IN ACCESS SHARE MODE", ExpectedTag: "LOCK TABLE"},
				{Query: "LOCK TABLE public.lock_test IN ROW SHARE MODE"},
				{Query: "LOCK TABLE lock_test IN ROW EXCLUSIVE MODE"},
				{Query: "LOCK TABLE lock_test IN SHARE UPDATE EXCLUSIVE MODE"},
				{Query: "LOCK TABLE lock_test IN SHARE MODE"},
				{Query: "LOCK TABLE lock_test IN SHARE ROW EXCLUSIVE MODE"},
				{Query: "LOCK TABLE lock_test IN EXCLUSIVE MODE"},
				{Query: "LOCK TABLE lock_test IN ACCESS EXCLUSIVE MODE NOWAIT"},
				{Query: "LOCK lock_test"},
				{Query: "LOCK TABLE ONLY lock_test, lock_test2 * IN EXCLUSIVE MODE"},
				{
					Query:    "SELECT * FROM lock_test ORDER BY pk",
					Expected: []sql.Row{{1}, {2}},
				},
				{Query: "COMMIT"},
			},
		},
		{
			Name: "LOCK TABLE errors",
			SetUpScript: []string{
				"CREATE TABLE lock_test (pk INT PRIMARY KEY)",
				"CREATE VIEW lock_view AS SELECT * FROM lock_test",
			},
			Assertions: []ScriptTestAssertion{
				{
					Query:           "LOCK TABLE lock_test",
					ExpectedErr:     "LOCK TABLE can only be used in transaction blocks",
					ExpectedErrCode: "25P01",
				},
				{Query: "BEGIN"},
				{
					Query:           "LOCK TABLE missing_table",
					ExpectedErr:     `relation "missing_table" does not exist`,
					ExpectedErrCode: "42P01",
				},
				{Query: "ROLLBACK"},
				{Query: "BEGIN"},
				{
					Query:       "LOCK TABLE lock_test, missing_schema.lock_test",
					ExpectedErr: `relation "missing_schema.lock_test" does not exist`,
				},
				{Query: "ROLLBACK"},
			},
		},
		{
			Name: "LOCK, EXCLUSIVE and MODE remain usable as identifiers",
			SetUpScript: []string{
				"CREATE TABLE lock (mode INT PRIMARY KEY, exclusive TEXT)",
				"INSERT INTO lock VALUES (1, 'a')",
			},
			Assertions: []ScriptTestAssertion{
				{
					Query:    "SELECT mode, exclusive FROM lock",
					Expected: []sql.Row{{1, "a"}},
				},
				{Query: "BEGIN"},
				{Query: "LOCK TABLE lock IN EXCLUSIVE MODE"},
				{Query: "COMMIT"},
			},
		},
		{
			Name: "LOCK TABLE privileges",
			SetUpScript: []string{
				"CREATE TABLE lock_test (pk INT PRIMARY KEY)",
				"CREATE USER reader PASSWORD 'password'",
				"GRANT USAGE ON SCHEMA public TO reader",
				"GRANT SELECT ON lock_test TO reader",
				"CREATE USER inserter PASSWORD 'password'",
				"GRANT USAGE ON SCHEMA public TO inserter",
				"GRANT INSERT ON lock_test TO inserter",
			},
			Assertions: []ScriptTestAssertion{
				{Username: "reader", Password: "password", Query: "BEGIN"},
				{Username: "reader", Password: "password", Query: "LOCK TABLE lock_test IN ACCESS SHARE MODE"},
				{
					Username:    "reader",
					Password:    "password",
					Query:       "LOCK TABLE lock_test IN ROW EXCLUSIVE MODE",
					ExpectedErr: "permission denied for table lock_test",
				},
				{Username: "reader", Password: "password", Query: "ROLLBACK"},
				{Username: "inserter", Password: "password", Query: "BEGIN"},
				{Username: "inserter", Password: "password", Query: "LOCK TABLE lock_test IN ROW EXCLUSIVE MODE"},
				{
					Username:    "inserter",
					Password:    "password",
					Query:       "LOCK TABLE lock_test IN EXCLUSIVE MODE",
					ExpectedErr: "permission denied for table lock_test",
				},
				{Username: "inserter", Password: "password", Query: "ROLLBACK"},
			},
		},
	})
}

func TestLockTableConcurrency(t *testing.T) {
	RunTransactionTests(t, []ScriptTest{
		{
			Name: "EXCLUSIVE blocks writers but not readers",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
				"INSERT INTO t VALUES (1)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ LOCK TABLE t IN EXCLUSIVE MODE"},
				{Query: "/* client b */ SELECT * FROM t", Expected: []sql.Row{{1}}},
				{Query: "/* client b */ INSERT INTO t VALUES (2)", ExpectedBlocking: true},
				{Query: "/* client c */ UPDATE t SET pk = 10 WHERE pk = 1", ExpectedBlocking: true},
				{Query: "/* client a */ INSERT INTO t VALUES (3)"},
				{Query: "/* client a */ COMMIT"},
				// Wait for the blocked statements of both clients to finish.
				{Query: "/* client c */ SELECT 1", Expected: []sql.Row{{1}}},
				{Query: "/* client b */ SELECT * FROM t ORDER BY pk", Expected: []sql.Row{{2}, {3}, {10}}},
			},
		},
		{
			Name: "ACCESS EXCLUSIVE blocks readers",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
				"INSERT INTO t VALUES (1)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ LOCK t"},
				{Query: "/* client b */ SELECT * FROM t", ExpectedBlocking: true},
				{Query: "/* client a */ ROLLBACK"},
				{Query: "/* client c */ SELECT * FROM t", Expected: []sql.Row{{1}}},
			},
		},
		{
			Name: "NOWAIT fails instead of waiting",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ LOCK TABLE t IN SHARE MODE"},
				{Query: "/* client b */ BEGIN"},
				// SHARE locks do not conflict with each other.
				{Query: "/* client b */ LOCK TABLE t IN SHARE MODE NOWAIT"},
				{
					Query:           "/* client b */ LOCK TABLE t IN EXCLUSIVE MODE NOWAIT",
					ExpectedErr:     `could not obtain lock on relation "t"`,
					ExpectedErrCode: "55P03",
				},
				{Query: "/* client b */ ROLLBACK"},
				{Query: "/* client a */ COMMIT"},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client b */ LOCK TABLE t IN EXCLUSIVE MODE NOWAIT"},
				{Query: "/* client b */ COMMIT"},
			},
		},
		{
			Name: "locks are released when a client disconnects",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ LOCK t"},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client b */ LOCK t", ExpectedBlocking: true},
				{Query: "/* client a */ DISCONNECT", CloseClient: true},
				{Query: "/* client b */ COMMIT"},
			},
		},
		{
			Name: "implicit locks of autocommit statements are released",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ INSERT INTO t VALUES (1)"},
				{Query: "/* client a */ SELECT * FROM t", Expected: []sql.Row{{1}}},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client b */ LOCK t NOWAIT"},
				{Query: "/* client b */ COMMIT"},
			},
		},
		{
			Name: "implicit locks are held until the transaction ends",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ INSERT INTO t VALUES (1)"},
				{Query: "/* client b */ BEGIN"},
				// The insert holds ROW EXCLUSIVE, which conflicts with SHARE.
				{
					Query:       "/* client b */ LOCK TABLE t IN SHARE MODE NOWAIT",
					ExpectedErr: `could not obtain lock on relation "t"`,
				},
				{Query: "/* client b */ ROLLBACK"},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client b */ LOCK TABLE t IN SHARE MODE", ExpectedBlocking: true},
				{Query: "/* client a */ COMMIT"},
				// The lock was granted after client a committed, so its changes must be visible.
				{Query: "/* client b */ SELECT * FROM t", Expected: []sql.Row{{1}}},
				{Query: "/* client b */ COMMIT"},
			},
		},
		{
			Name: "LOCK TABLE serializes read-modify-write transactions",
			SetUpScript: []string{
				"CREATE TABLE ids (id INT PRIMARY KEY)",
				"INSERT INTO ids VALUES (1)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client a */ LOCK TABLE ids IN EXCLUSIVE MODE"},
				{Query: "/* client b */ LOCK TABLE ids IN EXCLUSIVE MODE", ExpectedBlocking: true},
				{Query: "/* client a */ SELECT max(id) FROM ids", Expected: []sql.Row{{1}}},
				{Query: "/* client a */ INSERT INTO ids VALUES (2)"},
				{Query: "/* client a */ COMMIT"},
				// Client b started its transaction before client a committed, but must see client a's row once it
				// holds the lock.
				{Query: "/* client b */ SELECT max(id) FROM ids", Expected: []sql.Row{{2}}},
				{Query: "/* client b */ INSERT INTO ids VALUES (3)"},
				{Query: "/* client b */ COMMIT"},
				{Query: "/* client c */ SELECT * FROM ids ORDER BY id", Expected: []sql.Row{{1}, {2}, {3}}},
			},
		},
		{
			Name: "deadlocks are detected",
			SetUpScript: []string{
				"CREATE TABLE t1 (pk INT PRIMARY KEY)",
				"CREATE TABLE t2 (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				// Client b checks for deadlocks first, so it is the one that fails.
				{Query: "/* client a */ SET deadlock_timeout = 10000"},
				{Query: "/* client b */ SET deadlock_timeout = 100"},
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client a */ LOCK t1"},
				{Query: "/* client b */ LOCK t2"},
				{Query: "/* client a */ LOCK t2", ExpectedBlocking: true},
				{
					Query:           "/* client b */ LOCK t1",
					ExpectedErr:     "deadlock detected",
					ExpectedErrCode: "40P01",
				},
				{Query: "/* client b */ ROLLBACK"},
				{Query: "/* client a */ COMMIT"},
			},
		},
		{
			Name: "pg_locks shows granted and awaited table locks",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ LOCK TABLE t IN EXCLUSIVE MODE"},
				{Query: "/* client b */ BEGIN"},
				{Query: "/* client b */ LOCK TABLE t IN SHARE MODE", ExpectedBlocking: true},
				{
					Query: "/* client c */ SELECT l.locktype, l.mode, l.granted FROM pg_locks l " +
						"JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 't' ORDER BY l.granted DESC",
					Expected: []sql.Row{{"relation", "ExclusiveLock", "t"}, {"relation", "ShareLock", "f"}},
				},
				{Query: "/* client a */ COMMIT"},
				{Query: "/* client b */ COMMIT"},
				{
					Query:    "/* client c */ SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 't'",
					Expected: []sql.Row{{0}},
				},
				// Like in Postgres, reading pg_locks shows the lock that the query itself holds on pg_locks.
				{
					Query: "/* client c */ SELECT c.relname, l.mode, l.granted FROM pg_locks l " +
						"JOIN pg_class c ON c.oid = l.relation WHERE l.pid = pg_backend_pid()",
					Expected: []sql.Row{{"pg_class", "AccessShareLock", "t"}, {"pg_locks", "AccessShareLock", "t"}},
				},
			},
		},
		{
			Name: "lock_timeout limits the wait",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "/* client a */ BEGIN"},
				{Query: "/* client a */ LOCK t"},
				{Query: "/* client b */ SET lock_timeout = 50"},
				{Query: "/* client b */ BEGIN"},
				{
					Query:           "/* client b */ LOCK t IN ACCESS SHARE MODE",
					ExpectedErr:     "canceling statement due to lock timeout",
					ExpectedErrCode: "55P03",
				},
				{Query: "/* client b */ ROLLBACK"},
				{
					Query:       "/* client b */ SELECT * FROM t",
					ExpectedErr: "canceling statement due to lock timeout",
				},
				{Query: "/* client a */ COMMIT"},
				{Query: "/* client b */ SELECT * FROM t", Expected: []sql.Row{}},
			},
		},
	})
}
