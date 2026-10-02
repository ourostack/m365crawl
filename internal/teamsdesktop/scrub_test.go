package teamsdesktop

import "testing"

func TestScrub(t *testing.T) {
	const jwt = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln"
	for _, c := range []struct {
		name, in, want string
		n              int
	}{
		{"plain", `{"a":"b","n":1}`, `{"a":"b","n":1}`, 0},
		{"jwt in text", `{"a":"Bearer ` + jwt + ` x"}`, `{"a":"Bearer [redacted] x"}`, 1},
		{"jwt without signature", `"eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."`, `"[redacted]"`, 1},
		{"keyed values", `{"access_token":"abc","Refresh_Token":"d\"e","id_token":"x","authorization":"y","client_secret":"z","password":"p","ok":"v"}`,
			`{"access_token":"[redacted]","Refresh_Token":"[redacted]","id_token":"[redacted]","authorization":"[redacted]","client_secret":"[redacted]","password":"[redacted]","ok":"v"}`, 6},
		{"non-string keyed value untouched", `{"password":null,"access_token":{"a":1}}`, `{"password":null,"access_token":{"a":1}}`, 0},
		{"sig", `"https://x.blob/y?sv=1&sig=AbC%2Fd&se=2"`, `"https://x.blob/y?sv=1&sig=[redacted]&se=2"`, 1},
		{"sig at end", `"https://x/y?sig=q"`, `"https://x/y?sig=[redacted]"`, 1},
		{"not a sig param", `"https://x/y?design=q&xsig=1"`, `"https://x/y?design=q&xsig=1"`, 0},
		{"mixed", `{"access_token":"` + jwt + `","u":"?sig=1","j":"` + jwt + `"}`, `{"access_token":"[redacted]","u":"?sig=[redacted]","j":"[redacted]"}`, 3},
	} {
		got, n := Scrub([]byte(c.in))
		if string(got) != c.want || n != c.n {
			t.Errorf("%s: got %s (%d) want %s (%d)", c.name, got, n, c.want, c.n)
		}
	}
}
