package squirrel

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// ErrCacheLimit is returned when a StmtCache is full, i.e. adding another
// prepared statement would exceed its configured entry count or SQL byte
// budget. The underlying Preparer is never called when this error is returned.
var ErrCacheLimit = errors.New("statement cache limit reached")

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

// StmtCache wraps and delegates down to a Preparer type
//
// It also automatically prepares all statements sent to the underlying Preparer calls
// for Exec, Query and QueryRow and caches the returns *sql.Stmt using the provided
// query as the key. So that it can be automatically re-used.
type StmtCache struct {
	prep Preparer

	cache map[string]*sql.Stmt

	// maxEntries and maxSQLBytes bound the cache. A non-positive value means
	// no limit of that kind is enforced.
	maxEntries  int
	maxSQLBytes int
	// sqlBytes tracks len(query) summed over all cached entries.
	sqlBytes int

	mu sync.Mutex
}

// prepareLocked looks up query in the cache and, on a miss, enforces the
// configured limits before delegating to doPrepare. The caller must hold sc.mu.
func (sc *StmtCache) prepareLocked(query string, doPrepare func() (*sql.Stmt, error)) (*sql.Stmt, error) {
	if stmt, ok := sc.cache[query]; ok {
		return stmt, nil
	}

	if err := sc.checkLimitLocked(query); err != nil {
		return nil, err
	}

	stmt, err := doPrepare()
	if err == nil {
		sc.cache[query] = stmt
		sc.sqlBytes += len(query)
	}
	return stmt, err
}

// checkLimitLocked returns ErrCacheLimit when caching query would exceed the
// configured entry count or SQL byte budget. The caller must hold sc.mu.
func (sc *StmtCache) checkLimitLocked(query string) error {
	if sc.maxEntries > 0 && len(sc.cache) >= sc.maxEntries {
		return fmt.Errorf("%w: max entries %d reached", ErrCacheLimit, sc.maxEntries)
	}
	if sc.maxSQLBytes > 0 && sc.sqlBytes+len(query) > sc.maxSQLBytes {
		return fmt.Errorf("%w: SQL byte budget %d exceeded by query of %d bytes",
			ErrCacheLimit, sc.maxSQLBytes, len(query))
	}
	return nil
}

// Prepare delegates down to the underlying Preparer and caches the result
// using the provided query as a key
func (sc *StmtCache) Prepare(query string) (*sql.Stmt, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	return sc.prepareLocked(query, func() (*sql.Stmt, error) {
		return sc.prep.Prepare(query)
	})
}

// Len returns the number of statements currently held by the cache.
func (sc *StmtCache) Len() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return len(sc.cache)
}

// SQLBytes returns the total size in bytes of the SQL strings currently held
// by the cache.
func (sc *StmtCache) SQLBytes() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.sqlBytes
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
func (sc *StmtCache) Clear() (err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	for key, stmt := range sc.cache {
		delete(sc.cache, key)

		if stmt == nil {
			continue
		}

		if cerr := stmt.Close(); cerr != nil {
			err = cerr
		}
	}
	sc.sqlBytes = 0

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
