package squirrel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInsertBuilderToSql(t *testing.T) {
	b := Insert("").
		Prefix("WITH prefix AS ?", 0).
		Into("a").
		Options("DELAYED", "IGNORE").
		Columns("b", "c").
		Values(1, 2).
		Values(3, Expr("? + 1", 4)).
		Suffix("RETURNING ?", 5)

	sql, args, err := b.ToSql()
	assert.NoError(t, err)

	expectedSQL :=
		"WITH prefix AS ? " +
			"INSERT DELAYED IGNORE INTO a (b,c) VALUES (?,?),(?,? + 1) " +
			"RETURNING ?"
	assert.Equal(t, expectedSQL, sql)

	expectedArgs := []interface{}{0, 1, 2, 3, 4, 5}
	assert.Equal(t, expectedArgs, args)
}

func TestInsertBuilderToSqlErr(t *testing.T) {
	_, _, err := Insert("").Values(1).ToSql()
	assert.Error(t, err)

	_, _, err = Insert("x").ToSql()
	assert.Error(t, err)
}

func TestInsertBuilderMustSql(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("TestInsertBuilderMustSql should have panicked!")
		}
	}()
	Insert("").MustSql()
}

func TestInsertBuilderPlaceholders(t *testing.T) {
	b := Insert("test").Values(1, 2)

	sql, _, _ := b.PlaceholderFormat(Question).ToSql()
	assert.Equal(t, "INSERT INTO test VALUES (?,?)", sql)

	sql, _, _ = b.PlaceholderFormat(Dollar).ToSql()
	assert.Equal(t, "INSERT INTO test VALUES ($1,$2)", sql)
}

func TestInsertBuilderRunners(t *testing.T) {
	db := &DBStub{}
	b := Insert("test").Values(1).RunWith(db)

	expectedSQL := "INSERT INTO test VALUES (?)"

	b.Exec()
	assert.Equal(t, expectedSQL, db.LastExecSql)
}

func TestInsertBuilderNoRunner(t *testing.T) {
	b := Insert("test").Values(1)

	_, err := b.Exec()
	assert.Equal(t, RunnerNotSet, err)
}

func TestInsertBuilderSetMap(t *testing.T) {
	b := Insert("table").SetMap(Eq{"field1": 1, "field2": 2, "field3": 3})

	sql, args, err := b.ToSql()
	assert.NoError(t, err)

	expectedSQL := "INSERT INTO table (field1,field2,field3) VALUES (?,?,?)"
	assert.Equal(t, expectedSQL, sql)

	expectedArgs := []interface{}{1, 2, 3}
	assert.Equal(t, expectedArgs, args)
}

func TestInsertBuilderSelect(t *testing.T) {
	sb := Select("field1").From("table1").Where(Eq{"field1": 1})
	ib := Insert("table2").Columns("field1").Select(sb)

	sql, args, err := ib.ToSql()
	assert.NoError(t, err)

	expectedSQL := "INSERT INTO table2 (field1) SELECT field1 FROM table1 WHERE field1 = ?"
	assert.Equal(t, expectedSQL, sql)

	expectedArgs := []interface{}{1}
	assert.Equal(t, expectedArgs, args)
}

func TestInsertBuilderReplace(t *testing.T) {
	b := Replace("table").Values(1)

	expectedSQL := "REPLACE INTO table VALUES (?)"

	sql, _, err := b.ToSql()
	assert.NoError(t, err)

	assert.Equal(t, expectedSQL, sql)
}

func TestInsertBuilderValuesColumnMismatch(t *testing.T) {
	// Fewer values than columns.
	sql, args, err := Insert("test").Columns("a", "b").Values(1).ToSql()
	assert.Error(t, err)
	assert.Empty(t, sql)
	assert.Empty(t, args)

	// More values than columns.
	_, _, err = Insert("test").Columns("a").Values(1, 2).ToSql()
	assert.Error(t, err)

	// Mismatch in a later row of a batch.
	_, _, err = Insert("test").Columns("a", "b").Values(1, 2).Values(3).ToSql()
	assert.Error(t, err)

	// Inconsistent row widths without declared columns.
	_, _, err = Insert("test").Values(1, 2).Values(3).ToSql()
	assert.Error(t, err)

	// Validation happens before placeholder formatting, so every dialect
	// fails closed.
	sql, _, err = Insert("test").Columns("a", "b").Values(1).
		PlaceholderFormat(Dollar).ToSql()
	assert.Error(t, err)
	assert.Empty(t, sql)

	// The REPLACE chain shares the same validation.
	_, _, err = Replace("test").Columns("a", "b").Values(1).ToSql()
	assert.Error(t, err)
}

func TestInsertBuilderValuesColumnMatch(t *testing.T) {
	// Nested expressions count as a single value; legal batch values keep
	// working across placeholder formats.
	b := Insert("test").
		Columns("a", "b").
		Values(Expr("? + 1", 1), 2).
		Values(3, 4).
		PlaceholderFormat(Dollar)

	sql, args, err := b.ToSql()
	assert.NoError(t, err)
	assert.Equal(t, "INSERT INTO test (a,b) VALUES ($1 + 1,$2),($3,$4)", sql)
	assert.Equal(t, []interface{}{1, 2, 3, 4}, args)

	sql, args, err = b.PlaceholderFormat(Question).ToSql()
	assert.NoError(t, err)
	assert.Equal(t, "INSERT INTO test (a,b) VALUES (? + 1,?),(?,?)", sql)
	assert.Equal(t, []interface{}{1, 2, 3, 4}, args)
}
