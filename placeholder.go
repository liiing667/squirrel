package squirrel

import (
	"bytes"
	"fmt"
	"strings"
)

// PlaceholderFormat is the interface that wraps the ReplacePlaceholders method.
//
// ReplacePlaceholders takes a SQL statement and replaces each question mark
// placeholder with a (possibly different) SQL placeholder.
type PlaceholderFormat interface {
	ReplacePlaceholders(sql string) (string, error)
}

type placeholderDebugger interface {
	debugPlaceholder() string
}

var (
	// Question is a PlaceholderFormat instance that leaves placeholders as
	// question marks.
	Question = questionFormat{}

	// Dollar is a PlaceholderFormat instance that replaces placeholders with
	// dollar-prefixed positional placeholders (e.g. $1, $2, $3).
	Dollar = dollarFormat{}

	// Colon is a PlaceholderFormat instance that replaces placeholders with
	// colon-prefixed positional placeholders (e.g. :1, :2, :3).
	Colon = colonFormat{}

	// AtP is a PlaceholderFormat instance that replaces placeholders with
	// "@p"-prefixed positional placeholders (e.g. @p1, @p2, @p3).
	AtP = atpFormat{}
)

type questionFormat struct{}

func (questionFormat) ReplacePlaceholders(sql string) (string, error) {
	return sql, nil
}

func (questionFormat) debugPlaceholder() string {
	return "?"
}

type dollarFormat struct{}

func (dollarFormat) ReplacePlaceholders(sql string) (string, error) {
	return replacePositionalPlaceholders(sql, "$")
}

func (dollarFormat) debugPlaceholder() string {
	return "$"
}

type colonFormat struct{}

func (colonFormat) ReplacePlaceholders(sql string) (string, error) {
	return replacePositionalPlaceholders(sql, ":")
}

func (colonFormat) debugPlaceholder() string {
	return ":"
}

type atpFormat struct{}

func (atpFormat) ReplacePlaceholders(sql string) (string, error) {
	return replacePositionalPlaceholders(sql, "@p")
}

func (atpFormat) debugPlaceholder() string {
	return "@p"
}

// Placeholders returns a string with count ? placeholders joined with commas.
func Placeholders(count int) string {
	if count < 1 {
		return ""
	}

	return strings.Repeat(",?", count)[1:]
}

func replacePositionalPlaceholders(sql, prefix string) (string, error) {
	buf := &bytes.Buffer{}
	i := 0
	for len(sql) > 0 {
		p, err := findPlaceholder(sql)
		if err != nil {
			return "", err
		}
		if p == -1 {
			break
		}

		buf.WriteString(sql[:p])
		if p+1 < len(sql) && sql[p+1] == '?' { // escape ?? => ?
			buf.WriteString("?")
			sql = sql[p+2:]
		} else {
			i++
			fmt.Fprintf(buf, "%s%d", prefix, i)
			sql = sql[p+1:]
		}
	}

	buf.WriteString(sql)
	return buf.String(), nil
}

// findPlaceholder returns the byte offset of the first '?' in sql that acts
// as a bind placeholder (or as the start of a "??" escape sequence), or -1
// if there is none.
//
// Question marks inside string literals, quoted identifiers, comments, and
// dollar-quoted strings are not placeholders and are skipped, as are the
// PostgreSQL "?|" and "?&" operators. An error is returned when a literal,
// comment, or dollar-quoted string is left unterminated, because the
// placeholder boundaries cannot be determined reliably in that case.
//
// findPlaceholder is the single source of truth for placeholder boundaries:
// it is shared by ReplacePlaceholders, nested Sqlizer expansion in expr.go,
// and DebugSqlizer, so all of them agree on which question marks bind args.
func findPlaceholder(sql string) (int, error) {
	for i := 0; i < len(sql); {
		switch sql[i] {
		case '?':
			if i+1 < len(sql) && (sql[i+1] == '|' || sql[i+1] == '&') {
				// PostgreSQL "?|" and "?&" operators, not placeholders.
				i += 2
				continue
			}
			return i, nil
		case '\'', '"', '`':
			j, err := skipQuoted(sql, i)
			if err != nil {
				return -1, err
			}
			i = j
		case '-':
			if i+1 < len(sql) && sql[i+1] == '-' {
				nl := strings.IndexByte(sql[i:], '\n')
				if nl == -1 {
					// Comment runs to the end of the input.
					return -1, nil
				}
				i += nl + 1
			} else {
				i++
			}
		case '/':
			if i+1 < len(sql) && sql[i+1] == '*' {
				j, err := skipBlockComment(sql, i)
				if err != nil {
					return -1, err
				}
				i = j
			} else {
				i++
			}
		case '$':
			j, ok, err := skipDollarQuote(sql, i)
			if err != nil {
				return -1, err
			}
			if ok {
				i = j
			} else {
				i++
			}
		default:
			i++
		}
	}
	return -1, nil
}

// skipQuoted returns the offset just past the quoted string literal or
// quoted identifier that opens at sql[start]. A doubled quote character
// inside the literal is an escaped quote, not the end of it.
func skipQuoted(sql string, start int) (int, error) {
	quote := sql[start]
	for i := start + 1; i < len(sql); i++ {
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i++ // escaped quote; skip both
				continue
			}
			return i + 1, nil
		}
	}
	return -1, fmt.Errorf(
		"squirrel: unterminated %c-quoted literal starting at byte %d of %q",
		quote, start, sql)
}

// skipBlockComment returns the offset just past the block comment that opens
// at sql[start]. Nested block comments are supported, as in PostgreSQL.
func skipBlockComment(sql string, start int) (int, error) {
	depth := 0
	for i := start; i+1 < len(sql); i++ {
		switch {
		case sql[i] == '/' && sql[i+1] == '*':
			depth++
			i++
		case sql[i] == '*' && sql[i+1] == '/':
			depth--
			i++
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return -1, fmt.Errorf(
		"squirrel: unterminated block comment starting at byte %d of %q",
		start, sql)
}

// skipDollarQuote reports whether sql[start:] opens a PostgreSQL
// dollar-quoted string ("$$...$$" or "$tag$...$tag$") and, if so, returns
// the offset just past its closing tag. ok is false when the '$' does not
// open a dollar-quoted string (e.g. a "$1" positional placeholder), in
// which case the caller should treat it as an ordinary character.
func skipDollarQuote(sql string, start int) (end int, ok bool, err error) {
	i := start + 1
	if i < len(sql) && isIdentStart(sql[i]) {
		for i < len(sql) && isIdentChar(sql[i]) {
			i++
		}
	}
	if i >= len(sql) || sql[i] != '$' {
		return 0, false, nil
	}
	tag := sql[start : i+1]
	j := strings.Index(sql[i+1:], tag)
	if j == -1 {
		return -1, false, fmt.Errorf(
			"squirrel: unterminated dollar-quoted string opened by %q at byte %d of %q",
			tag, start, sql)
	}
	return i + 1 + j + len(tag), true, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
