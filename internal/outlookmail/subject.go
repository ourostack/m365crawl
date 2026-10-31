package outlookmail

import "strings"

// NormalizeSubject strips repeated native reply/forward prefixes and surrounding whitespace.
func NormalizeSubject(s string) string {
	s = strings.TrimSpace(s)
	for {
		n := stripSubjectPrefix(s)
		if n == s {
			return s
		}
		s = n
	}
}

var subjectPrefixes = []string{"re", "fwd", "fw", "aw", "wg", "sv"}

func stripSubjectPrefix(s string) string {
	for _, p := range subjectPrefixes {
		if len(s) < len(p) || !strings.EqualFold(s[:len(p)], p) {
			continue
		}
		if rest := strings.TrimLeft(s[len(p):], " \t"); strings.HasPrefix(rest, ":") {
			return strings.TrimSpace(rest[1:])
		}
	}
	return s
}
