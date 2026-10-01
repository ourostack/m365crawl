package teamsdesktop

import (
	"testing"
	"time"
)

func TestReactionActor(t *testing.T) {
	at := time.UnixMilli(1_000_000)
	const self = "8:orgid:me"
	cases := []struct {
		name string
		raw  string
		key  string
		want string
	}{
		{"nearest reaction of the key wins", `{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:far","time":1}, {"mri":"8:orgid:near","time":999000}]}]}}`, "like", "8:orgid:near"},
		{"key is matched in any case", `{"properties":{"emotions":[{"key":"Like","users":[{"mri":"8:orgid:a","time":5}]}]}}`, "like", "8:orgid:a"},
		{"other keys are ignored", `{"properties":{"emotions":[{"key":"heart","users":[{"mri":"8:orgid:a","time":1000000}]},{"key":"like","users":[{"mri":"8:orgid:b","time":1}]}]}}`, "like", "8:orgid:b"},
		{"the account's own reaction never counts", `{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:me","time":1000000},{"mri":"8:orgid:b","time":1}]}]}}`, "like", "8:orgid:b"},
		{"only the account reacted", `{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:me","time":1}]}]}}`, "like", ""},
		{"emotions stored as a JSON string", `{"properties":{"emotions":"[{\"key\":\"like\",\"users\":[{\"mri\":\"8:orgid:s\",\"time\":1000001}]}]"}}`, "like", "8:orgid:s"},
		{"delta emotions are read when there are no emotions", `{"properties":{"deltaEmotions":[{"key":"like","users":[{"mri":"8:orgid:d","time":1}]}]}}`, "like", "8:orgid:d"},
		{"a user without a time loses to one with a time", `{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:none"},{"mri":"8:orgid:t","time":5}]}]}}`, "like", "8:orgid:t"},
		{"a user without a time is still found alone", `{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:none"}]}]}}`, "like", "8:orgid:none"},
		{"a user without an mri is skipped", `{"properties":{"emotions":[{"key":"like","users":[{"time":5}]}]}}`, "like", ""},
		{"no emotions", `{"properties":{}}`, "like", ""},
		{"no key", `{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:a","time":5}]}]}}`, "", ""},
		{"not JSON", `nope`, "like", ""},
		{"empty raw", ``, "like", ""},
		{"emotions string that is not JSON", `{"properties":{"emotions":"zzz"}}`, "like", ""},
	}
	for _, c := range cases {
		if got := ReactionActor([]byte(c.raw), c.key, at, self); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
