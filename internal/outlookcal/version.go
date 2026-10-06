package outlookcal

// VersionRule says which copy of an event is the current one when the store holds
// several. The rule is likely, not established (docs/outlook-store.md, "Versions of
// one event"); it is a named constant so a different finding changes one value.
type VersionRule string

const (
	// RuleLastModified prefers the highest last-modified (+288), then the highest
	// change stamp (+112), then the later file position. The file position is only the
	// final tie-break, so the result is deterministic and never decided by order alone.
	RuleLastModified VersionRule = "last_modified,stamp,file_order"
	// RuleStamp prefers the highest change stamp first. It is the alternative the plan
	// kept for the case an edit experiment shows the stamp orders versions.
	RuleStamp VersionRule = "stamp,last_modified,file_order"
)

// CurrentVersionRule is the rule Collect uses unless told otherwise.
const CurrentVersionRule = RuleLastModified

// copyKey is what the rule compares.
type copyKey struct {
	lastMod, stamp uint64
	block          int64
	pos            int
}

// newer reports whether a is a better (more current) copy than b under the rule. Any
// rule other than RuleStamp is RuleLastModified.
func (r VersionRule) newer(a, b copyKey) bool {
	first, second := [2]uint64{a.lastMod, a.stamp}, [2]uint64{b.lastMod, b.stamp}
	if r == RuleStamp {
		first, second = [2]uint64{a.stamp, a.lastMod}, [2]uint64{b.stamp, b.lastMod}
	}
	for i := range first {
		if first[i] != second[i] {
			return first[i] > second[i]
		}
	}
	if a.block != b.block {
		return a.block > b.block
	}
	return a.pos > b.pos
}
