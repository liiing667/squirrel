package squirrel

import (
	"fmt"
	"regexp"
	"strings"
)

// onKeyword matches the ON keyword that separates the table/alias portion of
// a JOIN clause from its join condition. It is matched case-insensitively on
// word boundaries so table or alias names merely containing "on" (e.g.
// "button") do not split the clause.
var onKeyword = regexp.MustCompile(`(?i)\bon\b`)

// joinPart is a Sqlizer for string-based JOIN clauses. Unlike a plain part,
// it ties the table/alias portion and the ON condition to a single identity:
// placeholders (and therefore the args bound to them) may only appear in the
// ON condition, and the number of args must match the number of placeholders
// in that condition exactly. This prevents a hostile table/alias fragment
// from smuggling placeholders that would shift later args onto the wrong
// condition.
type joinPart struct {
	sql  string
	args []interface{}
}

func newJoinPart(sql string, args ...interface{}) Sqlizer {
	return joinPart{sql: sql, args: args}
}

// splitJoinClause splits a JOIN clause into its table/alias portion and its
// ON condition. hasOn reports whether an ON keyword was found.
func splitJoinClause(sql string) (table string, condition string, hasOn bool) {
	loc := onKeyword.FindStringIndex(sql)
	if loc == nil {
		return sql, "", false
	}
	return sql[:loc[0]], sql[loc[1]:], true
}

func (j joinPart) ToSql() (string, []interface{}, error) {
	table, condition, hasOn := splitJoinClause(j.sql)

	if strings.Contains(table, "?") {
		return "", nil, fmt.Errorf(
			"squirrel: join clause %q: placeholder in table/alias portion (before ON); "+
				"placeholders are only allowed in the ON condition", j.sql)
	}

	if !hasOn {
		if len(j.args) > 0 {
			return "", nil, fmt.Errorf(
				"squirrel: join clause %q: %d arg(s) provided but no ON condition to bind them to",
				j.sql, len(j.args))
		}
		return j.sql, nil, nil
	}

	placeholders := strings.Count(condition, "?")
	if placeholders != len(j.args) {
		return "", nil, fmt.Errorf(
			"squirrel: join clause %q: ON condition has %d placeholder(s) but %d arg(s) were provided",
			j.sql, placeholders, len(j.args))
	}

	return j.sql, j.args, nil
}

// newJoinClausePart builds the Sqlizer for a JoinClause. String preds get a
// joinPart so the alias boundary, join condition, and placeholder order are
// validated together; Sqlizer preds (e.g. nested selects) keep the existing
// behavior.
func newJoinClausePart(pred interface{}, args ...interface{}) Sqlizer {
	if sql, ok := pred.(string); ok {
		return newJoinPart(sql, args...)
	}
	return newPart(pred, args...)
}
