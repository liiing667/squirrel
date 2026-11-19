package squirrel

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/lann/builder"
)

func init() {
	builder.Register(CaseBuilder{}, caseData{})
}

// whenPart is a helper structure to describe SQLs "WHEN ... THEN ..." expression
type whenPart struct {
	when Sqlizer
	then Sqlizer
}

func newWhenPart(when interface{}, then interface{}) whenPart {
	return whenPart{newPart(when), newPart(then)}
}

// caseData holds all the data required to build a CASE SQL construct
type caseData struct {
	What      Sqlizer
	WhenParts []whenPart
	Else      Sqlizer
}

// ToSql implements Sqlizer
func (d *caseData) ToSql() (string, []interface{}, error) {
	if len(d.WhenParts) == 0 {
		return "", nil, errors.New("case expression must contain at lease one WHEN clause")
	}

	sql := &bytes.Buffer{}
	var args []interface{}

	sql.WriteString("CASE ")
	if d.What != nil {
		whatSql, whatArgs, err := nestedToSql(d.What)
		if err != nil {
			return "", nil, err
		}
		sql.WriteString(whatSql)
		sql.WriteByte(' ')
		args = append(args, whatArgs...)
	}

	for i, p := range d.WhenParts {
		whenSql, whenArgs, err := nestedToSql(p.when)
		if err != nil {
			return "", nil, fmt.Errorf("case WHEN #%d: %w", i+1, err)
		}
		if len(whenSql) == 0 {
			return "", nil, fmt.Errorf("case WHEN #%d must not be empty", i+1)
		}

		thenSql, thenArgs, err := nestedToSql(p.then)
		if err != nil {
			return "", nil, fmt.Errorf("case THEN #%d: %w", i+1, err)
		}
		if len(thenSql) == 0 {
			return "", nil, fmt.Errorf("case THEN #%d must not be empty", i+1)
		}

		sql.WriteString("WHEN ")
		sql.WriteString(whenSql)
		sql.WriteByte(' ')
		sql.WriteString("THEN ")
		sql.WriteString(thenSql)
		sql.WriteByte(' ')

		args = append(args, whenArgs...)
		args = append(args, thenArgs...)
	}

	if d.Else != nil {
		elseSql, elseArgs, err := nestedToSql(d.Else)
		if err != nil {
			return "", nil, err
		}
		sql.WriteString("ELSE ")
		sql.WriteString(elseSql)
		sql.WriteByte(' ')
		args = append(args, elseArgs...)
	}

	sql.WriteString("END")

	return sql.String(), args, nil
}

// CaseBuilder builds SQL CASE construct which could be used as parts of queries.
type CaseBuilder builder.Builder

// ToSql builds the query into a SQL string and bound args.
func (b CaseBuilder) ToSql() (string, []interface{}, error) {
	data := builder.GetStruct(b).(caseData)
	return data.ToSql()
}

// MustSql builds the query into a SQL string and bound args.
// It panics if there are any errors.
func (b CaseBuilder) MustSql() (string, []interface{}) {
	sql, args, err := b.ToSql()
	if err != nil {
		panic(err)
	}
	return sql, args
}

// what sets optional value for CASE construct "CASE [value] ..."
func (b CaseBuilder) what(expr interface{}) CaseBuilder {
	return builder.Set(b, "What", newPart(expr)).(CaseBuilder)
}

// When adds "WHEN ... THEN ..." part to CASE construct
func (b CaseBuilder) When(when interface{}, then interface{}) CaseBuilder {
	// TODO: performance hint: replace slice of WhenPart with just slice of parts
	// where even indices of the slice belong to "when"s and odd indices belong to "then"s
	return builder.Append(b, "WhenParts", newWhenPart(when, then)).(CaseBuilder)
}

// What sets optional "ELSE ..." part for CASE construct
func (b CaseBuilder) Else(expr interface{}) CaseBuilder {
	return builder.Set(b, "Else", newPart(expr)).(CaseBuilder)
}
