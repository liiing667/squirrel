package squirrel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSelectBuilderJoinOnTwoTables(t *testing.T) {
	// Fixed two-table fixture: schema-qualified table plus a legal alias.
	// Join args must bind to the ON condition and keep their order relative
	// to the WHERE args.
	b := Select("u.id", "o.total").
		From("users u").
		JoinOn("public.orders", "o",
			Expr("o.user_id = u.id AND o.status = ?", "shipped")).
		Where("u.tenant_id = ?", 7).
		PlaceholderFormat(Dollar)

	sql, args, err := b.ToSql()
	assert.NoError(t, err)

	expectedSql := "SELECT u.id, o.total FROM users u " +
		"JOIN public.orders AS o ON o.user_id = u.id AND o.status = $1 " +
		"WHERE u.tenant_id = $2"
	assert.Equal(t, expectedSql, sql)
	assert.Equal(t, []interface{}{"shipped", 7}, args)
}

func TestSelectBuilderJoinOnNoAlias(t *testing.T) {
	sql, args, err := Select("*").
		From("users").
		LeftJoinOn("orders", "", Expr("orders.user_id = users.id")).
		ToSql()
	assert.NoError(t, err)
	assert.Equal(t,
		"SELECT * FROM users LEFT JOIN orders ON orders.user_id = users.id",
		sql)
	assert.Empty(t, args)
}

func TestSelectBuilderJoinOnMaliciousAlias(t *testing.T) {
	maliciousAliases := []string{
		"o ON 1=1 --",          // comment injection past the alias boundary
		"o, secrets s",         // extra table smuggled into the clause
		"o.name",               // qualified name is not a bare alias
		"o?",                   // placeholder smuggled into the alias
		"o); DROP TABLE users", // statement injection
		"1o",                   // not a valid identifier
	}

	for _, alias := range maliciousAliases {
		b := Select("u.id").
			From("users u").
			JoinOn("orders", alias,
				Expr("o.user_id = u.id AND o.status = ?", "shipped"))

		sql, args, err := b.ToSql()
		if assert.Error(t, err, "alias %q must be rejected", alias) {
			assert.Contains(t, err.Error(), "invalid join alias")
			assert.Contains(t, err.Error(), alias)
		}
		assert.Empty(t, sql, "fail closed: no SQL may be emitted")
		assert.Empty(t, args, "fail closed: no args may be emitted")
	}
}

func TestSelectBuilderJoinOnMaliciousTable(t *testing.T) {
	for _, table := range []string{"", "orders o", "orders?", "public..orders"} {
		_, _, err := Select("*").From("users").
			JoinOn(table, "o", Expr("o.user_id = users.id")).
			ToSql()
		if assert.Error(t, err, "table %q must be rejected", table) {
			assert.Contains(t, err.Error(), "join table")
		}
	}
}

func TestSelectBuilderJoinPlaceholderMismatch(t *testing.T) {
	// An attacker-influenced alias adding a "?" to the join string leaves
	// more placeholders than args; binding must fail closed instead of
	// shifting the WHERE arg onto the wrong condition.
	b := Select("*").
		From("users").
		Join("orders o ON o.user_id = users.id AND o.status IN (?, ?)", "shipped").
		Where("tenant_id = ?", 7)

	sql, args, err := b.ToSql()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "2 placeholder(s) but 1 arg(s)")
	}
	assert.Empty(t, sql)
	assert.Empty(t, args)

	// Extra args are just as dangerous: they would slide onto later
	// conditions.
	_, _, err = Select("*").From("users").
		Join("orders o ON o.user_id = users.id AND o.status = ?", "shipped", "extra").
		ToSql()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "1 placeholder(s) but 2 arg(s)")
	}
}

func TestSelectBuilderJoinClauseSqlizerWithArgs(t *testing.T) {
	nested := Select("*").From("orders")
	_, _, err := Select("*").From("users").
		JoinClause(nested.Prefix("JOIN (").Suffix(") o ON o.user_id = users.id"), 42).
		ToSql()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "Sqlizer predicate")
	}
}

func TestCountPlaceholders(t *testing.T) {
	assert.Equal(t, 0, countPlaceholders("no placeholders"))
	assert.Equal(t, 2, countPlaceholders("a = ? AND b = ?"))
	assert.Equal(t, 1, countPlaceholders("a = ?? AND b = ?"))
	assert.Equal(t, 0, countPlaceholders("??"))
}
