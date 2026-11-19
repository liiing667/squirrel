package squirrel

import (
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

func TestStmtCacheNestedPlaceholderFormat(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCache(db)

	// A nested query rendered with the outer builder's format must produce
	// one deterministic SQL string, so statement cache hits stay aligned
	// with the args built for that exact string.
	b := Select("*").From("foo").
		Where(Expr("x IN (?)", Select("y").From("bar").Where(Eq{"z": 2}))).
		PlaceholderFormat(Dollar)

	query, args, err := b.ToSql()
	assert.NoError(t, err)
	assert.Equal(t, "SELECT * FROM foo WHERE x IN (SELECT y FROM bar WHERE z = $1)", query)
	assert.Equal(t, []interface{}{2}, args)

	// repeated ToSql of the same builder is deterministic
	again, againArgs, err := b.ToSql()
	assert.NoError(t, err)
	assert.Equal(t, query, again)
	assert.Equal(t, args, againArgs)

	sc.Prepare(query)
	sc.Prepare(query)
	assert.Equal(t, query, db.LastPrepareSql)
	assert.Equal(t, 1, db.PrepareCount, "expected 1 Prepare, got %d", db.PrepareCount)
}
