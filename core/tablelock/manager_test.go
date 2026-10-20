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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testKey = Key{Database: "postgres/main", Schema: "public", Table: "t"}

// noLongWait is used for requests that are expected to be granted, so that a broken test fails instead of hanging.
var noLongWait = Options{LockTimeout: 5 * time.Second}

func TestConflictTable(t *testing.T) {
	// The conflict table must be symmetric.
	for a := AccessShare; a <= AccessExclusive; a++ {
		for b := AccessShare; b <= AccessExclusive; b++ {
			assert.Equal(t, Conflicts(a, b), Conflicts(b, a), "%s / %s", a, b)
		}
	}
	assert.False(t, Conflicts(AccessShare, Exclusive))
	assert.True(t, Conflicts(AccessShare, AccessExclusive))
	assert.False(t, Conflicts(RowExclusive, RowExclusive))
	assert.True(t, Conflicts(RowExclusive, Share))
	assert.False(t, Conflicts(Share, Share))
	assert.True(t, Conflicts(ShareUpdateExclusive, ShareUpdateExclusive))
	assert.True(t, Conflicts(ShareRowExclusive, ShareRowExclusive))
	assert.False(t, Conflicts(RowShare, ShareRowExclusive))
	assert.True(t, Conflicts(Exclusive, RowShare))
}

func TestAcquireCompatibleAndReentrant(t *testing.T) {
	m := NewManager()
	ctx := context.Background()
	waited, err := m.Acquire(ctx, 1, testKey, RowExclusive, noLongWait)
	require.NoError(t, err)
	assert.False(t, waited)
	_, err = m.Acquire(ctx, 2, testKey, RowExclusive, noLongWait)
	require.NoError(t, err)
	// A session never conflicts with itself.
	_, err = m.Acquire(ctx, 1, testKey, AccessExclusive, Options{NoWait: true})
	require.Error(t, err, "session 2 holds RowExclusive")
	m.ReleaseAll(2)
	_, err = m.Acquire(ctx, 1, testKey, AccessExclusive, Options{NoWait: true})
	require.NoError(t, err)
	assert.True(t, m.Holds(1, testKey, AccessExclusive))
	assert.True(t, m.Holds(1, testKey, RowExclusive))
	m.ReleaseAll(1)
	assert.False(t, m.HasLocks(1))
	assert.Empty(t, m.Locks())
	assert.Empty(t, m.objects)
	assert.Empty(t, m.sessions)
}

func TestNoWait(t *testing.T) {
	m := NewManager()
	ctx := context.Background()
	_, err := m.Acquire(ctx, 1, testKey, Exclusive, noLongWait)
	require.NoError(t, err)
	_, err = m.Acquire(ctx, 2, testKey, RowShare, Options{NoWait: true})
	require.ErrorContains(t, err, `could not obtain lock on relation "t"`)
	// Reads are still allowed under EXCLUSIVE.
	_, err = m.Acquire(ctx, 2, testKey, AccessShare, Options{NoWait: true})
	require.NoError(t, err)
}

func TestWaitUntilReleased(t *testing.T) {
	m := NewManager()
	ctx := context.Background()
	_, err := m.Acquire(ctx, 1, testKey, Exclusive, noLongWait)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		waited, err := m.Acquire(ctx, 2, testKey, Exclusive, noLongWait)
		if err == nil && !waited {
			err = assert.AnError
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("lock was granted while a conflicting lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	locks := m.Locks()
	require.Len(t, locks, 2)
	assert.False(t, locks[1].Granted)

	m.ReleaseAll(1)
	require.NoError(t, <-done)
	assert.True(t, m.Holds(2, testKey, Exclusive))
}

func TestWaitersAreNotStarved(t *testing.T) {
	m := NewManager()
	ctx := context.Background()
	_, err := m.Acquire(ctx, 1, testKey, AccessShare, noLongWait)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := m.Acquire(ctx, 2, testKey, AccessExclusive, noLongWait)
		done <- err
	}()
	require.Eventually(t, func() bool { return len(m.Locks()) == 2 }, time.Second, time.Millisecond)

	// A new reader queues behind the waiting ACCESS EXCLUSIVE request instead of overtaking it.
	_, err = m.Acquire(ctx, 3, testKey, AccessShare, Options{NoWait: true})
	require.Error(t, err)
	// The existing reader may still acquire more locks on the table, as it already holds one.
	_, err = m.Acquire(ctx, 1, testKey, AccessShare, Options{NoWait: true})
	require.NoError(t, err)

	m.ReleaseAll(1)
	require.NoError(t, <-done)
}

func TestDeadlockDetection(t *testing.T) {
	m := NewManager()
	ctx := context.Background()
	keyA := Key{Database: "postgres/main", Schema: "public", Table: "a"}
	keyB := Key{Database: "postgres/main", Schema: "public", Table: "b"}
	opts := Options{DeadlockTimeout: 50 * time.Millisecond, LockTimeout: 5 * time.Second}
	_, err := m.Acquire(ctx, 1, keyA, AccessExclusive, opts)
	require.NoError(t, err)
	_, err = m.Acquire(ctx, 2, keyB, AccessExclusive, opts)
	require.NoError(t, err)

	results := make(chan error, 2)
	go func() {
		_, err := m.Acquire(ctx, 1, keyB, AccessExclusive, opts)
		if err != nil {
			m.ReleaseAll(1)
		}
		results <- err
	}()
	go func() {
		_, err := m.Acquire(ctx, 2, keyA, AccessExclusive, opts)
		if err != nil {
			m.ReleaseAll(2)
		}
		results <- err
	}()
	errs := []error{<-results, <-results}
	failed := 0
	for _, err := range errs {
		if err != nil {
			assert.ErrorContains(t, err, "deadlock detected")
			failed++
		}
	}
	assert.Equal(t, 1, failed, "exactly one session must be chosen as the deadlock victim")
}

func TestLockTimeoutAndCancel(t *testing.T) {
	m := NewManager()
	_, err := m.Acquire(context.Background(), 1, testKey, AccessExclusive, noLongWait)
	require.NoError(t, err)

	_, err = m.Acquire(context.Background(), 2, testKey, AccessShare, Options{LockTimeout: 20 * time.Millisecond})
	require.ErrorContains(t, err, "canceling statement due to lock timeout")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = m.Acquire(ctx, 3, testKey, AccessShare, Options{})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// The abandoned requests must not linger in the queue.
	require.Len(t, m.Locks(), 1)
	m.ReleaseAll(1)
	assert.Empty(t, m.objects)
	assert.Empty(t, m.sessions)
}
