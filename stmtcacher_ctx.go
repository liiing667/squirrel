// +build go1.8

package squirrel

import (
	"context"
	"database/sql"
)

// PrepareerContext is the interface that wraps the Prepare and PrepareContext methods.
//
// Prepare executes the given query as implemented by database/sql.Prepare.
// PrepareContext executes the given query as implemented by database/sql.PrepareContext.
type PreparerContext interface {
	Preparer
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// DBProxyContext groups the Execer, Queryer, QueryRower and PreparerContext interfaces.
type DBProxyContext interface {
	Execer
	Queryer
	QueryRower
	PreparerContext
}

// NewStmtCache returns a *StmtCache wrapping a PreparerContext that caches Prepared Stmts.
//
// Stmts are cached based on the string value of their queries.
func NewStmtCache(prep PreparerContext) *StmtCache {
	return &StmtCache{
		prep:     prep,
		cache:    make(map[string]*sql.Stmt),
		entries:  make(map[string]CacheEntry),
		inflight: make(map[string]*inflight),
	}
}

// NewStmtCacher is deprecated
//
// Use NewStmtCache instead
func NewStmtCacher(prep PreparerContext) DBProxyContext {
	return NewStmtCache(prep)
}

// PrepareContext delegates down to the underlying PreparerContext and caches the result
// using the provided query as a key
func (sc *StmtCache) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	ctxPrep, ok := sc.prep.(PreparerContext)
	if !ok {
		return nil, NoContextSupport
	}

	// A live cached statement stays valid regardless of the caller's context;
	// cancellation only prevents starting a new prepare.
	sc.mu.Lock()
	if stmt, cached := sc.cache[query]; cached {
		sc.mu.Unlock()
		return stmt, nil
	}
	sc.mu.Unlock()

	if err := ctx.Err(); err != nil {
		sc.recordOutcome(query, stateForErr(err), err)
		return nil, err
	}

	wait, run := sc.beginPrepare(query, func() (*sql.Stmt, error) {
		return ctxPrep.PrepareContext(ctx, query)
	})
	if run != nil {
		return run()
	}
	return sc.awaitOutcome(query, wait)
}

// recordOutcome stores a non-cached prepare outcome (e.g. an already-canceled
// context) so callers can observe it without inserting a statement.
func (sc *StmtCache) recordOutcome(query string, state CacheState, err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if _, cached := sc.cache[query]; cached {
		return
	}
	sc.entries[query] = CacheEntry{
		Query:      query,
		State:      state,
		Generation: sc.generation,
		Err:        err,
	}
}

// ExecContext delegates down to the underlying PreparerContext using a prepared statement
func (sc *StmtCache) ExecContext(ctx context.Context, query string, args ...interface{}) (res sql.Result, err error) {
	stmt, err := sc.PrepareContext(ctx, query)
	if err != nil {
		return
	}
	return stmt.ExecContext(ctx, args...)
}

// QueryContext delegates down to the underlying PreparerContext using a prepared statement
func (sc *StmtCache) QueryContext(ctx context.Context, query string, args ...interface{}) (rows *sql.Rows, err error) {
	stmt, err := sc.PrepareContext(ctx, query)
	if err != nil {
		return
	}
	return stmt.QueryContext(ctx, args...)
}

// QueryRowContext delegates down to the underlying PreparerContext using a prepared statement
func (sc *StmtCache) QueryRowContext(ctx context.Context, query string, args ...interface{}) RowScanner {
	stmt, err := sc.PrepareContext(ctx, query)
	if err != nil {
		return &Row{err: err}
	}
	return stmt.QueryRowContext(ctx, args...)
}
