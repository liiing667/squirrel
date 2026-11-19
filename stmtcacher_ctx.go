//go:build go1.8
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
	return NewStmtCacheWithOptions(prep)
}

// NewStmtCacher is deprecated
//
// Use NewStmtCache instead
func NewStmtCacher(prep PreparerContext) DBProxyContext {
	return NewStmtCache(prep)
}

// StmtCacheOption configures a StmtCache created by NewStmtCacheWithOptions.
type StmtCacheOption func(*StmtCache)

// WithMaxEntries limits the number of prepared statements held by the cache.
// A non-positive value means the number of entries is not limited.
func WithMaxEntries(maxEntries int) StmtCacheOption {
	return func(sc *StmtCache) {
		sc.maxEntries = maxEntries
	}
}

// WithMaxSQLBytes limits the combined size, in bytes, of the SQL strings held
// by the cache. A non-positive value means the SQL byte usage is not limited.
func WithMaxSQLBytes(maxSQLBytes int) StmtCacheOption {
	return func(sc *StmtCache) {
		sc.maxSQLBytes = maxSQLBytes
	}
}

// NewStmtCacheWithOptions returns a *StmtCache wrapping prep that caches
// Prepared Stmts and enforces the limits supplied via StmtCacheOptions.
//
// When the entry count or SQL byte budget is exhausted, Prepare and
// PrepareContext return an error wrapping ErrCacheLimit before the underlying
// Preparer is touched; already cached queries keep working as cache hits.
// Clear empties the cache and restores the full budgets.
func NewStmtCacheWithOptions(prep PreparerContext, opts ...StmtCacheOption) *StmtCache {
	sc := &StmtCache{prep: prep, cache: make(map[string]*sql.Stmt)}
	for _, opt := range opts {
		opt(sc)
	}
	return sc
}

// PrepareContext delegates down to the underlying PreparerContext and caches the result
// using the provided query as a key
func (sc *StmtCache) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	ctxPrep, ok := sc.prep.(PreparerContext)
	if !ok {
		return nil, NoContextSupport
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.prepareLocked(query, func() (*sql.Stmt, error) {
		return ctxPrep.PrepareContext(ctx, query)
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
