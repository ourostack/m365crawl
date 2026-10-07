package outlookmail

import (
	"net/http"
	"testing"
)

func TestHTMLText(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain":                   {"hello", "hello"},
		"tags dropped":            {"<p>Hello <b>big</b> world</p>", "Hello big world"},
		"inline tag no gap":       {"a<b>b</b>c", "abc"},
		"block tags gap":          {"<p>one</p><p>two</p><div>three</div>line<br>four<br/>five", "one two three line four five"},
		"script dropped":          {"a<script>var x = '<p>secret</p>';</script>b", "a b"},
		"script with attrs":       {"a<SCRIPT type=text/javascript>1 < 2</SCRIPT >b", "a b"},
		"style dropped":           {"<style>p { color: red }</style><p>x</p>", "x"},
		"script close unfinished": {"keep<script>x</script", "keep"},
		"stray script close":      {"a</script>b", "a b"},
		"unclosed script":         {"keep<script>never closed", "keep"},
		"comment dropped":         {"a<!-- hidden <p>x</p> -->b", "ab"},
		"unclosed comment":        {"a<!-- never closed", "a"},
		"doctype dropped":         {"<!DOCTYPE html><html><head><title>T</title></head><body>Body</body></html>", "T Body"},
		"entities":                {"Fish &amp; chips &lt;3 &nbsp;&#65;&#x42; &copy;", "Fish & chips <3 AB ©"},
		"whitespace":              {"  a \r\n\t b  c  ", "a b c"},
		"lone less-than":          {"1 < 2 and 3 <4", "1 < 2 and 3 <4"},
		"quoted gt":               {`<a title="x > y" href='a>b'>link</a>`, "link"},
		"unclosed tag":            {"text <p class=", "text"},
		"unclosed quote":          {`text <a title="x`, "text"},
		"processing":              {"<?xml version=\"1.0\"?>a", "a"},
		"image not fetched":       {`<img src="http://example.invalid/pixel.png">text`, "text"},
		"closing tag only":        {"a</p>b", "a b"},
		"empty":                   {"", ""},
		"upper case tags":         {"<P>one</P><BR>two", "one two"},
		"invalid utf-8":           {"a\xffb", "a�b"},
		"script close case":       {"<ScRiPt>x</sCrIpT>after", "after"},
	} {
		if got := HTMLText([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// HTMLText takes bytes and no client: nothing in it can fetch. The default transport is replaced
// by one that fails the test, as a second guard.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("HTMLText used the network")
	return nil, http.ErrNotSupported
}

func TestHTMLTextDoesNotUseTheNetwork(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	defer func() { http.DefaultTransport = old }()
	_ = HTMLText([]byte(`<img src="http://example.invalid/a.png"><link href="http://example.invalid/s.css"><iframe src="http://example.invalid/"></iframe>x`))
}
