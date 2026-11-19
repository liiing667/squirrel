package squirrel

import (
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

// StmtStatus describes the observable state of a cached (or attempted) query.
type StmtStatus int

const (
	// StmtStatusUnknown means the query has never been seen by the cache.
	StmtStatusUnknown StmtStatus = iota
	// StmtStatusPrepared means a statement for the query was prepared and cached.
	StmtStatusPrepared
	// StmtStatusCacheHit means the query was served from the current generation's cache.
	StmtStatusCacheHit
	// StmtStatusFailed means the last prepare attempt returned an error; nothing is cached.
	StmtStatusFailed
	// StmtStatusCanceled means the last prepare attempt was canceled by its context.
	StmtStatusCanceled
)

// String returns a human readable representation of the status.
func (s StmtStatus) String() string {
	switch s {
	case StmtStatusPrepared:
		return "prepared"
	case StmtStatusCacheHit:
		return "cache_hit"
	case StmtStatusFailed:
		return "failed"
	case StmtStatusCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// StmtState is an observable snapshot of what happened to a single query inside
// a StmtCache.
type StmtState struct {
	// Query is the SQL string the state belongs to.
	Query string
	// Status is the outcome of the most recent prepare attempt.
	Status StmtStatus
	// Generation is the cache generation the state belongs to. Every Clear bumps
	// the generation, so callers can tell stale and current entries apart.
	Generation uint64
	// Err holds the prepare error when Status is StmtStatusFailed or
	// StmtStatusCanceled.
	Err error
}

type inflightPrepare struct {
	once sync.Once
	done chan struct{}
	stmt *sql.Stmt
	err  error
	// waiters is the number of concurrent callers parked on done. Guarded by
	// StmtCache.mu; it exists to make concurrent behavior testable.
	waiters int
}

func (c *inflightPrepare) finish(stmt *sql.Stmt, err error) {
	c.once.Do(func() {
		c.stmt = stmt
		c.err = err
		close(c.done)
	})
}

// StmtCache wraps and delegates down to a Preparer type
//
// It also automatically prepares all statements sent to the underlying Preparer calls
// for Exec, Query and QueryRow and caches the returns *sql.Stmt using the provided
// query as the key. So that it can be automatically re-used.
type StmtCache struct {
	prep Preparer
	mu   sync.Mutex
	// generation is bumped on every Clear; entries and states carry it so callers
	// can observe context boundaries.
	generation uint64
	// cache holds statements of the current generation. Failed and canceled
	// prepares are never inserted, so the map only contains usable entries.
	cache map[string]*sql.Stmt
	// inflight deduplicates concurrent prepares of the same query.
	inflight map[string]*inflightPrepare
	// states keeps the observable outcome of every query ever attempted without
	// polluting the statement cache.
	states map[string]StmtState
}

// prepare runs the actual prepare call (via prepareFn) while coordinating the
// cache, generations, in-flight deduplication and observable state.
func (sc *StmtCache) prepare(query string, prepareFn func() (*sql.Stmt, error), canceled func(error) bool) (*sql.Stmt, StmtState, error) {
	sc.mu.Lock()

	if stmt, ok := sc.cache[query]; ok {
		state := StmtState{
			Query:      query,
			Status:     StmtStatusCacheHit,
			Generation: sc.generation,
		}
		sc.states[query] = state
		sc.mu.Unlock()
		return stmt, state, nil
	}

	if call, ok := sc.inflight[query]; ok {
		call.waiters++
		sc.mu.Unlock()
		<-call.done

		sc.mu.Lock()
		defer sc.mu.Unlock()
		call.waiters--
		if stmt, ok := sc.cache[query]; ok {
			state := StmtState{
				Query:      query,
				Status:     StmtStatusCacheHit,
				Generation: sc.generation,
			}
			sc.states[query] = state
			return stmt, state, nil
		}
		state := sc.states[query]
		return nil, state, call.err
	}

	call := &inflightPrepare{done: make(chan struct{})}
	sc.inflight[query] = call
	prepareGeneration := sc.generation
	sc.mu.Unlock()

	stmt, err := prepareFn()

	sc.mu.Lock()
	defer sc.mu.Unlock()

	delete(sc.inflight, query)

	if err != nil {
		status := StmtStatusFailed
		if canceled != nil && canceled(err) {
			status = StmtStatusCanceled
		}
		state := StmtState{
			Query:      query,
			Status:     status,
			Generation: prepareGeneration,
			Err:        err,
		}
		sc.states[query] = state
		call.finish(nil, err)
		return nil, state, err
	}

	state := StmtState{
		Query:      query,
		Status:     StmtStatusPrepared,
		Generation: prepareGeneration,
	}

	// Only publish when the statement still belongs to the current generation.
	// A Clear that happened while we were preparing the query must not be
	// silently overwritten with a stale statement.
	if sc.generation == prepareGeneration {
		if _, exists := sc.cache[query]; !exists {
			sc.cache[query] = stmt
		}
		sc.states[query] = state
	} else {
		if current, ok := sc.cache[query]; ok {
			// Another prepare already published a current-generation statement.
			state = StmtState{
				Query:      query,
				Status:     StmtStatusCacheHit,
				Generation: sc.generation,
			}
			sc.states[query] = state
			stmt = current
		} else {
			// The cache was cleared while we were preparing. The orphaned
			// statement is invalid for the caller; record the boundary and
			// close it so it does not leak.
			err = fmt.Errorf("squirrel: statement cache advanced to generation %d while preparing %q", sc.generation, query)
			state = StmtState{
				Query:      query,
				Status:     StmtStatusFailed,
				Generation: sc.generation,
				Err:        err,
			}
			sc.states[query] = state
			if stmt != nil {
				_ = stmt.Close()
			}
			call.finish(nil, err)
			return nil, state, err
		}
	}

	call.finish(stmt, nil)
	return stmt, state, nil
}

// Prepare delegates down to the underlying Preparer and caches the result
// using the provided query as a key.
func (sc *StmtCache) Prepare(query string) (*sql.Stmt, error) {
	stmt, _, err := sc.prepare(query, func() (*sql.Stmt, error) {
		return sc.prep.Prepare(query)
	}, nil)
	return stmt, err
}

// PrepareState delegates like Prepare but additionally returns the observable
// StmtState describing the generation and outcome of the call.
func (sc *StmtCache) PrepareState(query string) (*sql.Stmt, StmtState, error) {
	return sc.prepare(query, func() (*sql.Stmt, error) {
		return sc.prep.Prepare(query)
	}, nil)
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

// Clear removes and closes all the currently cached prepared statements and
// bumps the cache generation. In-flight prepares that finish after Clear do
// not repopulate the cache with stale statements.
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
		delete(sc.inflight, query)
		clearErr := fmt.Errorf("squirrel: statement cache cleared (generation %d) while preparing %q", generation, query)
		call.finish(nil, clearErr)
		sc.states[query] = StmtState{
			Query:      query,
			Status:     StmtStatusFailed,
			Generation: generation,
			Err:        clearErr,
		}
	}
	sc.mu.Unlock()

	if err != nil {
		return fmt.Errorf("one or more Stmt.Close failed; last error: %v", err)
	}

	return
}

// Generation returns the current cache generation. It starts at 0 and is
// incremented by every Clear.
func (sc *StmtCache) Generation() uint64 {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.generation
}

// State returns the observable state most recently recorded for query, along
// with ok=false when the query has never been attempted.
func (sc *StmtCache) State(query string) (StmtState, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	state, ok := sc.states[query]
	return state, ok
}

// States returns observable state snapshots for every query the cache has seen.
func (sc *StmtCache) States() []StmtState {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	snapshots := make([]StmtState, 0, len(sc.states))
	for _, state := range sc.states {
		snapshots = append(snapshots, state)
	}
	return snapshots
}

// inflightWaiters reports how many callers are currently parked waiting for an
// in-flight prepare of query to finish. It is intended for tests that need a
// deterministic concurrent ordering.
func (sc *StmtCache) inflightWaiters(query string) int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if call, ok := sc.inflight[query]; ok {
		return call.waiters
	}
	return 0
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
