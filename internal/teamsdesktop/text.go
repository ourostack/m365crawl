package teamsdesktop

import (
	"html"
	"strings"
)

// HTMLToText turns a Teams message body into plain text for search and display. Tags are dropped
// and their text kept (so an <at> or mention span leaves the person's display name); <br> and block
// elements (p, div, li, blockquote, headings, table rows) become newlines; entities are decoded;
// emoji elements and emoji images become their alt text; other images, attachments, scripts,
// styles and comments contribute nothing. Runs of whitespace inside a line collapse to one
// space, and at most one blank line is kept. The input need not be well-formed HTML.
func HTMLToText(s string) string {
	var out strings.Builder
	skipUntil := "" // closing tag name of a <script> or <style> being skipped
	pendingNL := 0  // newlines owed before the next text
	lineStart := true
	space := false // a collapsible space is owed before the next text on this line

	newline := func(n int) {
		if n > pendingNL {
			pendingNL = n
		}
	}
	text := func(t string) {
		for _, r := range t {
			switch r {
			case ' ', '\t', '\n', '\r', '\f', ' ':
				space = true
				continue
			}
			if pendingNL > 0 && out.Len() > 0 {
				out.WriteString(strings.Repeat("\n", pendingNL))
				lineStart = true
				space = false
			}
			pendingNL = 0
			if space && !lineStart {
				out.WriteByte(' ')
			}
			space = false
			lineStart = false
			out.WriteRune(r)
		}
	}

	for i := 0; i < len(s); {
		if skipUntil != "" {
			j := indexFold(s[i:], "</"+skipUntil)
			if j < 0 {
				break
			}
			i += j
			skipUntil = ""
		}
		if s[i] != '<' || !tagStart(s, i) {
			j := strings.IndexByte(s[i+1:], '<')
			end := len(s)
			if j >= 0 {
				end = i + 1 + j
			}
			text(html.UnescapeString(s[i:end]))
			i = end
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			j := strings.Index(s[i+4:], "-->")
			if j < 0 {
				break
			}
			i += 4 + j + 3
			continue
		}
		end := tagEnd(s, i)
		if end < 0 {
			break // unterminated tag: nothing after it is text
		}
		name, closing, attrs := parseTag(s[i+1 : end])
		i = end + 1
		switch name {
		case "script", "style":
			if !closing {
				skipUntil = name
			}
		case "br":
			pendingNL++
			space = false
		case "p", "div", "li", "ul", "ol", "tr", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "table", "hr":
			newline(1)
		case "emoji":
			if !closing {
				text(attrValue(attrs, "alt"))
			}
		case "img":
			if strings.Contains(strings.ToLower(attrValue(attrs, "itemtype")), "emoji") {
				text(attrValue(attrs, "alt"))
			}
		}
	}
	return strings.TrimRight(out.String(), " \n")
}

// tagStart reports whether the '<' at s[i] opens a tag, comment or closing tag rather than being
// a literal less-than sign.
func tagStart(s string, i int) bool {
	if i+1 >= len(s) {
		return false
	}
	c := s[i+1]
	return c == '/' || c == '!' || c == '?' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tagEnd returns the index of the '>' that closes the tag starting at s[i], skipping quoted
// attribute values, or -1.
func tagEnd(s string, i int) int {
	var quote byte
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j
		}
	}
	return -1
}

func parseTag(body string) (name string, closing bool, attrs string) {
	body = strings.TrimSpace(strings.TrimSuffix(body, "/"))
	if strings.HasPrefix(body, "/") {
		closing = true
		body = strings.TrimSpace(body[1:])
	}
	end := strings.IndexAny(body, " \t\r\n/")
	if end < 0 {
		return strings.ToLower(body), closing, ""
	}
	return strings.ToLower(body[:end]), closing, body[end:]
}

// attrValue returns the decoded value of attribute key in a tag's attribute text.
func attrValue(attrs, key string) string {
	low := asciiLower(attrs)
	for from := 0; ; {
		j := strings.Index(low[from:], key+"=")
		if j < 0 {
			return ""
		}
		j += from
		if j > 0 && !strings.ContainsRune(" \t\r\n", rune(attrs[j-1])) {
			from = j + 1
			continue
		}
		rest := attrs[j+len(key)+1:]
		if rest == "" {
			return ""
		}
		if q := rest[0]; q == '"' || q == '\'' {
			if e := strings.IndexByte(rest[1:], q); e >= 0 {
				return html.UnescapeString(rest[1 : 1+e])
			}
			return ""
		}
		if e := strings.IndexAny(rest, " \t\r\n"); e >= 0 {
			rest = rest[:e]
		}
		return html.UnescapeString(rest)
	}
}

// indexFold is strings.Index with ASCII case folding, so byte offsets stay valid for any input.
// It copies nothing: it compares in place, byte by byte.
func indexFold(s, sub string) int {
	if sub == "" {
		return 0
	}
	lo := lowerByte(sub[0])
	for i := 0; i+len(sub) <= len(s); i++ {
		if lowerByte(s[i]) != lo {
			continue
		}
		j := 1
		for j < len(sub) && lowerByte(s[i+j]) == lowerByte(sub[j]) {
			j++
		}
		if j == len(sub) {
			return i
		}
	}
	return -1
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		b[i] = lowerByte(c)
	}
	return string(b)
}
