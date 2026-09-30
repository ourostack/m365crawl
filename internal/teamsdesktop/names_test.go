package teamsdesktop

import "testing"

func TestParseDatabaseName(t *testing.T) {
	const tenant = "00000000-0000-4000-8000-000000000001"
	const user = "00000000-0000-4000-8000-0000000000a1"
	cases := []struct {
		name    string
		manager string
		acct    Account
		ok      bool
	}{
		{"Teams:replychain-manager:react-web-client:" + tenant + ":" + user + ":en-us", "replychain-manager", Account{tenant, user, "en-us"}, true},
		{"Teams:conversation-manager:react-web-client:" + tenant + ":8:orgid:" + user + ":en-us", "conversation-manager", Account{tenant, user, "en-us"}, true},
		{"Teams:auth:react-web-client:" + tenant + ":" + user + ":en-us", "auth", Account{tenant, user, "en-us"}, true},
		{"Teams:activity-manager:react-web-client:" + tenant + ":" + user + ":pt-br", "activity-manager", Account{tenant, user, "pt-br"}, true},
		{"Other:replychain-manager:react-web-client:" + tenant + ":" + user + ":en-us", "", Account{}, false},
		{"Teams:replychain-manager:something-else:" + tenant + ":" + user + ":en-us", "", Account{}, false},
		{"Teams:replychain-manager:react-web-client:" + tenant + ":en-us", "", Account{}, false},
		{"Teams:replychain-manager:react-web-client:::", "", Account{}, false},
		{"", "", Account{}, false},
		{"pure-noise", "", Account{}, false},
	}
	for _, c := range cases {
		m, a, ok := ParseDatabaseName(c.name)
		if ok != c.ok || m != c.manager || a != c.acct {
			t.Errorf("%q: got (%q, %+v, %v), want (%q, %+v, %v)", c.name, m, a, ok, c.manager, c.acct, c.ok)
		}
	}
}
