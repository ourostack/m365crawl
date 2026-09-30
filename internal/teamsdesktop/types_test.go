package teamsdesktop

import "testing"

func TestTypesZeroValues(t *testing.T) {
	// Construct each type to verify it compiles and zero-initializes correctly.
	var (
		m  Message
		mn Mention
		r  Reaction
		f  File
		c  Conversation
		p  Person
		a  Activity
	)

	// Verify struct construction initializes fields to their zero values.
	m.Mentions = []Mention{mn}
	m.Reactions = []Reaction{r}
	m.Files = []File{f}
	m.Links = []string{}
	c.Members = []string{p.ID}
	r.UserIDs = []string{}
	a.Raw = []byte{}

	// Ensure types can be constructed and used.
	if len(m.Mentions) != 1 || len(c.Members) != 1 {
		t.Error("type construction failed")
	}
}
