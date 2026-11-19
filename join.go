package squirrel

import (
	"fmt"
	"regexp"
	"strings"
)

// joinIdentifierPattern matches a bare SQL identifier. Join table names and
// aliases are validated against it so an attacker-controlled alias cannot
// smuggle whitespace, comments, or placeholders across the alias boundary.
var joinIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// countPlaceholders returns the number of unescaped "?" placeholders in sql.
// A doubled "??" is an escaped literal question mark and is not counted.
func countPlaceholders(sql string) int {
	count := 0
	for i := 0; i < len(sql); i++ {
		if sql[i] != '?' {
			continue
		}
		if i+1 < len(sql) && sql[i+1] == '?' {
			i++
			continue
		}
		count++
	}
	return count
}

// validateJoinTable ensures table is a plain identifier, optionally
// schema-qualified (e.g. "orders" or "public.orders").
func validateJoinTable(table string) error {
	if table == "" {
		return fmt.Errorf("squirrel: join table must not be empty")
	}
	for _, part := range strings.Split(table, ".") {
		if !joinIdentifierPattern.MatchString(part) {
			return fmt.Errorf("squirrel: invalid join table %q: must be an identifier or schema-qualified identifier like \"orders\" or \"public.orders\"", table)
		}
	}
	return nil
}

// validateJoinAlias ensures alias is a single bare identifier, so it cannot
// inject SQL or placeholders into the join clause.
func validateJoinAlias(alias string) error {
	if !joinIdentifierPattern.MatchString(alias) {
		return fmt.Errorf("squirrel: invalid join alias %q: must be a bare identifier matching %q", alias, joinIdentifierPattern.String())
	}
	return nil
}

// joinClause is a structured JOIN: the table/alias boundary and the ON
// condition share one identity, so placeholders in the ON expression can
// only ever bind to the ON expression's own arguments, in order.
type joinClause struct {
	joinType string
	table    string
	alias    string
	on       Sqlizer
}

func newJoinClause(joinType, table, alias string, on Sqlizer) Sqlizer {
	return joinClause{joinType: joinType, table: table, alias: alias, on: on}
}

func (j joinClause) ToSql() (sql string, args []interface{}, err error) {
	if err = validateJoinTable(j.table); err != nil {
		return "", nil, err
	}
	if j.alias != "" {
		if err = validateJoinAlias(j.alias); err != nil {
			return "", nil, err
		}
	}

	var b strings.Builder
	b.WriteString(j.joinType)
	b.WriteString(" ")
	b.WriteString(j.table)
	if j.alias != "" {
		b.WriteString(" AS ")
		b.WriteString(j.alias)
	}

	if j.on != nil {
		onSql, onArgs, onErr := j.on.ToSql()
		if onErr != nil {
			return "", nil, onErr
		}
		if len(onSql) > 0 {
			b.WriteString(" ON ")
			b.WriteString(onSql)
			args = onArgs
		}
	}

	return b.String(), args, nil
}

// joinPart is a join clause part that fails closed: it refuses to emit SQL
// when the number of placeholders in a string predicate does not match the
// number of supplied args. Without this check a mismatch (for example an
// alias containing a smuggled "?") would silently shift every later
// parameter in the statement onto the wrong condition.
type joinPart struct {
	pred interface{}
	args []interface{}
}

func newJoinPart(pred interface{}, args ...interface{}) Sqlizer {
	return joinPart{pred: pred, args: args}
}

func (p joinPart) ToSql() (sql string, args []interface{}, err error) {
	switch pred := p.pred.(type) {
	case nil:
		// no-op
	case Sqlizer:
		if len(p.args) > 0 {
			return "", nil, fmt.Errorf("squirrel: join clause with a Sqlizer predicate cannot bind %d extra arg(s); embed the args in the predicate instead", len(p.args))
		}
		sql, args, err = nestedToSql(pred)
	case string:
		placeholders := countPlaceholders(pred)
		if placeholders != len(p.args) {
			return "", nil, fmt.Errorf("squirrel: join clause %q has %d placeholder(s) but %d arg(s) were supplied; refusing to guess parameter order", pred, placeholders, len(p.args))
		}
		sql = pred
		args = p.args
	default:
		err = fmt.Errorf("expected string or Sqlizer, not %T", pred)
	}
	return
}
