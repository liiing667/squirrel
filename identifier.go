package squirrel

import (
	"fmt"
	"strings"
)

// InvalidIdentifierError describes why an identifier (table, column, schema or
// alias name) was rejected by ValidateIdentifier or QuoteIdentifier.
type InvalidIdentifierError struct {
	Name   string
	Reason string
}

func (e *InvalidIdentifierError) Error() string {
	return fmt.Sprintf("squirrel: invalid identifier %q: %s", e.Name, e.Reason)
}

func invalidIdentifier(name, reason string) error {
	return &InvalidIdentifierError{Name: name, Reason: reason}
}

// sqlReservedWords is a case-insensitive set of common SQL reserved words
// (ANSI SQL plus widespread MySQL/PostgreSQL/SQLite keywords). Identifiers
// matching these words are still valid, but must be quoted before being
// embedded in SQL; QuoteIdentifier takes care of that.
var sqlReservedWords = map[string]struct{}{
	"ALL": {}, "ALTER": {}, "AND": {}, "ANY": {}, "AS": {}, "ASC": {},
	"BETWEEN": {}, "BY": {}, "CASE": {}, "CAST": {}, "CHECK": {},
	"COLLATE": {}, "COLUMN": {}, "CONSTRAINT": {}, "CREATE": {},
	"CROSS": {}, "CURRENT_DATE": {}, "CURRENT_TIME": {},
	"CURRENT_TIMESTAMP": {}, "CURRENT_USER": {}, "DATABASE": {},
	"DEFAULT": {}, "DELETE": {}, "DESC": {}, "DISTINCT": {}, "DROP": {},
	"ELSE": {}, "END": {}, "ESCAPE": {}, "EXCEPT": {}, "EXISTS": {},
	"FALSE": {}, "FETCH": {}, "FOR": {}, "FOREIGN": {}, "FROM": {},
	"FULL": {}, "GRANT": {}, "GROUP": {}, "HAVING": {}, "IN": {},
	"INDEX": {}, "INNER": {}, "INSERT": {}, "INTERSECT": {}, "INTO": {},
	"IS": {}, "JOIN": {}, "KEY": {}, "LEFT": {}, "LIKE": {}, "LIMIT": {},
	"NATURAL": {}, "NOT": {}, "NULL": {}, "OFFSET": {}, "ON": {},
	"OR": {}, "ORDER": {}, "OUTER": {}, "PRIMARY": {}, "REFERENCES": {},
	"RETURNING": {}, "RIGHT": {}, "SELECT": {}, "SET": {}, "TABLE": {},
	"THEN": {}, "TO": {}, "TRUE": {}, "UNION": {}, "UNIQUE": {},
	"UPDATE": {}, "USER": {}, "USING": {}, "VALUES": {}, "VIEW": {},
	"WHEN": {}, "WHERE": {}, "WITH": {},
}

// IsReservedWord reports whether name (case-insensitively) is a common SQL
// reserved word. Reserved words are usable as identifiers only when quoted.
func IsReservedWord(name string) bool {
	_, ok := sqlReservedWords[strings.ToUpper(name)]
	return ok
}

func isIdentifierStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentifierChar(c byte) bool {
	return isIdentifierStart(c) || (c >= '0' && c <= '9') || c == '$'
}

// ValidateIdentifier checks that name is a safe SQL identifier, optionally
// qualified with dot-separated parts (e.g. "schema.table" or
// "schema.table.column"). Each part must start with a letter or underscore
// and contain only letters, digits, underscores and dollar signs. A single
// "*" part is allowed to support "table.*" selections. Empty parts, NUL or
// control characters, quotes, backticks, whitespace and any other bytes are
// rejected, so a validated identifier cannot break out of its syntactic
// position or alter the structure of the surrounding query.
//
// Reserved words are NOT rejected: they are legal identifiers as long as
// they are quoted, which QuoteIdentifier does. Use IsReservedWord to detect
// them explicitly.
func ValidateIdentifier(name string) error {
	if name == "" {
		return invalidIdentifier(name, "identifier is empty")
	}
	parts := strings.Split(name, ".")
	for _, part := range parts {
		if err := validateIdentifierPart(name, part); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentifierPart(name, part string) error {
	if part == "*" {
		return nil
	}
	if part == "" {
		return invalidIdentifier(name, "identifier contains an empty part")
	}
	for i := 0; i < len(part); i++ {
		c := part[i]
		if c < 0x20 || c == 0x7f {
			return invalidIdentifier(name, "identifier contains a NUL or control character")
		}
		if c >= 0x80 {
			return invalidIdentifier(name, "identifier contains a non-ASCII character")
		}
		if i == 0 {
			if !isIdentifierStart(c) {
				return invalidIdentifier(name, "identifier part must start with a letter or underscore")
			}
		} else if !isIdentifierChar(c) {
			return invalidIdentifier(name, fmt.Sprintf("identifier contains a disallowed character %q", rune(c)))
		}
	}
	return nil
}

// QuoteIdentifier validates name with ValidateIdentifier and returns it with
// every dot-separated part double-quoted (ANSI SQL), so reserved words and
// mixed-case names are safe to embed in a query. A "*" part is left
// unquoted. If name is invalid an *InvalidIdentifierError is returned and
// the caller must not fall back to embedding the raw name in SQL.
//
// Ex:
//
//	QuoteIdentifier("users")        => `"users"`, nil
//	QuoteIdentifier("public.users") => `"public"."users"`, nil
//	QuoteIdentifier("order")        => `"order"`, nil
//	QuoteIdentifier(`u"; DROP`)     => "", error
func QuoteIdentifier(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	parts := strings.Split(name, ".")
	for i, part := range parts {
		if part != "*" {
			parts[i] = `"` + part + `"`
		}
	}
	return strings.Join(parts, "."), nil
}

// MustQuoteIdentifier is like QuoteIdentifier but panics on invalid input.
// It is convenient for package-level or otherwise statically known
// identifiers; do not use it with untrusted input.
func MustQuoteIdentifier(name string) string {
	quoted, err := QuoteIdentifier(name)
	if err != nil {
		panic(err)
	}
	return quoted
}

// safeIdentifier is a Sqlizer that quotes a validated identifier when the
// SQL is generated, surfacing validation errors from ToSql instead of
// silently emitting unsafe SQL.
type safeIdentifier struct {
	name string
}

// SafeIdentifier returns a Sqlizer that renders name as a safely quoted
// identifier (see QuoteIdentifier). It can be used anywhere a Sqlizer is
// accepted, e.g. inside Expr or ConcatExpr, and carries no arguments so it
// never affects placeholder ordering:
//
//	sq.Expr("SELECT * FROM ? WHERE id = ?", squirrel.SafeIdentifier(table), id)
//
// If name is invalid, ToSql returns an *InvalidIdentifierError.
func SafeIdentifier(name string) Sqlizer {
	return safeIdentifier{name: name}
}

func (s safeIdentifier) ToSql() (string, []interface{}, error) {
	quoted, err := QuoteIdentifier(s.name)
	if err != nil {
		return "", nil, err
	}
	return quoted, nil, nil
}
