package squirrel

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNestedPlaceholderFormatMatrix checks that the outermost builder's
// PlaceholderFormat governs nested builders of every statement kind, and
// that positional placeholders are numbered sequentially across the whole
// statement for each supported dialect.
func TestNestedPlaceholderFormatMatrix(t *testing.T) {
	formats := []struct {
		name string
		f    PlaceholderFormat
		ph   func(i int) string
	}{
		{"Question", Question, func(i int) string { return "?" }},
		{"Dollar", Dollar, func(i int) string { return fmt.Sprintf("$%d", i) }},
		{"Colon", Colon, func(i int) string { return fmt.Sprintf(":%d", i) }},
		{"AtP", AtP, func(i int) string { return fmt.Sprintf("@p%d", i) }},
	}

	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			// Nested SELECT: the subquery carries a different format than
			// the outer query; the outer format must still win.
			sub := Select("id").From("t2").Where("b = ?", 1).PlaceholderFormat(Dollar)
			sql, args, err := Select("a").
				FromSelect(sub, "sub").
				Where("c = ?", 2).
				PlaceholderFormat(tc.f).
				ToSql()
			assert.NoError(t, err)
			assert.Equal(t, fmt.Sprintf(
				"SELECT a FROM (SELECT id FROM t2 WHERE b = %s) AS sub WHERE c = %s",
				tc.ph(1), tc.ph(2)), sql)
			assert.Equal(t, []interface{}{1, 2}, args)

			// INSERT ... SELECT with a nested select carrying Dollar.
			sel := Select("a").From("t2").Where("b = ?", 1).PlaceholderFormat(Dollar)
			sql, args, err = Insert("t1").
				Columns("a").
				Select(sel).
				PlaceholderFormat(tc.f).
				ToSql()
			assert.NoError(t, err)
			assert.Equal(t, fmt.Sprintf(
				"INSERT INTO t1 (a) SELECT a FROM t2 WHERE b = %s",
				tc.ph(1)), sql)
			assert.Equal(t, []interface{}{1}, args)

			// UPDATE with a nested subquery in a SET clause.
			subQ := Select("MAX(y)").From("t2").Where("z = ?", 1).PlaceholderFormat(Dollar)
			sql, args, err = Update("t1").
				Set("x", subQ).
				Where("id = ?", 2).
				PlaceholderFormat(tc.f).
				ToSql()
			assert.NoError(t, err)
			assert.Equal(t, fmt.Sprintf(
				"UPDATE t1 SET x = (SELECT MAX(y) FROM t2 WHERE z = %s) WHERE id = %s",
				tc.ph(1), tc.ph(2)), sql)
			assert.Equal(t, []interface{}{1, 2}, args)

			// DELETE with a nested INSERT in a CTE prefix.
			ins := Insert("log").Columns("msg").Values("audit").PlaceholderFormat(Dollar)
			sql, args, err = Delete("t1").
				Prefix("WITH audit AS (?)", ins).
				Where("id = ?", 7).
				PlaceholderFormat(tc.f).
				ToSql()
			assert.NoError(t, err)
			assert.Equal(t, fmt.Sprintf(
				"WITH audit AS (INSERT INTO log (msg) VALUES (%s)) DELETE FROM t1 WHERE id = %s",
				tc.ph(1), tc.ph(2)), sql)
			assert.Equal(t, []interface{}{"audit", 7}, args)
		})
	}
}

func TestQuestion(t *testing.T) {
	sql := "x = ? AND y = ?"
	s, _ := Question.ReplacePlaceholders(sql)
	assert.Equal(t, sql, s)
}

func TestDollar(t *testing.T) {
	sql := "x = ? AND y = ?"
	s, _ := Dollar.ReplacePlaceholders(sql)
	assert.Equal(t, "x = $1 AND y = $2", s)
}

func TestColon(t *testing.T) {
	sql := "x = ? AND y = ?"
	s, _ := Colon.ReplacePlaceholders(sql)
	assert.Equal(t, "x = :1 AND y = :2", s)
}

func TestAtp(t *testing.T) {
	sql := "x = ? AND y = ?"
	s, _ := AtP.ReplacePlaceholders(sql)
	assert.Equal(t, "x = @p1 AND y = @p2", s)
}

func TestPlaceholders(t *testing.T) {
	assert.Equal(t, Placeholders(2), "?,?")
}

func TestEscapeDollar(t *testing.T) {
	sql := "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ??| array['?'] AND enabled = ?"
	s, _ := Dollar.ReplacePlaceholders(sql)
	assert.Equal(t, "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ?| array['$1'] AND enabled = $2", s)
}

func TestEscapeColon(t *testing.T) {
	sql := "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ??| array['?'] AND enabled = ?"
	s, _ := Colon.ReplacePlaceholders(sql)
	assert.Equal(t, "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ?| array[':1'] AND enabled = :2", s)
}

func TestEscapeAtp(t *testing.T) {
	sql := "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ??| array['?'] AND enabled = ?"
	s, _ := AtP.ReplacePlaceholders(sql)
	assert.Equal(t, "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ?| array['@p1'] AND enabled = @p2", s)
}

func BenchmarkPlaceholdersArray(b *testing.B) {
	var count = b.N
	placeholders := make([]string, count)
	for i := 0; i < count; i++ {
		placeholders[i] = "?"
	}
	var _ = strings.Join(placeholders, ",")
}

func BenchmarkPlaceholdersStrings(b *testing.B) {
	Placeholders(b.N)
}
