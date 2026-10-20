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

func TestTransactionCharacteristics(t *testing.T) {
	RunScripts(t, []ScriptTest{
		{
			Name: "SET SESSION CHARACTERISTICS sets the defaults of subsequent transactions",
			Assertions: []ScriptTestAssertion{
				{Query: "SHOW TRANSACTION ISOLATION LEVEL", Expected: []sql.Row{{"read committed"}}},
				{
					Query:       "SET SESSION CHARACTERISTICS AS TRANSACTION ISOLATION LEVEL READ UNCOMMITTED",
					ExpectedTag: "SET",
				},
				{Query: "SHOW default_transaction_isolation", Expected: []sql.Row{{"read uncommitted"}}},
				{Query: "SHOW TRANSACTION ISOLATION LEVEL", Expected: []sql.Row{{"read uncommitted"}}},
				{Query: "BEGIN"},
				{Query: "SHOW transaction_isolation", Expected: []sql.Row{{"read uncommitted"}}},
				{Query: "COMMIT"},
				// Modes given to BEGIN take precedence over the defaults, for that transaction only.
				{Query: "BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY, DEFERRABLE"},
				{Query: "SHOW transaction_isolation", Expected: []sql.Row{{"serializable"}}},
				{Query: "SELECT current_setting('transaction_read_only')", Expected: []sql.Row{{"on"}}},
				{Query: "SELECT current_setting('transaction_deferrable')", Expected: []sql.Row{{"on"}}},
				{Query: "COMMIT"},
				{Query: "SHOW transaction_isolation", Expected: []sql.Row{{"read uncommitted"}}},
				{Query: "SELECT current_setting('transaction_read_only')", Expected: []sql.Row{{"off"}}},
				{Query: "SET SESSION CHARACTERISTICS AS TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE"},
				{Query: "SHOW default_transaction_isolation", Expected: []sql.Row{{"serializable"}}},
				{Query: "SELECT current_setting('default_transaction_read_only')", Expected: []sql.Row{{"on"}}},
				{Query: "SELECT current_setting('default_transaction_deferrable')", Expected: []sql.Row{{"on"}}},
				{Query: "SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE, NOT DEFERRABLE, ISOLATION LEVEL READ COMMITTED"},
				{Query: "SHOW TRANSACTION ISOLATION LEVEL", Expected: []sql.Row{{"read committed"}}},
				{Query: "SELECT current_setting('default_transaction_read_only')", Expected: []sql.Row{{"off"}}},
				{Query: "SELECT current_setting('default_transaction_deferrable')", Expected: []sql.Row{{"off"}}},
			},
		},
		{
			Name: "SET SESSION CHARACTERISTICS is rolled back with its transaction",
			Assertions: []ScriptTestAssertion{
				{Query: "BEGIN"},
				{Query: "SET SESSION CHARACTERISTICS AS TRANSACTION ISOLATION LEVEL REPEATABLE READ"},
				{Query: "ROLLBACK"},
				{Query: "SHOW default_transaction_isolation", Expected: []sql.Row{{"read committed"}}},
				{Query: "BEGIN"},
				{Query: "SET SESSION CHARACTERISTICS AS TRANSACTION ISOLATION LEVEL REPEATABLE READ"},
				{Query: "COMMIT"},
				{Query: "SHOW default_transaction_isolation", Expected: []sql.Row{{"repeatable read"}}},
			},
		},
		{
			Name: "a read-only default is enforced",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
				"INSERT INTO t VALUES (1)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"},
				{Query: "SELECT * FROM t", Expected: []sql.Row{{1}}},
				{
					Query:           "INSERT INTO t VALUES (2)",
					ExpectedErr:     "cannot execute INSERT in a read-only transaction",
					ExpectedErrCode: "25006",
				},
				{Query: "UPDATE t SET pk = 2", ExpectedErr: "cannot execute UPDATE in a read-only transaction"},
				{Query: "DELETE FROM t", ExpectedErr: "cannot execute DELETE in a read-only transaction"},
				{Query: "CREATE TABLE t2 (pk INT)", ExpectedErr: "cannot execute CREATE TABLE in a read-only transaction"},
				{Query: "DROP TABLE t", ExpectedErr: "cannot execute DROP TABLE in a read-only transaction"},
				{Query: "BEGIN"},
				{Query: "INSERT INTO t VALUES (2)", ExpectedErr: "cannot execute INSERT in a read-only transaction"},
				{Query: "ROLLBACK"},
				// An explicit READ WRITE overrides the default.
				{Query: "BEGIN READ WRITE"},
				{Query: "INSERT INTO t VALUES (2)"},
				{Query: "COMMIT"},
				// Settings remain available, including the one that lifts the restriction.
				{Query: "SET search_path TO public"},
				{Query: "SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE"},
				{Query: "INSERT INTO t VALUES (3)"},
				{Query: "SELECT * FROM t ORDER BY pk", Expected: []sql.Row{{1}, {2}, {3}}},
			},
		},
		{
			Name: "SET TRANSACTION sets the characteristics of the current transaction",
			SetUpScript: []string{
				"CREATE TABLE t (pk INT PRIMARY KEY)",
			},
			Assertions: []ScriptTestAssertion{
				{Query: "BEGIN"},
				{Query: "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY", ExpectedTag: "SET"},
				{Query: "SHOW transaction_isolation", Expected: []sql.Row{{"repeatable read"}}},
				{Query: "SHOW default_transaction_isolation", Expected: []sql.Row{{"read committed"}}},
				{Query: "INSERT INTO t VALUES (1)", ExpectedErr: "cannot execute INSERT in a read-only transaction"},
				{Query: "ROLLBACK"},
				{Query: "SHOW transaction_isolation", Expected: []sql.Row{{"read committed"}}},
				{Query: "INSERT INTO t VALUES (1)"},
				{
					// Outside of a transaction block, it only applies to its own implicit transaction.
					Query: "SET TRANSACTION READ ONLY",
					ExpectedNotices: []ExpectedNotice{{
						Severity: "WARNING",
						Code:     "25P01",
						Message:  "SET TRANSACTION can only be used in transaction blocks",
					}},
				},
				{Query: "INSERT INTO t VALUES (2)"},
				{Query: "SELECT * FROM t ORDER BY pk", Expected: []sql.Row{{1}, {2}}},
			},
		},
	})
}
