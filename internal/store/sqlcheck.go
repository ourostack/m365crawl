package store

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrQueryRefused marks a query the sql command refuses before running it (a statement that is
// not a read, or more than one statement). Errors from SQLite itself do not wrap it.
var ErrQueryRefused = errors.New("query refused")

func refused(msg string) error { return fmt.Errorf("%w: %s", ErrQueryRefused, msg) }

// CheckSQL is the friendly early check for the sql command: one statement, and a read (SELECT,
// WITH, EXPLAIN or VALUES). It reads the text as SQL tokens, so comments, string literals and
// quoted identifiers that contain words such as attach or a semicolon are never mistaken for
// statements. It is not the security boundary: the archive is opened read-only at the file level
// (mode=ro with query_only), which stops every write whatever this check lets through.
func CheckSQL(q string) error {
	toks := sqlTokens(q)
	if len(toks) == 0 {
		return refused(onlyReads)
	}
	switch strings.ToLower(toks[0].word) {
	case "select", "with", "explain", "values":
	default:
		return refused(onlyReads)
	}
	for i, t := range toks {
		if t.semi && i+1 < len(toks) {
			return refused("sql takes a single statement")
		}
	}
	return nil
}

const onlyReads = "sql accepts only read statements (SELECT, WITH, EXPLAIN or VALUES)"

// sqlToken is a word (a keyword or bare identifier), a semicolon, or anything else.
type sqlToken struct {
	word string
	semi bool
}

// sqlTokens splits q into tokens, skipping whitespace and comments and treating each quoted string
// or identifier as one opaque token.
func sqlTokens(q string) []sqlToken {
	rs := []rune(q)
	var out []sqlToken
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			i += 2
			for i < len(rs) && (rs[i] != '*' || i+1 >= len(rs) || rs[i+1] != '/') {
				i++
			}
			i += 2
		case r == ';':
			out = append(out, sqlToken{semi: true})
			i++
		case r == '\'' || r == '"' || r == '`' || r == '[':
			end := r
			if r == '[' {
				end = ']'
			}
			i++
			for i < len(rs) {
				if rs[i] == end {
					if end != ']' && i+1 < len(rs) && rs[i+1] == end {
						i += 2 // a doubled quote is a literal quote
						continue
					}
					break
				}
				i++
			}
			i++
			out = append(out, sqlToken{})
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			out = append(out, sqlToken{word: string(rs[i:j])})
			i = j
		default:
			out = append(out, sqlToken{})
			i++
		}
	}
	return out
}
