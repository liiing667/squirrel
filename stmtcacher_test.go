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

func TestStmtCacheNestedPlaceholderConsistency(t *testing.T) {
	// The same logical query must produce the same SQL regardless of the
	// placeholder format carried by the nested builder, so both builds hit
	// the same statement cache entry.
	build := func(inner SelectBuilder) (string, []interface{}, error) {
		return StatementBuilder.PlaceholderFormat(Dollar).
			Select("a").From("t1").
			Where(Expr("x IN (?)", inner)).
			Where("z = ?", 2).
			ToSql()
	}

	innerQuestion := StatementBuilder.Select("b").From("t2").Where("y = ?", 1)
	innerDollar := StatementBuilder.PlaceholderFormat(Dollar).Select("b").From("t2").Where("y = ?", 1)

	sqlQ, argsQ, err := build(innerQuestion)
	assert.NoError(t, err)
	sqlD, argsD, err := build(innerDollar)
	assert.NoError(t, err)

	expectedSql := "SELECT a FROM t1 WHERE x IN (SELECT b FROM t2 WHERE y = $1) AND z = $2"
	assert.Equal(t, expectedSql, sqlQ)
	assert.Equal(t, expectedSql, sqlD)
	assert.Equal(t, argsQ, argsD)

	db := &DBStub{}
	sc := NewStmtCache(db)

	sc.Prepare(sqlQ)
	sc.Prepare(sqlD)
	assert.Equal(t, 1, db.PrepareCount, "expected 1 Prepare, got %d", db.PrepareCount)
}
