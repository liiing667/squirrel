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
		inflight: make(map[string]*inflightPrepare),
		states:   make(map[string]StmtState),
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
	stmt, _, err := sc.PrepareContextState(ctx, query)
	return stmt, err
}

// PrepareContextState delegates like PrepareContext but additionally returns
// the observable StmtState describing the generation and outcome of the call.
//
// A prepare whose context is already canceled is reported with
// StmtStatusCanceled and never populates the cache.
func (sc *StmtCache) PrepareContextState(ctx context.Context, query string) (*sql.Stmt, StmtState, error) {
	ctxPrep, ok := sc.prep.(PreparerContext)
	if !ok {
		state := StmtState{
			Query:      query,
			Status:     StmtStatusFailed,
			Generation: sc.Generation(),
			Err:        NoContextSupport,
		}
		sc.mu.Lock()
		sc.states[query] = state
		sc.mu.Unlock()
		return nil, state, NoContextSupport
	}
	return sc.prepare(query, func() (*sql.Stmt, error) {
		return ctxPrep.PrepareContext(ctx, query)
	}, func(err error) bool {
		return ctx.Err() == context.Canceled || ctx.Err() == context.DeadlineExceeded
	})
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
