//go:build go1.8
// +build go1.8

package squirrel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStmtCacherPrepareContext(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCache(db)
	query := "SELECT 1"

	sc.PrepareContext(ctx, query)
	assert.Equal(t, query, db.LastPrepareSql)

	sc.PrepareContext(ctx, query)
	assert.Equal(t, 1, db.PrepareCount, "expected 1 Prepare, got %d", db.PrepareCount)
}

func TestStmtCacherPrepareContextLimit(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCacheWithOptions(db, WithMaxEntries(1))

	_, err := sc.PrepareContext(ctx, "SELECT 1")
	assert.NoError(t, err)

	_, err = sc.PrepareContext(ctx, "SELECT 2")
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCacheLimit))
	assert.Equal(t, 1, db.PrepareCount, "expected limit error before Prepare, got %d", db.PrepareCount)

	// The cached statement remains available through the context path.
	_, err = sc.PrepareContext(ctx, "SELECT 1")
	assert.NoError(t, err)
	assert.Equal(t, 1, db.PrepareCount)

	assert.Nil(t, sc.Clear())

	_, err = sc.PrepareContext(ctx, "SELECT 2")
	assert.NoError(t, err)
	assert.Equal(t, 2, db.PrepareCount, "expected 2 Prepare after Clear, got %d", db.PrepareCount)
}
