package outlookmail

import (
	"bytes"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func TestMapBodyInlineAndFile(t *testing.T) {
	html := []byte("<html><body>Fixture body</body></html>")
	for _, word := range []int{0, 1660, 1668, 1800} {
		b := MapBody(obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: html, WordOff: word, Lead: 79})))
		if b.State != "inline" || !bytes.Equal(b.HTML, html) || b.Path != "" {
			t.Errorf("inline at %d: %+v", word, b)
		}
	}
	const path = "~/Files/S0/2/EFMData/12345.dat"
	for _, word := range []int{0, 1524, 1748, 1784} {
		b := MapBody(obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, Path: path, WordOff: word, Lead: 59})))
		if b.State != "file" || b.Path != path || b.HTML != nil {
			t.Errorf("file at %d: %+v", word, b)
		}
	}
}

func TestMapBodyNone(t *testing.T) {
	for name, o := range map[string]*hxbuild.Object{
		"empty":    hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1}),
		"text":     hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: []byte("plain text, not html")}),
		"odd path": hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, Path: "somewhere/else.dat"}),
	} {
		if b := MapBody(obj(o)); b.State != "none" || b.HTML != nil || b.Path != "" {
			t.Errorf("%s: %+v", name, b)
		}
	}
}

func TestMapBodyInlineSniff(t *testing.T) {
	for name, tc := range map[string]struct {
		in    []byte
		state string
	}{
		"leading whitespace": {[]byte("  \r\n\t<p>x</p>"), "inline"},
		"utf-8 bom":          {append([]byte{0xef, 0xbb, 0xbf}, "<p>x</p>"...), "inline"},
		"bom then space":     {append([]byte{0xef, 0xbb, 0xbf}, " <p>x</p>"...), "inline"},
		"only whitespace":    {[]byte("   "), "none"},
		"only a bom":         {[]byte{0xef, 0xbb, 0xbf}, "none"},
		"not utf-8":          {append([]byte("<p>"), 0xff, 0xfe, 'x'), "unreadable"},
	} {
		b := MapBody(obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: tc.in})))
		if b.State != tc.state {
			t.Errorf("%s: %+v", name, b)
		}
		if tc.state != "inline" && b.HTML != nil {
			t.Errorf("%s: HTML kept for %s", name, b.State)
		}
	}
}

func TestMapBodyPairBounds(t *testing.T) {
	// A word pair whose target runs past the object is skipped, and the scan goes on to the
	// next pair that fits.
	o := hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: []byte("<p>fixture</p>")})
	o.PutU32(1000, 1<<30) // a pair far outside
	o.PutU32(1004, 20)
	if b := MapBody(obj(o)); b.State != "inline" {
		t.Fatalf("%+v", b)
	}
	// A path of odd length is not a UTF-16 string.
	p := hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, Path: "~/Files/S0/2/EFMData/1.dat"})
	p.PutU32(1528, 1<<31|51)
	if b := MapBody(obj(p)); b.State != "none" {
		t.Fatalf("%+v", b)
	}
	// A path that is not valid UTF-16 text (an unpaired surrogate) is not a path.
	q := obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, Path: "~/Files/S0/2/EFMData/1.dat"}))
	q.Raw[len(q.Raw)-4], q.Raw[len(q.Raw)-3] = 0x00, 0xd8
	if b := MapBody(q); b.State != "none" {
		t.Fatalf("%+v", b)
	}
	// An object shorter than its fixed region has nothing to scan.
	short := obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1}))
	short.Raw = short.Raw[:500]
	if b := MapBody(short); b.State != "none" {
		t.Fatalf("%+v", b)
	}
	// An unreadable lead word means no string area.
	tiny := obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1}))
	tiny.Raw = tiny.Raw[:100]
	if b := MapBody(tiny); b.State != "none" {
		t.Fatalf("%+v", b)
	}
}

func TestMapBodyPairChoice(t *testing.T) {
	html := []byte("<html>fixture body that is long enough</html>")
	// The same pair twice (the real store holds a second copy): the first stays.
	o := hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: html})
	raw := obj(o)
	copyWord, copyLen := word(raw, 1668), word(raw, 1672)
	o.PutU32(1660, copyWord)
	o.PutU32(1664, copyLen)
	if b := MapBody(obj(o)); b.State != "inline" || !bytes.Equal(b.HTML, html) {
		t.Fatalf("%+v", b)
	}
	// A body whose length word has no bit 31 still maps: the longest unflagged pair wins over
	// the small integers that point at the start of the string area by accident.
	o = hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: html})
	o.PutU32(1672, 45) // the length of html, bit 31 clear
	if len(html) != 45 {
		t.Fatal(len(html))
	}
	if b := MapBody(obj(o)); b.State != "inline" || !bytes.Equal(b.HTML, html) {
		t.Fatalf("unflagged: %+v", b)
	}
	// A flagged pair beats a longer unflagged one.
	o = hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: html})
	o.PutU32(1000, 0)
	o.PutU32(1004, u32of(len(html)+10))
	if b := MapBody(obj(o)); !bytes.Equal(b.HTML, html) {
		t.Fatalf("flagged: %+v", b)
	}
	// Inline wins over a path.
	o = hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: html})
	pathAt := o.AppendString("~/Files/S0/2/EFMData/1.dat")
	o.PutU32(1524, u32of(pathAt-hxbuild.MailBodySize))
	o.PutU32(1528, u32of(len(hxbuild.UTF16Z("~/Files/S0/2/EFMData/1.dat")))|1<<31)
	if b := MapBody(obj(o)); b.State != "inline" {
		t.Fatalf("%+v", b)
	}
}

func TestMapBodyStripsTrailingNULs(t *testing.T) {
	b := MapBody(obj(hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 1, HTML: []byte("<p>fixture</p>\x00\x00")})))
	if b.State != "inline" || string(b.HTML) != "<p>fixture</p>" {
		t.Fatalf("%+v %q", b, b.HTML)
	}
}
