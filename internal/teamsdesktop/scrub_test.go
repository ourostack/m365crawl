package teamsdesktop

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestScrub(t *testing.T) {
	const jwt = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln"
	for _, c := range []struct {
		name, in, want string
		n              int
	}{
		{"plain", `{"a":"b","n":1}`, `{"a":"b","n":1}`, 0},
		{"jwt in text", `{"a":"see ` + jwt + ` x"}`, `{"a":"see [redacted] x"}`, 1},
		{"jwt without signature", `"eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."`, `"[redacted]"`, 1},
		{"keyed values", `{"access_token":"abc","Refresh_Token":"d\"e","id_token":"x","authorization":"y","client_secret":"z","password":"p","ok":"v"}`,
			`{"access_token":"[redacted]","Refresh_Token":"[redacted]","id_token":"[redacted]","authorization":"[redacted]","client_secret":"[redacted]","password":"[redacted]","ok":"v"}`, 6},
		{"non-string keyed values", `{"password":null,"access_token":{"a":[1,"}"]},"id_token":[1,2],"refresh_token":12.5,"authorization":true,"ok":1}`,
			`{"password":"[redacted]","access_token":"[redacted]","id_token":"[redacted]","refresh_token":"[redacted]","authorization":"[redacted]","ok":1}`, 5},
		{"keyed name inside a string value is not a key", `{"a":"x \"password\":1 y"}`, `{"a":"x \"password\":1 y"}`, 0},
		{"map pair", `{"$map":[["Access_Token","abc"],["k",{"a":1}],["password",[1,2]],["id_token",7]]}`,
			`{"$map":[["Access_Token","[redacted]"],["k",{"a":1}],["password","[redacted]"],["id_token","[redacted]"]]}`, 3},
		{"escaped key name", `{"access\u005ftoken":1}`, `{"access\u005ftoken":"[redacted]"}`, 1},
		{"whitespace after key", `{"password" : 1,"access_token"	:  {"a":[1, 2]},"ok":2}`, `{"password" : "[redacted]","access_token"	:  "[redacted]","ok":2}`, 2},
		{"whitespace in map pair", `{"$map":[ ["password" , {"a":1}],["k","v"]]}`, `{"$map":[ ["password" , "[redacted]"],["k","v"]]}`, 1},
		{"bearer whitespace", `["bearer\tabc","Bearer\nabc","BEARER\u0020x","bearer\\tx"]`, `["[redacted]","[redacted]","[redacted]","bearer\\tx"]`, 3},
		{"sibling name", `{"name":"Access_Token","value":{"a":1},"x":1}`, `{"name":"Access_Token","value":"[redacted]","x":1}`, 1},
		{"sibling type after", `[{"value":"v","TYPE":"password"},{"key":"other","value":"v"},{"name":"id_token"}]`, `[{"value":"[redacted]","TYPE":"password"},{"key":"other","value":"v"},{"name":"id_token"}]`, 1},
		{"sibling nested", `{"a":{"key":"client_secret","Value":[1,{"b":2}]},"value":"top"}`, `{"a":{"key":"client_secret","Value":"[redacted]"},"value":"top"}`, 1},
		{"stringified json", `{"data":"{\"refresh_token\":\"0.AXYZopaque\"}","n":"{not json"}`, `{"data":"{\"refresh_token\":\"[redacted]\"}","n":"{not json"}`, 1},
		{"stringified array", `{"data":" [ {\"password\":1}, \"<&>\" ] "}`, `{"data":"[ {\"password\":\"[redacted]\"}, \"<&>\" ]"}`, 1},
		{"sig any case", `"?SIG=a&x=1&Sig=b"`, `"?SIG=[redacted]&x=1&Sig=[redacted]"`, 2},
		{"bearer string", `{"h":"bEARER abc.def","l":"bearer  x\"y","n":"not bearer x","m":"Bearers"}`,
			`{"h":"[redacted]","l":"[redacted]","n":"not bearer x","m":"Bearers"}`, 2},
		{"sig", `"https://x.blob/y?sv=1&sig=AbC%2Fd&se=2"`, `"https://x.blob/y?sv=1&sig=[redacted]&se=2"`, 1},
		{"sig at end", `"https://x/y?sig=q"`, `"https://x/y?sig=[redacted]"`, 1},
		{"not a sig param", `"https://x/y?design=q&xsig=1"`, `"https://x/y?design=q&xsig=1"`, 0},
		{"mixed", `{"access_token":"` + jwt + `","u":"?sig=1","j":"` + jwt + `"}`, `{"access_token":"[redacted]","u":"?sig=[redacted]","j":"[redacted]"}`, 3},
	} {
		got, n := Scrub([]byte(c.in))
		if string(got) != c.want || n != c.n {
			t.Errorf("%s: got %s (%d) want %s (%d)", c.name, got, n, c.want, c.n)
		}
		if !json.Valid(got) {
			t.Errorf("%s: output is not valid JSON: %s", c.name, got)
		}
	}
}

// Malformed input never happens for canonical JSON, but the scanner must stay in bounds.
func TestScrubMalformedInputStaysInBounds(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"bad\q":1}`, `{"bad\q":1}`},
		{`{"abc`, `{"abc`},
		{`{"password":`, `{"password":"[redacted]"`},
		{`{"password":{"a":[1`, `{"password":"[redacted]"`},
		{`{"password":"x`, `{"password":"[redacted]"`},
		{`"password"`, `"password"`},
	} {
		if got, _ := Scrub([]byte(c.in)); string(got) != c.want {
			t.Errorf("%s: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestScrubStringifiedNesting(t *testing.T) {
	wrap := func(inner string) string { b, _ := json.Marshal(inner); return string(b) }
	level0 := `{"refresh_token":"0.AXYZopaque","k":"v"}`
	level1 := `{"payload":` + wrap(level0) + `}`
	level2 := `{"data":` + wrap(level1) + `}`
	got, n := Scrub([]byte(level2))
	if n != 1 || !json.Valid(got) || bytes.Contains(got, []byte("AXYZ")) {
		t.Fatalf("two levels: %s (%d)", got, n)
	}
	var outer map[string]string
	if err := json.Unmarshal(got, &outer); err != nil {
		t.Fatal(err)
	}
	var mid map[string]string
	if err := json.Unmarshal([]byte(outer["data"]), &mid); err != nil || mid["payload"] != `{"refresh_token":"[redacted]","k":"v"}` {
		t.Fatalf("inner: %v %v", mid, err)
	}
	// Past the depth bound the text is left as is (and stays valid JSON).
	deep := level0
	for i := 0; i < 7; i++ {
		deep = `{"d":` + wrap(deep) + `}`
	}
	got, _ = Scrub([]byte(deep))
	if !json.Valid(got) {
		t.Fatalf("deep: %s", got)
	}
}
