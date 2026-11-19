package squirrel

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStmtCachePrepare(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCache(db)
	query := "SELECT 1"

	sc.Prepare(query)
	assert.Equal(t, query, db.LastPrepareSql)

	sc.Prepare(query)
	assert.Equal(t, 1, db.PrepareCount, "expected 1 Prepare, got %d", db.PrepareCount)

	// clear statement cache
	assert.Nil(t, sc.Clear())

	// should prepare the query again
	sc.Prepare(query)
	assert.Equal(t, 2, db.PrepareCount, "expected 2 Prepare, got %d", db.PrepareCount)
}

func TestStmtCacheMaxEntries(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCacheWithOptions(db, WithMaxEntries(2))

	first := "SELECT 1"
	second := "SELECT 2"
	third := "SELECT 3"

	_, err := sc.Prepare(first)
	assert.NoError(t, err)
	_, err = sc.Prepare(second)
	assert.NoError(t, err)
	assert.Equal(t, 2, db.PrepareCount)
	assert.Equal(t, 2, sc.Len())

	// Exceeding the entry budget fails before touching the underlying Preparer.
	_, err = sc.Prepare(third)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCacheLimit))
	assert.Equal(t, 2, db.PrepareCount, "expected no extra Prepare, got %d", db.PrepareCount)
	assert.Equal(t, 2, sc.Len())

	// Existing entries are still served as cache hits.
	_, err = sc.Prepare(first)
	assert.NoError(t, err)
	assert.Equal(t, 2, db.PrepareCount, "expected cache hit, got Prepare count %d", db.PrepareCount)

	// Clear restores the full budget.
	assert.Nil(t, sc.Clear())
	assert.Equal(t, 0, sc.Len())

	_, err = sc.Prepare(third)
	assert.NoError(t, err)
	assert.Equal(t, 3, db.PrepareCount, "expected 3 Prepare after Clear, got %d", db.PrepareCount)
	assert.Equal(t, 1, sc.Len())
}

func TestStmtCacheMaxSQLBytes(t *testing.T) {
	db := &DBStub{}
	first := "SELECT 1"
	second := "SELECT 1, 2"
	third := "SELECT 1, 2, 3"

	// Budget covers the first two queries but not all three.
	budget := len(first) + len(second)
	sc := NewStmtCacheWithOptions(db, WithMaxSQLBytes(budget))

	_, err := sc.Prepare(first)
	assert.NoError(t, err)
	assert.Equal(t, len(first), sc.SQLBytes())

	// A query larger than the whole budget fails before Prepare is called.
	oversized := strings.Repeat("x", budget+1)
	_, err = sc.Prepare(oversized)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCacheLimit))
	assert.Equal(t, 1, db.PrepareCount)
	assert.Equal(t, len(first), sc.SQLBytes())

	// A query that fits the remaining budget is prepared and accounted for.
	_, err = sc.Prepare(second)
	assert.NoError(t, err)
	assert.Equal(t, 2, db.PrepareCount)
	assert.Equal(t, budget, sc.SQLBytes())

	// The budget is now exhausted, so another query is rejected.
	_, err = sc.Prepare(third)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCacheLimit))
	assert.Equal(t, 2, db.PrepareCount)
	assert.Equal(t, budget, sc.SQLBytes())

	// Cache hits on the fitting queries remain untouched by the budget.
	_, err = sc.Prepare(first)
	assert.NoError(t, err)
	_, err = sc.Prepare(second)
	assert.NoError(t, err)
	assert.Equal(t, 2, db.PrepareCount)

	// Clear resets the SQL byte accounting and restores the full budget.
	assert.Nil(t, sc.Clear())
	assert.Equal(t, 0, sc.SQLBytes())

	_, err = sc.Prepare(third)
	assert.NoError(t, err)
	assert.Equal(t, 3, db.PrepareCount)
	assert.Equal(t, len(third), sc.SQLBytes())
}

func TestStmtCachePrepareFailureDoesNotConsumeBudget(t *testing.T) {
	db := &DBStub{err: StubError}
	sc := NewStmtCacheWithOptions(db, WithMaxEntries(1))
	query := "SELECT 1"

	_, err := sc.Prepare(query)
	assert.Equal(t, StubError, err)
	assert.Equal(t, 0, sc.Len(), "failed Prepare must not be cached")

	// The budget stays available for the next attempt after Clear-less recovery.
	db.err = nil
	_, err = sc.Prepare(query)
	assert.NoError(t, err)
	assert.Equal(t, 1, sc.Len())
	assert.Equal(t, 2, db.PrepareCount)
}

func TestStmtCacheWithoutOptionsIsUnlimited(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCache(db)

	for i := 0; i < 100; i++ {
		_, err := sc.Prepare("SELECT " + string(rune('a'+i%26)))
		assert.NoError(t, err)
	}
	assert.Equal(t, 26, sc.Len())
}
