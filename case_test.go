package squirrel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCaseWithVal(t *testing.T) {
	caseStmt := Case("number").
		When("1", "one").
		When("2", "two").
		Else(Expr("?", "big number"))

	qb := Select().
		Column(caseStmt).
		From("table")
	sql, args, err := qb.ToSql()

	assert.NoError(t, err)

	expectedSql := "SELECT CASE number " +
		"WHEN 1 THEN one " +
		"WHEN 2 THEN two " +
		"ELSE ? " +
		"END " +
		"FROM table"
	assert.Equal(t, expectedSql, sql)

	expectedArgs := []interface{}{"big number"}
	assert.Equal(t, expectedArgs, args)
}

func TestCaseWithComplexVal(t *testing.T) {
	caseStmt := Case("? > ?", 10, 5).
		When("true", "'T'")

	qb := Select().
		Column(Alias(caseStmt, "complexCase")).
		From("table")
	sql, args, err := qb.ToSql()

	assert.NoError(t, err)

	expectedSql := "SELECT (CASE ? > ? " +
		"WHEN true THEN 'T' " +
		"END) AS complexCase " +
		"FROM table"
	assert.Equal(t, expectedSql, sql)

	expectedArgs := []interface{}{10, 5}
	assert.Equal(t, expectedArgs, args)
}

func TestCaseWithNoVal(t *testing.T) {
	caseStmt := Case().
		When(Eq{"x": 0}, "x is zero").
		When(Expr("x > ?", 1), Expr("CONCAT('x is greater than ', ?)", 2))

	qb := Select().Column(caseStmt).From("table")
	sql, args, err := qb.ToSql()

	assert.NoError(t, err)

	expectedSql := "SELECT CASE " +
		"WHEN x = ? THEN x is zero " +
		"WHEN x > ? THEN CONCAT('x is greater than ', ?) " +
		"END " +
		"FROM table"

	assert.Equal(t, expectedSql, sql)

	expectedArgs := []interface{}{0, 1, 2}
	assert.Equal(t, expectedArgs, args)
}

func TestCaseWithExpr(t *testing.T) {
	caseStmt := Case(Expr("x = ?", true)).
		When("true", Expr("?", "it's true!")).
		Else("42")

	qb := Select().Column(caseStmt).From("table")
	sql, args, err := qb.ToSql()

	assert.NoError(t, err)

	expectedSql := "SELECT CASE x = ? " +
		"WHEN true THEN ? " +
		"ELSE 42 " +
		"END " +
		"FROM table"

	assert.Equal(t, expectedSql, sql)

	expectedArgs := []interface{}{true, "it's true!"}
	assert.Equal(t, expectedArgs, args)
}

func TestMultipleCase(t *testing.T) {
	caseStmtNoval := Case(Expr("x = ?", true)).
		When("true", Expr("?", "it's true!")).
		Else("42")
	caseStmtExpr := Case().
		When(Eq{"x": 0}, "'x is zero'").
		When(Expr("x > ?", 1), Expr("CONCAT('x is greater than ', ?)", 2))

	qb := Select().
		Column(Alias(caseStmtNoval, "case_noval")).
		Column(Alias(caseStmtExpr, "case_expr")).
		From("table")

	sql, args, err := qb.ToSql()

	assert.NoError(t, err)

	expectedSql := "SELECT " +
		"(CASE x = ? WHEN true THEN ? ELSE 42 END) AS case_noval, " +
		"(CASE WHEN x = ? THEN 'x is zero' WHEN x > ? THEN CONCAT('x is greater than ', ?) END) AS case_expr " +
		"FROM table"

	assert.Equal(t, expectedSql, sql)

	expectedArgs := []interface{}{
		true, "it's true!",
		0, 1, 2,
	}
	assert.Equal(t, expectedArgs, args)
}

func TestCaseWithNoWhenClause(t *testing.T) {
	caseStmt := Case("something").
		Else("42")

	qb := Select().Column(caseStmt).From("table")

	_, _, err := qb.ToSql()

	assert.Error(t, err)

	assert.Equal(t, "case expression must contain at lease one WHEN clause", err.Error())
}

func TestCaseBuilderMustSql(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("TestCaseBuilderMustSql should have panicked!")
		}
	}()
	Case("").MustSql()
}

func TestCaseWithNestedExprs(t *testing.T) {
	caseStmt := Case().
		When(Expr("y = ?", 99), Expr("CONCAT('a=', ?)", 1)).
		When(Expr("z > ?", 2), Expr("?", 3))

	sql, args, err := caseStmt.ToSql()

	assert.NoError(t, err)

	expectedSql := "CASE " +
		"WHEN y = ? THEN CONCAT('a=', ?) " +
		"WHEN z > ? THEN ? " +
		"END"
	assert.Equal(t, expectedSql, sql)

	expectedArgs := []interface{}{99, 1, 2, 3}
	assert.Equal(t, expectedArgs, args)

	expectedDollarSql := "CASE " +
		"WHEN y = $1 THEN CONCAT('a=', $2) " +
		"WHEN z > $3 THEN $4 " +
		"END"
	dollarSql, err := Dollar.ReplacePlaceholders(sql)
	assert.NoError(t, err)
	assert.Equal(t, expectedDollarSql, dollarSql)
}

func TestCaseWithEmptyBranch(t *testing.T) {
	testCases := []struct {
		name        string
		caseStmt    CaseBuilder
		expectedErr string
	}{
		{
			name:        "nil WHEN",
			caseStmt:    Case().When(nil, "thenVal"),
			expectedErr: "case WHEN #1 must not be empty",
		},
		{
			name:        "empty string WHEN",
			caseStmt:    Case().When("", "thenVal"),
			expectedErr: "case WHEN #1 must not be empty",
		},
		{
			name:        "nil THEN",
			caseStmt:    Case().When("whenVal", nil),
			expectedErr: "case THEN #1 must not be empty",
		},
		{
			name:        "empty THEN on second branch",
			caseStmt:    Case().When("a", "one").When("b", ""),
			expectedErr: "case THEN #2 must not be empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := tc.caseStmt.ToSql()

			assert.Error(t, err)
			assert.Equal(t, tc.expectedErr, err.Error())
			assert.Empty(t, sql)
			assert.Nil(t, args)
		})
	}
}

func TestCaseWithInvalidBranchArg(t *testing.T) {
	caseStmt := Case().
		When("validWhen", "validThen").
		When(123, 456)

	sql, args, err := caseStmt.ToSql()

	assert.Error(t, err)
	assert.Equal(t, "case WHEN #2: expected string or Sqlizer, not int", err.Error())
	assert.Empty(t, sql)
	assert.Nil(t, args)

	valid := Case().When("validWhen", "validThen")
	validSql, validArgs, validErr := valid.ToSql()
	assert.NoError(t, validErr)
	assert.Equal(t, "CASE WHEN validWhen THEN validThen END", validSql)
	assert.Nil(t, validArgs)
}
