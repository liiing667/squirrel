//go:build go1.8
// +build go1.8

package squirrel

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canceledOncePreparer fails PrepareContext with the canceled context's error
// once, then delegates to a real *sql.DB. It models a driver that reports
// cancellation instead of short-circuiting inside database/sql.
type canceledOncePreparer struct {
	db       *sql.DB
	canceled context.Context
	failed   bool
}

func (c *canceledOncePreparer) Prepare(query string) (*sql.Stmt, error) {
	return c.db.Prepare(query)
}

func (c *canceledOncePreparer) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if !c.failed && c.canceled != nil && c.canceled.Err() != nil {
		c.failed = true
		return nil, ctx.Err()
	}
	return c.db.PrepareContext(ctx, query)
}

var _ PreparerContext = (*canceledOncePreparer)(nil)

func TestStmtCachePrepareContextCanceledIsObservableAndNotCached(t *testing.T) {
	db, drv := newFakeDB(t)
	query := "SELECT 1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sc := NewStmtCache(&canceledOncePreparer{db: db, canceled: ctx})
	stmt, err := sc.PrepareContext(ctx, query)
	assert.Nil(t, stmt)
	assert.Equal(t, context.Canceled, err)

	state, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusCanceled, state.Status)
	assert.Error(t, state.Err)
	assert.Equal(t, sc.Generation(), state.Generation)
	assert.NotContains(t, sc.cache, query, "canceled prepare must not pollute the cache")

	// A healthy retry succeeds and gets cached.
	stmt, retryState, err := sc.PrepareContextState(context.Background(), query)
	require.NoError(t, err)
	assert.NotNil(t, stmt)
	assert.Equal(t, StmtStatusPrepared, retryState.Status)
	assert.Equal(t, uint64(0), retryState.Generation)

	stmt2, err := sc.PrepareContext(context.Background(), query)
	require.NoError(t, err)
	assert.Equal(t, stmt, stmt2)
	assert.Equal(t, 1, drv.prepareCount())
}

// preparerOnly implements Preparer but not PreparerContext.
type preparerOnly struct{}

func (n *preparerOnly) Prepare(query string) (*sql.Stmt, error) {
	return nil, nil
}

var _ Preparer = (*preparerOnly)(nil)

// newCacheForPreparer builds a StmtCache around a plain Preparer that lacks
// PrepareContext, exercising the NoContextSupport branch.
func newCacheForPreparer(p Preparer) *StmtCache {
	return &StmtCache{
		prep:     p,
		cache:    make(map[string]*sql.Stmt),
		inflight: make(map[string]*inflightPrepare),
		states:   make(map[string]StmtState),
	}
}

func TestStmtCachePrepareContextNoContextSupportIsObservable(t *testing.T) {
	sc := newCacheForPreparer(&preparerOnly{})
	query := "SELECT 1"

	stmt, err := sc.PrepareContext(context.Background(), query)
	assert.Nil(t, stmt)
	assert.Equal(t, NoContextSupport, err)

	state, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusFailed, state.Status)
	assert.Equal(t, NoContextSupport, state.Err)
	assert.NotContains(t, sc.cache, query)
}
