package outlookowacal

import "unicode/utf8"

// Cached previews can end at a UTF-16 code-unit boundary inside a surrogate pair.
// Render those decoded WTF-8 units as one Unicode replacement character each;
// arbitrary malformed bytes remain an observation failure.
func previewUnicode(text string) (string, bool) {
	out := make([]byte, 0, len(text))
	for len(text) > 0 {
		_, size := utf8.DecodeRuneInString(text)
		if size != 1 || text[0] < utf8.RuneSelf {
			out = append(out, text[:size]...)
			text = text[size:]
			continue
		}
		if len(text) < 3 || text[0] != 0xed || text[1] < 0xa0 || text[1] > 0xbf || text[2] < 0x80 || text[2] > 0xbf {
			return "", false
		}
		out = utf8.AppendRune(out, utf8.RuneError)
		text = text[3:]
	}
	return string(out), true
}
