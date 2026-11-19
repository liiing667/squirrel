package squirrel

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Prepareer is the interface that wraps the Prepare method.
//
// Prepare executes the given query as implemented by database/sql.Prepare.
type Preparer interface {
	Prepare(query string) (*sql.Stmt, error)
}

// DBProxy groups the Execer, Queryer, QueryRower, and Preparer interfaces.
type DBProxy interface {
	Execer
	Queryer
	QueryRower
	Preparer
}

// NOTE: NewStmtCache is defined in stmtcacher_ctx.go (Go >= 1.8) or stmtcacher_noctx.go (Go < 1.8).

// CacheState describes the observable lifecycle state of a single query within
// a StmtCache.
type CacheState int

const (
	// StateUnknown is returned for a query that the cache has never prepared.
	StateUnknown CacheState = iota
	// StatePreparing means a prepare for the query is currently in flight.
	StatePreparing
	// StatePrepared means the query has a live, reusable prepared statement.
	StatePrepared
	// StateCanceled means the last prepare was abandoned because its context
	// was canceled or reached its deadline. Nothing is cached for the query,
	// so a later call prepares it again.
	StateCanceled
	// StateFailed means the last prepare returned an ordinary error. Nothing is
	// cached for the query, so a later call prepares it again.
	StateFailed
)

// String returns a stable, human readable name for the CacheState.
func (s CacheState) String() string {
	switch s {
	case StatePreparing:
		return "preparing"
	case StatePrepared:
		return "prepared"
	case StateCanceled:
		return "canceled"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// CacheEntry is an immutable observation of a single query in a StmtCache.
type CacheEntry struct {
	// Query is the SQL string the entry is tracked under.
	Query string
	// State is the current lifecycle state of the query.
	State CacheState
	// Generation is the cache generation in which the entry was last touched.
	// Generations are bumped by Clear.
	Generation uint64
	// Err holds the cancellation or prepare error for StateCanceled/StateFailed.
	Err error
}

// CacheSnapshot is a point-in-time, concurrency-safe view of a StmtCache.
type CacheSnapshot struct {
	// Generation is the cache generation at the time the snapshot was taken.
	Generation uint64
	// Prepared is the number of reusable statements currently held.
	Prepared int
	// Preparing is the number of prepares currently in flight.
	Preparing int
	// Canceled is the number of queries whose last prepare was canceled.
	Canceled int
	// Failed is the number of queries whose last prepare failed.
	Failed int
	// Entries holds the tracked state of every query seen by the cache.
	Entries []CacheEntry
}

// inflight allows concurrent callers preparing the same query to share a
// single underlying prepare instead of each racing to insert a statement.
type inflight struct {
	generation uint64
	ready      chan struct{}
	stmt       *sql.Stmt
	err        error
}

// StmtCache wraps and delegates down to a Preparer type
//
// It also automatically prepares all statements sent to the underlying Preparer calls
// for Exec, Query and QueryRow and caches the returns *sql.Stmt using the provided
// query as the key. So that it can be automatically re-used.
type StmtCache struct {
	prep       Preparer
	cache      map[string]*sql.Stmt
	generation uint64
	entries    map[string]CacheEntry
	inflight   map[string]*inflight
	mu         sync.Mutex
}

// Generation returns the current cache generation. Every successful Clear bumps
// the generation, letting callers tell whether cached statements predate a
// context boundary they consider invalid.
func (sc *StmtCache) Generation() uint64 {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.generation
}

// State returns the observable state currently recorded for query.
func (sc *StmtCache) State(query string) CacheState {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if entry, ok := sc.entries[query]; ok {
		return entry.State
	}
	return StateUnknown
}

// Entry returns a point-in-time observation of query, or ok == false if the
// query has never been prepared.
func (sc *StmtCache) Entry(query string) (entry CacheEntry, ok bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	entry, ok = sc.entries[query]
	return
}

// Snapshot returns a concurrency-safe, point-in-time view of the cache.
func (sc *StmtCache) Snapshot() CacheSnapshot {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	snap := CacheSnapshot{
		Generation: sc.generation,
		Entries:    make([]CacheEntry, 0, len(sc.entries)),
	}
	for _, entry := range sc.entries {
		snap.Entries = append(snap.Entries, entry)
		switch entry.State {
		case StatePrepared:
			snap.Prepared++
		case StatePreparing:
			snap.Preparing++
		case StateCanceled:
			snap.Canceled++
		case StateFailed:
			snap.Failed++
		}
	}
	return snap
}

// beginPrepare joins an in-flight prepare for query or starts a new one. The
// doPrepare callback performs the actual (context-aware or plain) prepare and
// must be invoked by exactly one goroutine; concurrent callers block on the
// returned channel instead.
func (sc *StmtCache) beginPrepare(query string, doPrepare func() (*sql.Stmt, error)) (wait <-chan struct{}, run func() (*sql.Stmt, error)) {
	sc.mu.Lock()

	if stmt, ok := sc.cache[query]; ok {
		sc.mu.Unlock()
		return nil, func() (*sql.Stmt, error) { return stmt, nil }
	}

	if call, ok := sc.inflight[query]; ok {
		sc.mu.Unlock()
		return call.ready, nil
	}

	call := &inflight{generation: sc.generation, ready: make(chan struct{})}
	sc.inflight[query] = call
	sc.entries[query] = CacheEntry{
		Query:      query,
		State:      StatePreparing,
		Generation: sc.generation,
	}
	sc.mu.Unlock()

	return nil, func() (*sql.Stmt, error) {
		stmt, err := doPrepare()
		sc.finishPrepare(query, call, stmt, err)
		return stmt, err
	}
}

// awaitOutcome blocks until an in-flight prepare finishes and returns its
// cached statement or the recorded error.
func (sc *StmtCache) awaitOutcome(query string, wait <-chan struct{}) (*sql.Stmt, error) {
	<-wait
	sc.mu.Lock()
	entry := sc.entries[query]
	if entry.State == StatePrepared {
		stmt := sc.cache[query]
		sc.mu.Unlock()
		return stmt, nil
	}
	sc.mu.Unlock()
	return nil, entry.Err
}

// finishPrepare records the outcome of a prepare started via beginPrepare.
// Statements that finish after a Clear belong to a stale generation and are
// closed instead of being cached, so callers never observe an invalidated
// statement through the map.
func (sc *StmtCache) finishPrepare(query string, call *inflight, stmt *sql.Stmt, err error) {
	sc.mu.Lock()

	stale := call.generation != sc.generation
	delete(sc.inflight, query)

	switch {
	case err != nil:
		sc.entries[query] = CacheEntry{
			Query:      query,
			State:      stateForErr(err),
			Generation: sc.generation,
			Err:        err,
		}
	case stale:
		if stmt != nil {
			stmt.Close()
		}
		sc.entries[query] = CacheEntry{
			Query:      query,
			State:      StateCanceled,
			Generation: sc.generation,
			Err:        context.Canceled,
		}
	default:
		sc.cache[query] = stmt
		sc.entries[query] = CacheEntry{
			Query:      query,
			State:      StatePrepared,
			Generation: sc.generation,
		}
	}

	call.stmt = stmt
	call.err = err
	close(call.ready)
	sc.mu.Unlock()
}

// Prepare delegates down to the underlying Preparer and caches the result
// using the provided query as a key. Concurrent calls for the same query share
// a single prepare; canceled or failed prepares are never cached.
func (sc *StmtCache) Prepare(query string) (*sql.Stmt, error) {
	wait, run := sc.beginPrepare(query, func() (*sql.Stmt, error) {
		return sc.prep.Prepare(query)
	})
	if run != nil {
		return run()
	}
	return sc.awaitOutcome(query, wait)
}

// stateForErr classifies a prepare error so canceled contexts are observable
// separately from ordinary prepare failures.
func stateForErr(err error) CacheState {
	if contextCanceled(err) != nil {
		return StateCanceled
	}
	return StateFailed
}

// contextCanceled returns err if err is a context cancellation or deadline
// error, nil otherwise. It is context-aware without requiring errors.Is, which
// keeps the package buildable on the older Go versions it supports.
func contextCanceled(err error) error {
	if err == nil {
		return nil
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	type timeoutErr interface {
		Timeout() bool
	}
	if te, ok := err.(timeoutErr); ok && te.Timeout() {
		return err
	}
	return nil
}

// Exec delegates down to the underlying Preparer using a prepared statement
func (sc *StmtCache) Exec(query string, args ...interface{}) (res sql.Result, err error) {
	stmt, err := sc.Prepare(query)
	if err != nil {
		return
	}
	return stmt.Exec(args...)
}

// Query delegates down to the underlying Preparer using a prepared statement
func (sc *StmtCache) Query(query string, args ...interface{}) (rows *sql.Rows, err error) {
	stmt, err := sc.Prepare(query)
	if err != nil {
		return
	}
	return stmt.Query(args...)
}

// QueryRow delegates down to the underlying Preparer using a prepared statement
func (sc *StmtCache) QueryRow(query string, args ...interface{}) RowScanner {
	stmt, err := sc.Prepare(query)
	if err != nil {
		return &Row{err: err}
	}
	return stmt.QueryRow(args...)
}

// Clear removes and closes all the currently cached prepared statements
//
// Clear also bumps the cache generation, marking every statement prepared in
// earlier generations as invalidated. Prepares still in flight when Clear runs
// are abandoned to their original caller and their statements are closed
// instead of being cached, so a cleared cache never hands back a closed Stmt.
func (sc *StmtCache) Clear() (err error) {
	sc.mu.Lock()
	sc.generation++
	generation := sc.generation

	for key, stmt := range sc.cache {
		delete(sc.cache, key)

		if stmt == nil {
			continue
		}

		if cerr := stmt.Close(); cerr != nil {
			err = cerr
		}
	}

	for query, call := range sc.inflight {
		// Do not delete the in-flight entry: the preparing goroutine still
		// owns it and records the outcome in finishPrepare. Marking the call
		// stale makes that outcome uncacheable and its Stmt gets closed.
		call.generation = 0
		sc.entries[query] = CacheEntry{
			Query:      query,
			State:      StatePreparing,
			Generation: generation,
			Err:        context.Canceled,
		}
	}

	sc.mu.Unlock()

	if err != nil {
		return fmt.Errorf("one or more Stmt.Close failed; last error: %v", err)
	}

	return
}

type DBProxyBeginner interface {
	DBProxy
	Begin() (*sql.Tx, error)
}

type stmtCacheProxy struct {
	DBProxy
	db *sql.DB
}

func NewStmtCacheProxy(db *sql.DB) DBProxyBeginner {
	return &stmtCacheProxy{DBProxy: NewStmtCache(db), db: db}
}

func (sp *stmtCacheProxy) Begin() (*sql.Tx, error) {
	return sp.db.Begin()
}
