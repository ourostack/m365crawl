package teamsdesktop

import (
	"crypto/sha256"
	"fmt"
	"reflect"
)

// RulesVersion numbers the code that applies the denylist (deny.go) and the scrub rules
// (scrub.go). Raise it in the same change that alters how either one works. A change to a rule
// table needs no raise: RulesStamp covers the tables itself.
const RulesVersion = 1

// RulesStamp identifies the denylist and scrub rules in force: RulesVersion and a digest of every
// rule table. The archive remembers the stamp the calendar tables were derived under, and a
// different stamp makes the next sync rebuild them from the archived records under the current
// rules, so a derived copy made under an older rule does not outlive it.
func RulesStamp() string {
	h := sha256.New()
	hashValue(h, "rules", reflect.ValueOf(rules))
	return fmt.Sprintf("%d.%x", RulesVersion, h.Sum(nil)[:6])
}
