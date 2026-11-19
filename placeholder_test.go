package squirrel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
	assert.Equal(t, "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ?| array['?'] AND enabled = $1", s)
}

func TestEscapeColon(t *testing.T) {
	sql := "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ??| array['?'] AND enabled = ?"
	s, _ := Colon.ReplacePlaceholders(sql)
	assert.Equal(t, "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ?| array['?'] AND enabled = :1", s)
}

func TestEscapeAtp(t *testing.T) {
	sql := "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ??| array['?'] AND enabled = ?"
	s, _ := AtP.ReplacePlaceholders(sql)
	assert.Equal(t, "SELECT uuid, \"data\" #> '{tags}' AS tags FROM nodes WHERE  \"data\" -> 'tags' ?| array['?'] AND enabled = @p1", s)
}

func TestDollarIgnoresQuestionMarksInLiterals(t *testing.T) {
	sql := "SELECT * FROM nodes WHERE note = 'what?' AND title = \"why?\" AND tag = `hm?` AND id = ?"
	s, err := Dollar.ReplacePlaceholders(sql)
	assert.NoError(t, err)
	assert.Equal(t, "SELECT * FROM nodes WHERE note = 'what?' AND title = \"why?\" AND tag = `hm?` AND id = $1", s)
}

func TestDollarIgnoresQuestionMarksInEscapedQuotes(t *testing.T) {
	sql := "SELECT * FROM nodes WHERE note = 'it''s ?' AND id = ?"
	s, err := Dollar.ReplacePlaceholders(sql)
	assert.NoError(t, err)
	assert.Equal(t, "SELECT * FROM nodes WHERE note = 'it''s ?' AND id = $1", s)
}

func TestDollarIgnoresQuestionMarksInComments(t *testing.T) {
	sql := "SELECT * /* why? /* nested? */ still? */ FROM nodes -- where?\nWHERE id = ?"
	s, err := Dollar.ReplacePlaceholders(sql)
	assert.NoError(t, err)
	assert.Equal(t, "SELECT * /* why? /* nested? */ still? */ FROM nodes -- where?\nWHERE id = $1", s)
}

func TestDollarIgnoresQuestionMarksInDollarQuotedStrings(t *testing.T) {
	sql := "SELECT $$what?$$, $body$who? what?$body$ FROM nodes WHERE id = ?"
	s, err := Dollar.ReplacePlaceholders(sql)
	assert.NoError(t, err)
	assert.Equal(t, "SELECT $$what?$$, $body$who? what?$body$ FROM nodes WHERE id = $1", s)
}

func TestDollarPostgresQuestionOperators(t *testing.T) {
	sql := "SELECT * FROM nodes WHERE data ?| ?::text[] AND data ?& ?::text[] AND id = ?"
	s, err := Dollar.ReplacePlaceholders(sql)
	assert.NoError(t, err)
	assert.Equal(t, "SELECT * FROM nodes WHERE data ?| $1::text[] AND data ?& $2::text[] AND id = $3", s)
}

func TestDollarUnterminatedLiteralsAreRejected(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM nodes WHERE note = 'oops ? AND id = ?",
		"SELECT * FROM nodes WHERE note = \"oops ? AND id = ?",
		"SELECT * /* oops ? FROM nodes WHERE id = ?",
		"SELECT $body$oops ? FROM nodes WHERE id = ?",
	} {
		_, err := Dollar.ReplacePlaceholders(sql)
		if assert.Error(t, err, sql) {
			assert.Contains(t, err.Error(), "unterminated", sql)
		}
	}
}

func TestPlaceholderArgOrderWithLiterals(t *testing.T) {
	sql, args, err := Select("id").From("nodes").
		Where("note = '?' AND a = ?", 1).
		Where(Expr("body = $$has ? mark$$ AND b = ?", 2)).
		Where(Expr("data ?| ?::text[] AND c = ?", 3, 4)).
		Where(Expr("hint = '??' AND d = ?", 5)).
		PlaceholderFormat(Dollar).
		ToSql()
	assert.NoError(t, err)
	assert.Equal(t,
		"SELECT id FROM nodes WHERE note = '?' AND a = $1 AND body = $$has ? mark$$ AND b = $2 "+
			"AND data ?| $3::text[] AND c = $4 AND hint = '??' AND d = $5",
		sql)
	assert.Equal(t, []interface{}{1, 2, 3, 4, 5}, args)
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
