package teamsdesktop

import "testing"

func TestHTMLToText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain text", "hello world", "hello world"},
		{"paragraph", "<p>Hello from Alex</p>", "Hello from Alex"},
		{"two paragraphs", "<p>one</p><p>two</p>", "one\ntwo"},
		{"br", "a<br>b<br/>c<br />d", "a\nb\nc\nd"},
		{"double br keeps the blank line", "a<br><br>b", "a\n\nb"},
		{"div and li", "<div>a</div><ul><li>b</li><li>c</li></ul>", "a\nb\nc"},
		{"named entities", "<p>Fish &amp; chips &lt;3 &quot;quoted&quot; &#39;single&#39; &nbsp;done</p>", `Fish & chips <3 "quoted" 'single' done`},
		{"numeric entities", "<p>&#x1F600; &#128512; &copy;</p>", "\U0001F600 \U0001F600 ©"},
		{"at mention keeps display text", `<p><at id="0">Pat Example</at> please review</p>`, "Pat Example please review"},
		{"span mention keeps display text", `<p><span itemscope itemtype="http://schema.skype.com/Mention" itemid="0">Pat Example</span> hi</p>`, "Pat Example hi"},
		{"emoji literal", "<p>Party \U0001F389\U0001F680 \U0001F468\u200d\U0001F469\u200d\U0001F467</p>", "Party \U0001F389\U0001F680 \U0001F468\u200d\U0001F469\u200d\U0001F467"},
		{"emoji element uses alt", `<p>hi <emoji id="smile" alt="` + "\U0001F603" + `" title="Smile"></emoji> there</p>`, "hi \U0001F603 there"},
		{"emoji image uses alt", `<p><img itemtype="http://schema.skype.com/Emoji" alt="` + "\U0001F44D" + `" src="x"></p>`, "\U0001F44D"},
		{"rtl", `<p dir="rtl">مرحبا بالعالم שלום</p>`, "مرحبا بالعالم שלום"},
		{"nested blockquote", "<blockquote><blockquote>inner</blockquote>outer</blockquote><p>reply</p>", "inner\nouter\nreply"},
		{"link keeps text", `<a href="https://example.com/?a=1&amp;b=2">the docs</a>`, "the docs"},
		{"image only", `<p><img src="https://x/views/imgo" itemtype="http://schema.skype.com/AMSImage" alt="image"></p>`, ""},
		{"card attachment only", `<attachment id="card-1"></attachment>`, ""},
		{"script and style dropped", "<style>p{color:red}</style>a<script>alert(1)</script>b", "ab"},
		{"comment dropped", "a<!-- hidden -->b", "ab"},
		{"whitespace collapses", "<p>  a \n\t b  </p>", "a b"},
		{"literal angle bracket", "1 < 2 and 3 > 2", "1 < 2 and 3 > 2"},
		{"unterminated tag", "abc <b", "abc"},
		{"attribute with angle bracket", `<a title="a>b">x</a>`, "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HTMLToText(c.in); got != c.want {
				t.Errorf("HTMLToText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
