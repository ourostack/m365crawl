//go:build acceptance

package acceptance

import "testing"

func TestOWARawRootAnchorCriteria(t *testing.T) {
	for _, tc := range []struct {
		name                string
		folders             map[string]owaRawFolder
		messageFolder, root string
	}{
		{"anchor_unused", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "I": {parent: "A", kind: "inbox"}, "X": {parent: "Other", kind: "other"}}, "I", "Primary"},
		{"legacy", map[string]owaRawFolder{"I": {parent: "Primary", kind: "inbox"}}, "I", "Primary"},
		{"no_anchor_multiple", map[string]owaRawFolder{"I": {parent: "Primary"}, "X": {parent: "Other"}}, "I", ""},
		{"two", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "B": {parent: "A", distinguished: "msgfolderroot"}}, "A", ""},
		{"foreign_message", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "X": {parent: "Other"}}, "X", ""},
		{"same_component_not_descent", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "X": {parent: "Primary"}}, "X", ""},
		{"foreign_inbox", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "I": {parent: "Other", kind: "inbox"}}, "A", ""},
		{"foreign_sent", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "S": {parent: "Other", kind: "sent"}}, "A", ""},
		{"cycle", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}, "X": {parent: "X"}}, "A", ""},
		{"missing_parent", map[string]owaRawFolder{"A": {distinguished: "msgfolderroot"}}, "A", ""},
		{"unknown_message", map[string]owaRawFolder{"A": {parent: "Primary", distinguished: "msgfolderroot"}}, "Missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := owaRawCensus{folders: tc.folders, messages: map[string]owaRawMessage{"M": {folder: tc.messageFolder}}}
			root, err := c.root()
			if tc.root != "" {
				if err != nil || root != tc.root {
					t.Fatal("raw_anchor_root_mismatch")
				}
			} else if err == nil {
				t.Fatal("raw_ambiguous_anchor_accepted")
			}
		})
	}
}
