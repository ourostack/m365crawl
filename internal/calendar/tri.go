package calendar

import (
	"database/sql"
	"fmt"
)

// Tri is a flag that may be unknown. The zero value is unknown, so a mapper that forgets to set a
// flag reports "not known", never a wrong "false". Capture and Merge never let an unknown flag
// replace a known one.
type Tri int8

// The three values of a Tri.
const (
	TriUnknown Tri = 0
	TriFalse   Tri = 1
	TriTrue    Tri = 2
)

// TriOf is the known flag for b.
func TriOf(b bool) Tri {
	if b {
		return TriTrue
	}
	return TriFalse
}

// Known reports whether the source said anything about the flag.
func (t Tri) Known() bool { return t != TriUnknown }

// Is reports whether the flag is known and equal to b.
func (t Tri) Is(b bool) bool { return t == TriOf(b) }

// String names the value, for messages.
func (t Tri) String() string {
	switch t {
	case TriFalse:
		return "false"
	case TriTrue:
		return "true"
	}
	return "unknown"
}

// triArg is the SQL value of t: NULL for unknown, 0 for false, 1 for true.
func triArg(t Tri) any {
	switch t {
	case TriFalse:
		return 0
	case TriTrue:
		return 1
	}
	return nil
}

// triFromSQL reads a stored flag: NULL is unknown, 0 false, 1 true; anything else is damage.
func triFromSQL(n sql.NullInt64) (Tri, error) {
	switch {
	case !n.Valid:
		return TriUnknown, nil
	case n.Int64 == 0:
		return TriFalse, nil
	case n.Int64 == 1:
		return TriTrue, nil
	}
	return TriUnknown, fmt.Errorf("calendar: stored flag %d, want NULL, 0 or 1", n.Int64)
}
