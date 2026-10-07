package outlookmail

import (
	"html"
	"strings"
)

// blockTags are the tags that end a run of text: a gap is put where one starts or ends, so
// "<p>a</p><p>b</p>" reads "a b" and "<b>a</b>b" reads "ab".
var blockTags = map[string]bool{
	"p": true, "br": true, "div": true, "tr": true, "td": true, "th": true, "li": true, "ul": true, "ol": true,
	"table": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "hr": true,
	"blockquote": true, "pre": true, "title": true, "head": true, "body": true, "html": true,
	"script": true, "style": true,
}

// HTMLText turns an HTML body into plain text: tags are dropped, script and style content is
// dropped, comments are dropped, entities are decoded and whitespace (the no-break space
// included) is collapsed to single spaces. It only reads the bytes it is given; an image, link
// or frame in the HTML is never fetched. NUL bytes are dropped. Bytes that are not valid UTF-8 become U+FFFD.
func HTMLText(src []byte) string {
	s := strings.ToValidUTF8(strings.ReplaceAll(string(src), "\x00", ""), "\ufffd")
	lower := asciiLower(s)
	var out strings.Builder
	for i := 0; i < len(s); {
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			out.WriteString(s[i:])
			break
		}
		out.WriteString(s[i : i+lt])
		i += lt
		next := i + 1
		if next >= len(s) || !startsTag(s[next]) {
			out.WriteByte('<') // a less-than sign that starts no tag
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		end := tagEnd(s, i)
		if end < 0 {
			break
		}
		inside := lower[i+1 : end]
		name := tagName(inside)
		i = end + 1
		if (name == "script" || name == "style") && !strings.HasPrefix(inside, "/") {
			end2 := strings.Index(lower[i:], "</"+name)
			if end2 < 0 {
				break
			}
			i += end2
			end = tagEnd(s, i)
			if end < 0 {
				break
			}
			i = end + 1
		}
		if blockTags[name] {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(out.String())), " ")
}

// tagEnd returns the index of the '>' that closes the tag starting at start, skipping quoted
// attribute values, or -1.
func tagEnd(s string, start int) int {
	var quote byte
	for i := start + 1; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		}
	}
	return -1
}

// tagName is the lower-case name at the start of the inside of a tag, without a leading '/'.
func tagName(inside string) string {
	inside = strings.TrimPrefix(inside, "/")
	n := 0
	for n < len(inside) && (isLetter(inside[n]) || n > 0 && inside[n] >= '0' && inside[n] <= '9') {
		n++
	}
	return inside[:n]
}

// startsTag reports whether c, after a '<', starts a tag, a closing tag, a declaration or a
// processing instruction.
func startsTag(c byte) bool { return isLetter(c) || c == '/' || c == '!' || c == '?' }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// asciiLower lower-cases ASCII letters only, so byte offsets match the input.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
