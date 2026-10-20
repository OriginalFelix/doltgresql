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

package tree

// LockTableMode represents the lock mode of a LOCK TABLE statement.
type LockTableMode byte

const (
	// LockModeAccessExclusive is the default mode when no mode is given.
	LockModeAccessExclusive LockTableMode = iota
	LockModeAccessShare
	LockModeRowShare
	LockModeRowExclusive
	LockModeShareUpdateExclusive
	LockModeShare
	LockModeShareRowExclusive
	LockModeExclusive
)

var lockTableModeName = [...]string{
	LockModeAccessExclusive:      "ACCESS EXCLUSIVE",
	LockModeAccessShare:          "ACCESS SHARE",
	LockModeRowShare:             "ROW SHARE",
	LockModeRowExclusive:         "ROW EXCLUSIVE",
	LockModeShareUpdateExclusive: "SHARE UPDATE EXCLUSIVE",
	LockModeShare:                "SHARE",
	LockModeShareRowExclusive:    "SHARE ROW EXCLUSIVE",
	LockModeExclusive:            "EXCLUSIVE",
}

func (m LockTableMode) String() string {
	return lockTableModeName[m]
}

// LockTable represents a LOCK [TABLE] statement.
type LockTable struct {
	Tables TableNames
	Mode   LockTableMode
	NoWait bool
}

var _ Statement = &LockTable{}

// Format implements the NodeFormatter interface.
func (node *LockTable) Format(ctx *FmtCtx) {
	ctx.WriteString("LOCK TABLE ")
	sep := ""
	for i := range node.Tables {
		ctx.WriteString(sep)
		ctx.FormatNode(&node.Tables[i])
		sep = ", "
	}
	ctx.WriteString(" IN ")
	ctx.WriteString(node.Mode.String())
	ctx.WriteString(" MODE")
	if node.NoWait {
		ctx.WriteString(" NOWAIT")
	}
}

// StatementType implements the Statement interface.
func (*LockTable) StatementType() StatementType { return Ack }

// StatementTag returns a short string identifying the type of statement.
func (*LockTable) StatementTag() string { return "LOCK TABLE" }

func (node *LockTable) String() string { return AsString(node) }
