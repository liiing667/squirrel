package squirrel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		// valid identifiers
		{name: "simple column", input: "id"},
		{name: "underscore start", input: "_tmp"},
		{name: "mixed case", input: "TenantID"},
		{name: "digits and dollar", input: "col_2$"},
		{name: "dotted schema.table", input: "public.users"},
		{name: "dotted schema.table.column", input: "public.users.id"},
		{name: "star", input: "*"},
		{name: "table star", input: "users.*"},
		{name: "reserved word is valid", input: "order"},
		// malicious / invalid identifiers
		{name: "empty", input: "", wantErr: "empty"},
		{name: "empty part", input: "public..users", wantErr: "empty part"},
		{name: "leading dot", input: ".users", wantErr: "empty part"},
		{name: "trailing dot", input: "users.", wantErr: "empty part"},
		{name: "semicolon injection", input: "users; DROP TABLE users", wantErr: "disallowed character"},
		{name: "double quote breakout", input: `u" AS "x`, wantErr: "disallowed character"},
		{name: "backtick breakout", input: "u` AS `x", wantErr: "disallowed character"},
		{name: "single quote", input: "u'", wantErr: "disallowed character"},
		{name: "NUL byte", input: "u\x00sers", wantErr: "control character"},
		{name: "newline", input: "users\nDROP", wantErr: "control character"},
		{name: "tab", input: "us\ters", wantErr: "control character"},
		{name: "delete control char", input: "us\x7fers", wantErr: "control character"},
		{name: "space", input: "user name", wantErr: "disallowed character"},
		{name: "comment sequence", input: "users--", wantErr: "disallowed character"},
		{name: "parenthesis", input: "count(*)", wantErr: "disallowed character"},
		{name: "leading digit", input: "1col", wantErr: "must start"},
		{name: "unicode homoglyph", input: "usérs", wantErr: "non-ASCII"},
		{name: "star not alone", input: "user*", wantErr: "disallowed character"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateIdentifier(c.input)
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), c.wantErr)
				assert.IsType(t, &InvalidIdentifierError{}, err)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "simple", input: "users", want: `"users"`},
		{name: "dotted schema", input: "public.users", want: `"public"."users"`},
		{name: "dotted column", input: "public.users.id", want: `"public"."users"."id"`},
		{name: "reserved word quoted", input: "order", want: `"order"`},
		{name: "star unquoted", input: "*", want: `*`},
		{name: "table star", input: "users.*", want: `"users".*`},
		{name: "injection rejected", input: "users; DROP TABLE users--", wantErr: true},
		{name: "quote rejected", input: `a"b`, wantErr: true},
		{name: "NUL rejected", input: "a\x00b", wantErr: true},
		{name: "empty rejected", input: "", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := QuoteIdentifier(c.input)
			if c.wantErr {
				assert.Error(t, err)
				assert.Empty(t, got)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestIsReservedWord(t *testing.T) {
	for _, word := range []string{"select", "SELECT", "Order", "from", "table", "where"} {
		assert.True(t, IsReservedWord(word), word)
	}
	for _, word := range []string{"users", "id", "tenant_id", "selection"} {
		assert.False(t, IsReservedWord(word), word)
	}
}

func TestSafeIdentifierSqlizer(t *testing.T) {
	t.Run("quoted output with no args", func(t *testing.T) {
		sql, args, err := SafeIdentifier("public.users").ToSql()
		assert.NoError(t, err)
		assert.Equal(t, `"public"."users"`, sql)
		assert.Empty(t, args)
	})
	t.Run("invalid name errors from ToSql", func(t *testing.T) {
		_, _, err := SafeIdentifier("users; DROP TABLE users").ToSql()
		assert.Error(t, err)
		assert.IsType(t, &InvalidIdentifierError{}, err)
	})
	t.Run("placeholder order preserved inside Expr", func(t *testing.T) {
		sql, args, err := Expr(
			"SELECT * FROM ? WHERE a = ? AND b = ?",
			SafeIdentifier("public.users"), "first", "second",
		).ToSql()
		assert.NoError(t, err)
		assert.Equal(t, `SELECT * FROM "public"."users" WHERE a = ? AND b = ?`, sql)
		assert.Equal(t, []interface{}{"first", "second"}, args)
	})
	t.Run("invalid name propagates through Expr", func(t *testing.T) {
		_, _, err := Expr("SELECT * FROM ?", SafeIdentifier("bad name")).ToSql()
		assert.Error(t, err)
	})
}

// TestIdentifierDefaultBehaviorUnchanged pins the existing raw-passthrough
// semantics of Eq, From and Alias: the safe identifier API is strictly
// opt-in and must not silently rewrite caller-provided SQL fragments.
func TestIdentifierDefaultBehaviorUnchanged(t *testing.T) {
	sql, args, err := Select("id").From("users u").Where(Eq{"u.tenant_id": 7}).ToSql()
	assert.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users u WHERE u.tenant_id = ?", sql)
	assert.Equal(t, []interface{}{7}, args)

	sql, _, err = Select("id").Column(Alias(Expr("COUNT(*)"), "total")).From("users").ToSql()
	assert.NoError(t, err)
	assert.Equal(t, "SELECT id, (COUNT(*)) AS total FROM users", sql)

	// Quoted identifiers compose with the existing string-based APIs.
	from := MustQuoteIdentifier("public.users")
	key := MustQuoteIdentifier("public.users.id")
	sql, args, err = Select("id").From(from).Where(Eq{key: 7}).ToSql()
	assert.NoError(t, err)
	assert.Equal(t, `SELECT id FROM "public"."users" WHERE "public"."users"."id" = ?`, sql)
	assert.Equal(t, []interface{}{7}, args)

	assert.True(t, strings.HasPrefix(MustQuoteIdentifier("order"), `"`))
}
