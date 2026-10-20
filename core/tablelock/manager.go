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

// Package tablelock implements PostgreSQL's table-level lock modes. Locks are owned by a session, are held until the
// owning transaction ends, and conflict with each other according to PostgreSQL's conflict table:
// https://www.postgresql.org/docs/current/explicit-locking.html#LOCKING-TABLES
package tablelock

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/dolthub/doltgresql/postgres/parser/pgcode"
	"github.com/dolthub/doltgresql/postgres/parser/pgerror"
)

// Mode is a PostgreSQL table-level lock mode. Modes are ordered from weakest to strongest.
type Mode uint8

const (
	AccessShare Mode = iota
	RowShare
	RowExclusive
	ShareUpdateExclusive
	Share
	ShareRowExclusive
	Exclusive
	AccessExclusive

	modeCount = int(AccessExclusive) + 1
)

var modeNames = [modeCount]string{
	AccessShare:          "AccessShareLock",
	RowShare:             "RowShareLock",
	RowExclusive:         "RowExclusiveLock",
	ShareUpdateExclusive: "ShareUpdateExclusiveLock",
	Share:                "ShareLock",
	ShareRowExclusive:    "ShareRowExclusiveLock",
	Exclusive:            "ExclusiveLock",
	AccessExclusive:      "AccessExclusiveLock",
}

// String returns the name that PostgreSQL uses for the mode, such as in pg_locks.
func (m Mode) String() string {
	return modeNames[m]
}

// conflictTable holds, for each mode, the set of modes that it conflicts with as a bitmask.
var conflictTable = [modeCount]uint16{
	AccessShare:          bits(AccessExclusive),
	RowShare:             bits(Exclusive, AccessExclusive),
	RowExclusive:         bits(Share, ShareRowExclusive, Exclusive, AccessExclusive),
	ShareUpdateExclusive: bits(ShareUpdateExclusive, Share, ShareRowExclusive, Exclusive, AccessExclusive),
	Share:                bits(RowExclusive, ShareUpdateExclusive, ShareRowExclusive, Exclusive, AccessExclusive),
	ShareRowExclusive:    bits(RowExclusive, ShareUpdateExclusive, Share, ShareRowExclusive, Exclusive, AccessExclusive),
	Exclusive:            bits(RowShare, RowExclusive, ShareUpdateExclusive, Share, ShareRowExclusive, Exclusive, AccessExclusive),
	AccessExclusive:      bits(AccessShare, RowShare, RowExclusive, ShareUpdateExclusive, Share, ShareRowExclusive, Exclusive, AccessExclusive),
}

func bits(modes ...Mode) uint16 {
	var b uint16
	for _, m := range modes {
		b |= 1 << m
	}
	return b
}

// Conflicts returns whether the two modes conflict with each other.
func Conflicts(a Mode, b Mode) bool {
	return conflictTable[a]&(1<<b) != 0
}

// Key identifies a lockable table.
type Key struct {
	// Database is the revision-qualified database name, such as "postgres/main".
	Database string
	Schema   string
	Table    string
}

// String returns the name used in error messages.
func (k Key) String() string {
	return k.Table
}

// Less returns whether the key sorts before the other key.
func (k Key) Less(other Key) bool {
	if k.Database != other.Database {
		return k.Database < other.Database
	}
	if k.Schema != other.Schema {
		return k.Schema < other.Schema
	}
	return k.Table < other.Table
}

// Options control how a lock request waits.
type Options struct {
	// NoWait returns an error immediately instead of waiting for a conflicting lock to be released.
	NoWait bool
	// LockTimeout aborts the wait after the given duration. Zero waits indefinitely.
	LockTimeout time.Duration
	// DeadlockTimeout is how long to wait before checking for a deadlock. Zero uses one second.
	DeadlockTimeout time.Duration
}

// Manager grants and releases table-level locks.
type Manager struct {
	mu       sync.Mutex
	objects  map[Key]*object
	sessions map[uint32]*sessionState
}

// object is the lock state of a single table.
type object struct {
	granted map[uint32]*[modeCount]int
	queue   []*waiter
}

// sessionState tracks the locks of a single session.
type sessionState struct {
	held    map[Key]struct{}
	waiting *waiter
}

// waiter is a lock request that could not be granted immediately.
type waiter struct {
	session uint32
	key     Key
	mode    Mode
	ready   chan struct{}
	granted bool
}

// Global is the manager used by the server.
var Global = NewManager()

// NewManager returns a new, empty *Manager.
func NewManager() *Manager {
	return &Manager{
		objects:  make(map[Key]*object),
		sessions: make(map[uint32]*sessionState),
	}
}

// Acquire obtains the lock in the given mode on behalf of the session, waiting as long as necessary unless the options
// state otherwise. Locks are reentrant and every successful acquisition is held until ReleaseAll is called for the
// session. Returns whether the call had to wait for another session.
func (m *Manager) Acquire(ctx context.Context, session uint32, key Key, mode Mode, opts Options) (waited bool, err error) {
	m.mu.Lock()
	obj := m.object(key)
	if m.grantable(obj, session, mode, true) {
		m.grant(obj, session, key, mode)
		m.mu.Unlock()
		return false, nil
	}
	if opts.NoWait {
		m.cleanup(key, obj)
		m.mu.Unlock()
		return false, pgerror.Newf(pgcode.LockNotAvailable, `could not obtain lock on relation "%s"`, key)
	}
	w := &waiter{session: session, key: key, mode: mode, ready: make(chan struct{})}
	obj.queue = append(obj.queue, w)
	m.session(session).waiting = w
	m.mu.Unlock()

	deadlockTimeout := opts.DeadlockTimeout
	if deadlockTimeout <= 0 {
		deadlockTimeout = time.Second
	}
	deadlockTimer := time.NewTimer(deadlockTimeout)
	defer deadlockTimer.Stop()
	var lockTimeout <-chan time.Time
	if opts.LockTimeout > 0 {
		lockTimer := time.NewTimer(opts.LockTimeout)
		defer lockTimer.Stop()
		lockTimeout = lockTimer.C
	}

	for {
		select {
		case <-w.ready:
			return true, nil
		case <-deadlockTimer.C:
			if m.abandonIfDeadlocked(w) {
				return true, pgerror.Newf(pgcode.DeadlockDetected, "deadlock detected")
			}
		case <-lockTimeout:
			if m.abandon(w) {
				return true, pgerror.Newf(pgcode.LockNotAvailable, "canceling statement due to lock timeout")
			}
			return true, nil
		case <-ctx.Done():
			if m.abandon(w) {
				return true, ctx.Err()
			}
			return true, nil
		}
	}
}

// ReleaseAll releases every lock held by the session.
func (m *Manager) ReleaseAll(session uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.sessions[session]
	if !ok {
		return
	}
	for key := range state.held {
		obj, ok := m.objects[key]
		if !ok {
			continue
		}
		delete(obj.granted, session)
		m.wake(obj)
		m.cleanup(key, obj)
	}
	state.held = nil
	if state.waiting == nil {
		delete(m.sessions, session)
	}
}

// Lock describes a granted or awaited lock.
type Lock struct {
	Key     Key
	Session uint32
	Mode    Mode
	Granted bool
}

// Locks returns a snapshot of all granted and awaited locks.
func (m *Manager) Locks() []Lock {
	m.mu.Lock()
	defer m.mu.Unlock()
	var locks []Lock
	for key, obj := range m.objects {
		for session, counts := range obj.granted {
			for mode, count := range counts {
				if count > 0 {
					locks = append(locks, Lock{Key: key, Session: session, Mode: Mode(mode), Granted: true})
				}
			}
		}
		for _, w := range obj.queue {
			locks = append(locks, Lock{Key: key, Session: w.session, Mode: w.mode, Granted: false})
		}
	}
	sort.Slice(locks, func(i, j int) bool {
		a, b := locks[i], locks[j]
		if a.Key != b.Key {
			return a.Key.Less(b.Key)
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Mode < b.Mode
	})
	return locks
}

// HasLocks returns whether the session holds any lock.
func (m *Manager) HasLocks(session uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.sessions[session]
	return ok && len(state.held) > 0
}

// Holds returns whether the session holds the lock in the given mode.
func (m *Manager) Holds(session uint32, key Key, mode Mode) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return false
	}
	counts, ok := obj.granted[session]
	return ok && counts[mode] > 0
}

// object returns the lock state of the key, creating it if necessary. Must be called while holding the mutex.
func (m *Manager) object(key Key) *object {
	obj, ok := m.objects[key]
	if !ok {
		obj = &object{granted: make(map[uint32]*[modeCount]int)}
		m.objects[key] = obj
	}
	return obj
}

// session returns the state of the session, creating it if necessary. Must be called while holding the mutex.
func (m *Manager) session(session uint32) *sessionState {
	state, ok := m.sessions[session]
	if !ok {
		state = &sessionState{}
		m.sessions[session] = state
	}
	return state
}

// grantable returns whether the mode may be granted to the session right now. A request must not conflict with locks
// granted to other sessions. New requests also queue behind conflicting waiters so that those cannot be starved,
// unless the session already holds a lock on the table, as waiting would then risk a deadlock with those waiters.
func (m *Manager) grantable(obj *object, session uint32, mode Mode, isNewRequest bool) bool {
	for holder, counts := range obj.granted {
		if holder == session {
			continue
		}
		for held, count := range counts {
			if count > 0 && Conflicts(mode, Mode(held)) {
				return false
			}
		}
	}
	if isNewRequest {
		if _, holdsAny := obj.granted[session]; !holdsAny {
			for _, w := range obj.queue {
				if w.session != session && Conflicts(mode, w.mode) {
					return false
				}
			}
		}
	}
	return true
}

// grant records the lock for the session. Must be called while holding the mutex.
func (m *Manager) grant(obj *object, session uint32, key Key, mode Mode) {
	counts, ok := obj.granted[session]
	if !ok {
		counts = &[modeCount]int{}
		obj.granted[session] = counts
	}
	counts[mode]++
	state := m.session(session)
	if state.held == nil {
		state.held = make(map[Key]struct{})
	}
	state.held[key] = struct{}{}
}

// wake grants queued requests in order, as long as each is compatible with the granted locks and with the requests
// queued ahead of it. Must be called while holding the mutex.
func (m *Manager) wake(obj *object) {
	remaining := obj.queue[:0]
	var ahead []*waiter
	for _, w := range obj.queue {
		ok := m.grantable(obj, w.session, w.mode, false)
		if ok {
			for _, a := range ahead {
				if a.session != w.session && Conflicts(w.mode, a.mode) {
					ok = false
					break
				}
			}
		}
		if !ok {
			ahead = append(ahead, w)
			remaining = append(remaining, w)
			continue
		}
		m.grant(obj, w.session, w.key, w.mode)
		w.granted = true
		if state, ok := m.sessions[w.session]; ok && state.waiting == w {
			state.waiting = nil
		}
		close(w.ready)
	}
	for i := len(remaining); i < len(obj.queue); i++ {
		obj.queue[i] = nil
	}
	obj.queue = remaining
}

// abandon removes the waiter from its queue. Returns false if the lock was granted in the meantime, in which case the
// caller owns the lock. Must be called without holding the mutex.
func (m *Manager) abandon(w *waiter) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.abandonLocked(w)
}

func (m *Manager) abandonLocked(w *waiter) bool {
	if w.granted {
		return false
	}
	obj := m.objects[w.key]
	for i, queued := range obj.queue {
		if queued == w {
			obj.queue = append(obj.queue[:i], obj.queue[i+1:]...)
			break
		}
	}
	if state, ok := m.sessions[w.session]; ok {
		state.waiting = nil
		if len(state.held) == 0 {
			delete(m.sessions, w.session)
		}
	}
	// Requests that queued behind this one may now be grantable.
	m.wake(obj)
	m.cleanup(w.key, obj)
	return true
}

// abandonIfDeadlocked checks whether the waiter is part of a deadlock and, if so, removes it from its queue so that the
// other participants may proceed. Returns true if the waiter was removed.
func (m *Manager) abandonIfDeadlocked(w *waiter) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w.granted || !m.reaches(w, w.session, make(map[uint32]bool)) {
		return false
	}
	return m.abandonLocked(w)
}

// reaches returns whether the target session is (transitively) blocking the waiter. Must be called while holding the
// mutex.
func (m *Manager) reaches(w *waiter, target uint32, visited map[uint32]bool) bool {
	for _, blocker := range m.blockers(w) {
		if blocker == target {
			return true
		}
		if visited[blocker] {
			continue
		}
		visited[blocker] = true
		if state, ok := m.sessions[blocker]; ok && state.waiting != nil && m.reaches(state.waiting, target, visited) {
			return true
		}
	}
	return false
}

// blockers returns the sessions that the waiter is waiting on: those holding a conflicting lock, and those queued ahead
// of it with a conflicting request. Must be called while holding the mutex.
func (m *Manager) blockers(w *waiter) []uint32 {
	obj := m.objects[w.key]
	var sessions []uint32
	for holder, counts := range obj.granted {
		if holder == w.session {
			continue
		}
		for held, count := range counts {
			if count > 0 && Conflicts(w.mode, Mode(held)) {
				sessions = append(sessions, holder)
				break
			}
		}
	}
	for _, queued := range obj.queue {
		if queued == w {
			break
		}
		if queued.session != w.session && Conflicts(w.mode, queued.mode) {
			sessions = append(sessions, queued.session)
		}
	}
	return sessions
}

// cleanup removes the object if nothing references it anymore. Must be called while holding the mutex.
func (m *Manager) cleanup(key Key, obj *object) {
	if len(obj.granted) == 0 && len(obj.queue) == 0 {
		delete(m.objects, key)
	}
}
